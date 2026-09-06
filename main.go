// Zeno — plataforma de deploy self-hosted. Um binário, uma VPS.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

// versao é preenchida pelo workflow de release via -ldflags na build
// (`-X main.versao=v1.2.3`); `go run .`/`go build` locais ficam com "dev".
var versao = "dev"

func main() {
	log.Printf("zeno %s", versao)

	db, err := abrirDB(env("ZENO_DB", "zeno.db"))
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	defer db.Close()

	// Não conecta aqui — só monta o cliente. Docker fora do ar vira erro na
	// tela, não binário que se recusa a subir.
	dk, err := novoDocker()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}
	defer dk.Close()

	presets, err := carregarPresets(env("ZENO_PRESETS", "/etc/zeno/presets"))
	if err != nil {
		log.Fatalf("presets: %v", err)
	}
	log.Printf("%d presets carregados", len(presets))

	addr := env("ZENO_ADDR", ":8080")
	ng := &nginx{dk: dk, db: db}
	sv := &servicos{dk: dk, db: db, presets: presets}
	dp := &deploys{
		dk: dk, db: db, ng: ng, sv: sv,
		logDir: env("ZENO_LOGS", "logs"),
		porta:  porta(addr),
	}
	// Fecha o ciclo: o nginx precisa dos vhosts das apps, que só o motor de
	// deploy conhece. Injetado aqui para o nginx não depender do deploy.
	ng.vhostsExtra = dp.vhostsDasApps

	acmeCfg := acmeConfigDoAmbiente()
	if err := garantirConfigSegura(acmeCfg); err != nil {
		log.Fatalf("config ACME: %v", err)
	}
	ac := &acme{db: db, dk: dk, ng: ng, cfg: acmeCfg, portaZeno: porta(addr)}
	// Sempre em goroutine: emissão fala com uma CA externa pela rede, e
	// sincronizar() é chamado de dentro de handler HTTP — não pode travar
	// a resposta esperando um round-trip que pode levar segundos.
	ng.aposSincronizar = func(hosts []string) { ac.garantirCertificados(context.Background(), hosts) }
	ng.aposCriarContainer = ac.rehidratarCerts

	// Em background: o primeiro boot puxa a imagem do nginx e isso leva um
	// minuto. Nada disso pode atrasar o ListenAndServe — a tela precisa estar
	// no ar justamente para mostrar o que está acontecendo. Falhar aqui também
	// não é fatal: sem nginx o Zeno ainda serve a UI direto na porta dele.
	ng.setEstado("provisionando", "")
	go func() {
		if err := subirNginx(ng, porta(addr)); err != nil {
			log.Printf("nginx: %v", err)
			ng.setEstado("erro", err.Error())
			return
		}
		log.Printf("nginx pronto")
		ng.setEstado("pronto", "")
	}()

	mux := http.NewServeMux()
	// Fora de qualquer lista de rotas "protegidas": esse path é público por
	// definição — a CA precisa alcançá-lo sem cabeçalho, sem cookie, nada.
	mux.HandleFunc("GET /.well-known/acme-challenge/{token}", ac.handlerChallenge)
	mux.HandleFunc("GET /api/containers", listarContainers(dk))
	mux.HandleFunc("GET /api/nginx", statusNginx(ng, addr))
	mux.HandleFunc("POST /api/nginx/aplicar", aplicarNginx(ng, addr))
	mux.HandleFunc("GET /api/certs", listarCerts(ac))
	rotasNginx(mux, ng, porta(addr))
	rotasServicos(mux, sv)
	rotasDeploy(mux, dp)
	mux.Handle("GET /", spa())

	go ac.renovarPeriodicamente(context.Background(), func() []string {
		hosts, err := ac.hostnamesComSSL()
		if err != nil {
			log.Printf("listar hostnames com SSL: %v", err)
			return nil
		}
		return hosts
	})

	log.Printf("zeno ouvindo em http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func listarCerts(ac *acme) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		certs, err := ac.todosCerts()
		if err != nil {
			erroHTTP(w, http.StatusInternalServerError, err.Error())
			return
		}
		if certs == nil {
			certs = []certInfo{}
		}
		responder(w, http.StatusOK, certs)
	}
}

func subirNginx(ng *nginx, portaZeno string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := ng.garantir(ctx); err != nil {
		return err
	}
	return ng.sincronizar(ctx, portaZeno)
}

func statusNginx(ng *nginx, addr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, rodando, err := ng.dk.acharContainer(r.Context(), nginxNome)
		if err != nil {
			responder(w, http.StatusBadGateway, map[string]string{
				"erro": "Docker inacessível: " + err.Error(),
			})
			return
		}

		var aplicadoEm, erroUltima string
		var okUltima bool
		// Última tentativa de apply, tenha dado certo ou não.
		_ = ng.db.QueryRow(
			`SELECT aplicado_em, ok, COALESCE(erro, '') FROM nginx_versions
			 WHERE arquivo = ? ORDER BY id DESC LIMIT 1`, arquivoApps,
		).Scan(&aplicadoEm, &okUltima, &erroUltima)

		fase, erroFase := ng.estado()
		if erroFase != "" {
			erroUltima = erroFase
		}

		gerados, _ := ng.carregarServers(true)
		doUsuario, _ := ng.carregarServers(false)
		_ = ng.marcarCertsProntos(gerados)
		_ = ng.marcarCertsProntos(doUsuario)

		responder(w, http.StatusOK, map[string]any{
			"container":    nginxNome,
			"fase":         fase,
			"existe":       id != "",
			"rodando":      rodando,
			"imagem":       nginxImagem,
			"conf":         renderConf(gerados, porta(addr)),
			"conf_usuario": renderConf(doUsuario, porta(addr)),
			"aplicado_em":  aplicadoEm,
			"ok":           okUltima,
			"erro":         erroUltima,
		})
	}
}

func aplicarNginx(ng *nginx, addr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ng.sincronizar(r.Context(), porta(addr)); err != nil {
			responder(w, http.StatusUnprocessableEntity, map[string]string{"erro": err.Error()})
			return
		}
		responder(w, http.StatusOK, map[string]string{"status": "aplicado"})
	}
}

// porta extrai "8080" de ":8080" ou "0.0.0.0:8080".
func porta(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return "8080"
}

func listarContainers(dk *docker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cs, err := dk.listar(r.Context())
		if err != nil {
			// 502: o problema é o Docker, não a requisição.
			responder(w, http.StatusBadGateway, map[string]string{
				"erro": "Docker inacessível: " + err.Error(),
			})
			return
		}
		responder(w, http.StatusOK, cs)
	}
}

// spa serve o front embutido; qualquer rota desconhecida cai no index.html
// para o router do client resolver.
func spa() http.Handler {
	dist, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		log.Fatalf("front não embutido (rode `npm run build` em web/): %v", err)
	}
	arquivos := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caminho := strings.TrimPrefix(r.URL.Path, "/")
		if caminho == "" {
			caminho = "index.html"
		}
		if _, err := fs.Stat(dist, caminho); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		arquivos.ServeHTTP(w, r)
	})
}

func responder(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("resposta json: %v", err)
	}
}

func env(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}
