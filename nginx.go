package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	nginxNome      = "zeno-nginx"
	nginxImagem    = "nginx:1.29-alpine"
	nginxConfDir   = "/etc/nginx/conf.d"
	arquivoApps    = "zeno-apps.conf" // gerado pelo Zeno; a UI não edita
	arquivoUsuario = "user.conf"      // o que o editor da tela escreve
	arquivoPadrao  = "default.conf"   // vhost que vem na imagem, atrapalha
)

type nginx struct {
	dk *docker
	db *sql.DB

	// vhostsExtra fornece os server blocks das apps em deploy. Injetado
	// depois porque o motor de deploy depende do nginx, não o contrário.
	vhostsExtra func() ([]nginxServer, error)

	// aposSincronizar dispara emissão/renovação de certificado para quem
	// ainda não tem um válido. Roda em goroutine própria — nunca pode
	// atrasar sincronizar(), que é chamado de dentro de handlers HTTP.
	aposSincronizar func(hostnamesComSSL []string)

	// aposCriarContainer rehidrata certs já emitidos assim que o container
	// do nginx é criado do zero — o filesystem dele é novo, o banco não.
	aposCriarContainer func(ctx context.Context)

	// O primeiro boot puxa a imagem, o que leva um minuto ou mais. Provisionar
	// em background e reportar a fase aqui: control plane que não responde
	// enquanto baixa imagem é inútil justo quando você quer olhar o que houve.
	mu   sync.Mutex
	fase string // provisionando | pronto | erro
	erro string
}

func (n *nginx) setEstado(fase, erro string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fase, n.erro = fase, erro
}

func (n *nginx) estado() (fase, erro string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fase, n.erro
}

// --- modelo -----------------------------------------------------------------

type nginxServer struct {
	ID         int64  `json:"id"`
	ServerName string `json:"server_name"`
	SSL        bool   `json:"ssl"`
	Gerado     bool   `json:"gerado"` // do Zeno: a tela mostra, mas não edita
	// Padrao emite `default_server`. Sem isso o catch-all é só o primeiro
	// server block que o nginx carregar — e conf.d/ carrega em ordem
	// alfabética, então user.conf viria antes de zeno-apps.conf e qualquer
	// vhost criado pelo usuário sequestraria o tráfego da própria UI.
	// (`server_name _` não é catch-all: é um nome que nunca casa.)
	Padrao bool `json:"padrao"`
	// CertPronto é preenchido pelo chamador de renderConf (sincronizar),
	// nunca calculado aqui dentro — renderConf fica puro e testável sem
	// banco. true = existe certificado válido gravado para este hostname.
	CertPronto bool            `json:"cert_pronto"`
	Locations  []nginxLocation `json:"locations"`
}

type nginxLocation struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Tipo string `json:"tipo"` // proxy | redirect | static
	Alvo string `json:"alvo"`
	// Dinamico resolve o upstream a cada request, em vez de uma vez no boot.
	// Obrigatório para container de app: o deploy recria o container com IP
	// novo e o nginx serviria 502 apontando pro IP velho até reiniciar.
	// NÃO usar para alvo que vem do /etc/hosts (host.docker.internal): com
	// variável o nginx consulta só o resolver, e o /etc/hosts é ignorado.
	Dinamico bool `json:"dinamico"`
	// SSE (logs ao vivo) morre se o nginx bufferizar a resposta.
	Stream bool `json:"stream"`
}

// --- render -----------------------------------------------------------------

// renderConf recebe portaZeno só para montar a location do desafio ACME —
// ela sempre aponta para o mesmo lugar que a UI (host.docker.internal), o
// endereço já provado no vhost do Zeno.
func renderConf(servers []nginxServer, portaZeno string) string {
	var b strings.Builder
	b.WriteString("# Gerado pelo Zeno — não edite à mão, some no próximo apply.\n")

	escreverChallenge := func() {
		b.WriteString("\n    location /.well-known/acme-challenge/ {\n")
		fmt.Fprintf(&b, "        proxy_pass http://host.docker.internal:%s;\n", portaZeno)
		b.WriteString("        proxy_set_header Host $host;\n")
		b.WriteString("    }\n")
	}

	escreverLocations := func(locs []nginxLocation) {
		for _, l := range locs {
			fmt.Fprintf(&b, "\n    location %s {\n", l.Path)
			switch l.Tipo {
			case "redirect":
				fmt.Fprintf(&b, "        return 301 %s;\n", l.Alvo)
			case "static":
				fmt.Fprintf(&b, "        root %s;\n        try_files $uri $uri/ =404;\n", l.Alvo)
			default: // proxy
				if l.Dinamico {
					b.WriteString("        resolver 127.0.0.11 valid=10s;\n")
					fmt.Fprintf(&b, "        set $upstream %s;\n", l.Alvo)
					b.WriteString("        proxy_pass $upstream;\n")
				} else {
					fmt.Fprintf(&b, "        proxy_pass %s;\n", l.Alvo)
				}
				b.WriteString("        proxy_http_version 1.1;\n")
				b.WriteString("        proxy_set_header Host $host;\n")
				b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
				b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
				b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
				if l.Stream {
					b.WriteString("        proxy_buffering off;\n")
					b.WriteString("        proxy_cache off;\n")
					b.WriteString("        proxy_read_timeout 1h;\n")
				}
			}
			b.WriteString("    }\n")
		}
	}

	for _, s := range servers {
		listen80 := "listen 80;"
		listen443 := "listen 443 ssl;"
		if s.Padrao {
			listen80 = "listen 80 default_server;"
			listen443 = "listen 443 ssl default_server;"
		}

		switch {
		case s.SSL && s.CertPronto:
			// Certificado emitido: a porta 80 só existe para o desafio de
			// renovação e para redirecionar; o tráfego de verdade vai na 443.
			fmt.Fprintf(&b, "\nserver {\n    %s\n    server_name %s;\n", listen80, s.ServerName)
			escreverChallenge()
			b.WriteString("\n    location / {\n        return 301 https://$host$request_uri;\n    }\n")
			b.WriteString("}\n")

			certPath, keyPath := caminhosCert(s.ServerName)
			fmt.Fprintf(&b, "\nserver {\n    %s\n    server_name %s;\n", listen443, s.ServerName)
			fmt.Fprintf(&b, "    ssl_certificate %s;\n    ssl_certificate_key %s;\n", certPath, keyPath)
			escreverLocations(s.Locations)
			b.WriteString("}\n")

		case s.SSL:
			// Quer SSL mas ainda não tem certificado: serve HTTP normal com
			// a location do desafio, sem redirecionar — redirecionar antes
			// de existir certificado deixaria o domínio inacessível.
			fmt.Fprintf(&b, "\nserver {\n    %s\n    server_name %s;\n", listen80, s.ServerName)
			escreverChallenge()
			escreverLocations(s.Locations)
			b.WriteString("}\n")

		default:
			fmt.Fprintf(&b, "\nserver {\n    %s\n    server_name %s;\n", listen80, s.ServerName)
			escreverLocations(s.Locations)
			b.WriteString("}\n")
		}
	}
	return b.String()
}

// vhostZeno roteia a própria UI do Zeno — primeiro caso de teste do proxy.
// O binário roda no host, não em container, daí o host.docker.internal.
func vhostZeno(portaZeno string) nginxServer {
	return nginxServer{
		ServerName: "_",
		Padrao:     true, // recebe tudo que não casa com nenhum vhost nomeado
		Locations: []nginxLocation{{
			Path: "/",
			Tipo: "proxy",
			Alvo: "http://host.docker.internal:" + portaZeno,
			// Estático de propósito: host.docker.internal vem do /etc/hosts,
			// que a forma com variável não enxerga. E o endereço não muda.
			Dinamico: false,
			Stream:   true, // a UI usa SSE para logs
		}},
	}
}

// --- container --------------------------------------------------------------

func (n *nginx) garantir(ctx context.Context) error {
	if err := n.dk.garantirRede(ctx, redeZeno); err != nil {
		return fmt.Errorf("rede %s: %w", redeZeno, err)
	}

	id, rodando, err := n.dk.acharContainer(ctx, nginxNome)
	if err != nil {
		return err
	}
	if id != "" {
		if !rodando {
			_, err = n.dk.ContainerStart(ctx, id, client.ContainerStartOptions{})
		}
		return err
	}

	if err := n.dk.garantirImagem(ctx, nginxImagem); err != nil {
		return fmt.Errorf("pull %s: %w", nginxImagem, err)
	}

	http80 := network.MustParsePort("80/tcp")
	https443 := network.MustParsePort("443/tcp")

	criado, err := n.dk.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: nginxNome,
		Config: &container.Config{
			Image: nginxImagem,
			Labels: map[string]string{
				labelManaged: "true",
				labelTipo:    "service",
				labelNome:    "nginx",
			},
			ExposedPorts: network.PortSet{http80: struct{}{}, https443: struct{}{}},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(redeZeno),
			PortBindings: network.PortMap{
				http80:   {{HostPort: env("ZENO_HTTP_PORT", "80")}},
				https443: {{HostPort: env("ZENO_HTTPS_PORT", "443")}},
			},
			// Sem isso o nginx não alcança o binário do Zeno no host quando
			// roda em Linux. No Docker Desktop já vem de graça.
			ExtraHosts:    []string{"host.docker.internal:host-gateway"},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
	})
	if err != nil {
		return fmt.Errorf("criar %s: %w", nginxNome, err)
	}

	if _, err := n.dk.ContainerStart(ctx, criado.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("iniciar %s: %w", nginxNome, err)
	}

	// O vhost default da imagem responde em `_` e roubaria o tráfego do nosso.
	_, _, _ = n.dk.exec(ctx, criado.ID, []string{"rm", "-f", nginxConfDir + "/" + arquivoPadrao})

	if n.aposCriarContainer != nil {
		n.aposCriarContainer(ctx)
	}
	return nil
}

// --- aplicar ----------------------------------------------------------------

// aplicar grava a config, valida com o próprio nginx e só então recarrega.
// Config inválida no disco não afeta quem está no ar: o nginx só relê no
// reload. Se o teste falhar, o arquivo anterior volta e nada é recarregado.
func (n *nginx) aplicar(ctx context.Context, arquivo, conf string) error {
	id, rodando, err := n.dk.acharContainer(ctx, nginxNome)
	if err != nil {
		return err
	}
	if id == "" || !rodando {
		return fmt.Errorf("container %s não está rodando", nginxNome)
	}

	// Se o arquivo ainda não existe, o `cat` devolve a própria mensagem de
	// erro no stdout. Sem olhar o exit code, esse texto viraria o "anterior"
	// e seria escrito como config na hora de desfazer.
	anterior, codeCat, err := n.dk.exec(ctx, id, []string{"cat", nginxConfDir + "/" + arquivo})
	if err != nil {
		return err
	}
	existia := codeCat == 0
	if !existia {
		anterior = ""
	}

	if err := n.dk.escreverArquivo(ctx, id, nginxConfDir, arquivo, []byte(conf)); err != nil {
		return err
	}

	saida, code, err := n.dk.exec(ctx, id, []string{"nginx", "-t"})
	if err != nil {
		return err
	}
	if code != 0 {
		// Volta o que estava valendo antes de qualquer reload.
		if existia {
			_ = n.dk.escreverArquivo(ctx, id, nginxConfDir, arquivo, []byte(anterior))
		} else {
			_, _, _ = n.dk.exec(ctx, id, []string{"rm", "-f", nginxConfDir + "/" + arquivo})
		}
		n.gravarVersao(arquivo, conf, false, saida)
		return fmt.Errorf("nginx -t recusou a config:\n%s", saida)
	}

	if saidaReload, code, err := n.dk.exec(ctx, id, []string{"nginx", "-s", "reload"}); err != nil {
		return err
	} else if code != 0 {
		n.gravarVersao(arquivo, conf, false, saidaReload)
		return fmt.Errorf("reload falhou: %s", saidaReload)
	}

	n.gravarVersao(arquivo, conf, true, "")
	return nil
}

func (n *nginx) gravarVersao(arquivo, conf string, ok bool, erro string) {
	_, err := n.db.Exec(
		`INSERT INTO nginx_versions (arquivo, conf, aplicado_em, ok, erro) VALUES (?, ?, ?, ?, ?)`,
		arquivo, conf, time.Now().UTC().Format(time.RFC3339), ok, erro,
	)
	if err != nil {
		log.Printf("gravar versão nginx: %v", err)
	}
}

// --- persistência do modelo -------------------------------------------------

// garantirVhostZeno cria, uma vez, a linha do vhost que roteia a própria UI.
// Marcado gerado=1: aparece na tela, mas o editor não deixa mexer.
func (n *nginx) garantirVhostZeno(portaZeno string) error {
	var id int64
	err := n.db.QueryRow(`SELECT id FROM nginx_servers WHERE gerado = 1 AND server_name = '_'`).Scan(&id)
	if err == nil {
		// O endereço da UI pode ter mudado entre execuções.
		_, err = n.db.Exec(`UPDATE nginx_locations SET alvo = ? WHERE server_id = ?`,
			"http://host.docker.internal:"+portaZeno, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}

	res, err := n.db.Exec(
		`INSERT INTO nginx_servers (server_name, ssl, gerado, padrao) VALUES ('_', 0, 1, 1)`)
	if err != nil {
		return err
	}
	id, _ = res.LastInsertId()
	_, err = n.db.Exec(
		`INSERT INTO nginx_locations (server_id, path, tipo, alvo, dinamico, stream)
		 VALUES (?, '/', 'proxy', ?, 0, 1)`,
		id, "http://host.docker.internal:"+portaZeno)
	return err
}

func (n *nginx) carregarServers(gerado bool) ([]nginxServer, error) {
	linhas, err := n.db.Query(
		`SELECT id, server_name, ssl, padrao FROM nginx_servers WHERE gerado = ? ORDER BY id`, gerado)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var servers []nginxServer
	for linhas.Next() {
		var s nginxServer
		if err := linhas.Scan(&s.ID, &s.ServerName, &s.SSL, &s.Padrao); err != nil {
			return nil, err
		}
		s.Gerado = gerado
		servers = append(servers, s)
	}
	if err := linhas.Err(); err != nil {
		return nil, err
	}

	for i := range servers {
		locs, err := n.carregarLocations(servers[i].ID)
		if err != nil {
			return nil, err
		}
		servers[i].Locations = locs
	}
	return servers, nil
}

func (n *nginx) carregarLocations(serverID int64) ([]nginxLocation, error) {
	linhas, err := n.db.Query(
		`SELECT id, path, tipo, alvo, dinamico, stream FROM nginx_locations
		 WHERE server_id = ? ORDER BY id`, serverID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []nginxLocation
	for linhas.Next() {
		var l nginxLocation
		if err := linhas.Scan(&l.ID, &l.Path, &l.Tipo, &l.Alvo, &l.Dinamico, &l.Stream); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, linhas.Err()
}

// sincronizar re-renderiza os dois arquivos e aplica. O banco é a fonte da
// verdade; o container é estado derivado, reconstruído a cada boot.
//
// Dois arquivos de propósito: o gerado é do Zeno e some se editado à mão, o
// do usuário é o que o editor da tela escreve. Misturar os dois num arquivo
// só faria o próximo deploy apagar o trabalho de alguém.
func (n *nginx) sincronizar(ctx context.Context, portaZeno string) error {
	if err := n.garantirVhostZeno(portaZeno); err != nil {
		return err
	}

	gerados, err := n.carregarServers(true)
	if err != nil {
		return err
	}
	// Vhosts das apps não vêm de nginx_servers: são derivados de domains +
	// apps.container_atual, que muda a cada deploy. Guardar isso em tabela
	// própria seria manter duas verdades sobre a mesma coisa.
	if n.vhostsExtra != nil {
		extras, err := n.vhostsExtra()
		if err != nil {
			return err
		}
		gerados = append(gerados, extras...)
	}
	if err := n.marcarCertsProntos(gerados); err != nil {
		return err
	}
	if err := n.aplicar(ctx, arquivoApps, renderConf(gerados, portaZeno)); err != nil {
		return err
	}

	doUsuario, err := n.carregarServers(false)
	if err != nil {
		return err
	}
	if err := n.marcarCertsProntos(doUsuario); err != nil {
		return err
	}
	if err := n.aplicar(ctx, arquivoUsuario, renderConf(doUsuario, portaZeno)); err != nil {
		return err
	}

	if n.aposSincronizar != nil {
		var comSSL []string
		for _, s := range append(gerados, doUsuario...) {
			if s.SSL {
				comSSL = append(comSSL, s.ServerName)
			}
		}
		if len(comSSL) > 0 {
			go n.aposSincronizar(comSSL)
		}
	}
	return nil
}

// marcarCertsProntos preenche CertPronto olhando o que já foi emitido — o
// renderConf continua puro, quem decide "tem cert válido" é quem tem banco.
func (n *nginx) marcarCertsProntos(servers []nginxServer) error {
	for i := range servers {
		if !servers[i].SSL {
			continue
		}
		var expiraStr string
		err := n.db.QueryRow(`SELECT expira_em FROM certs WHERE hostname = ?`, servers[i].ServerName).Scan(&expiraStr)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		expira, err := time.Parse(time.RFC3339, expiraStr)
		// Certificado vencido não conta como pronto: melhor cair para o
		// modo "provisionando" (HTTP puro) do que servir HTTPS com cert
		// expirado até a renovação terminar.
		servers[i].CertPronto = err == nil && time.Now().Before(expira)
	}
	return nil
}
