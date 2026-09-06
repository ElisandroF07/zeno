package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type deploys struct {
	dk     *docker
	db     *sql.DB
	ng     *nginx
	sv     *servicos
	logDir string
	porta  string // porta da UI do Zeno, para re-sincronizar o nginx
}

type App struct {
	ID           int64   `json:"id"`
	Nome         string  `json:"nome"`
	RepoURL      string  `json:"repo_url"`
	Branch       string  `json:"branch"`
	Porta        int     `json:"porta"`
	Dominio      string  `json:"dominio"`
	Estado       string  `json:"estado"`
	Container    string  `json:"container"`
	UltimoDeploy *Deploy `json:"ultimo_deploy,omitempty"`
}

type Deploy struct {
	ID        int64  `json:"id"`
	AppID     int64  `json:"app_id"`
	ImagemTag string `json:"imagem_tag"`
	Status    string `json:"status"` // building | success | failed
	CommitSHA string `json:"commit_sha"`
	CriadoEm  string `json:"criado_em"`
	Atual     bool   `json:"atual"`
	// Nunca vai para o JSON: pode carregar segredo injetado por serviço.
	// Existe só para o rollback recriar o container com o env exato daquele
	// deploy — sem isso, rollback usaria o env de HOJE, que pode ter mudado.
	EnvSnapshot string `json:"-"`
}

var nomeAppValido = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// Container versionado por deploy: o novo sobe ao lado do antigo e só então o
// nginx passa a apontar para ele. Nome fixo impediria os dois coexistirem, e
// sem coexistência não existe troca sem downtime.
func containerDoDeploy(app string, deployID int64) string {
	return fmt.Sprintf("zeno-app-%s-%d", app, deployID)
}

// containerAtualDeployID lê o deployID de volta do nome do container. Formato
// é sempre "...-<deployID>" (containerDoDeploy garante isso), então pegar o
// que vem depois do último hífen é seguro mesmo com hífen no nome da app.
func containerAtualDeployID(nomeContainer string) (int64, bool) {
	i := strings.LastIndex(nomeContainer, "-")
	if i < 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(nomeContainer[i+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (d *deploys) criarApp(ctx context.Context, a App) (int64, error) {
	if !nomeAppValido.MatchString(a.Nome) {
		return 0, fmt.Errorf("nome inválido: minúsculas, números e hífen (2 a 32 caracteres)")
	}
	if a.RepoURL == "" {
		return 0, fmt.Errorf("repo_url é obrigatório")
	}
	// Branch vazio fica vazio: clonar() sem --branch deixa o git resolver
	// sozinho a branch padrão do remoto — forçar "main" aqui quebrava o
	// clone de qualquer repo que não use esse nome (ainda comum: "master").
	if a.Porta == 0 {
		a.Porta = 3000
	}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO apps (nome, repo_url, branch, porta) VALUES (?, ?, ?, ?)`,
		a.Nome, a.RepoURL, a.Branch, a.Porta)
	if err != nil {
		return 0, fmt.Errorf("gravar app (nome já usado?): %w", err)
	}
	id, _ := res.LastInsertId()

	if a.Dominio != "" {
		if _, err := d.db.ExecContext(ctx,
			`INSERT INTO domains (app_id, hostname) VALUES (?, ?)`, id, a.Dominio); err != nil {
			return id, fmt.Errorf("gravar domínio: %w", err)
		}
	}
	return id, nil
}

func (d *deploys) listarApps(ctx context.Context) ([]App, error) {
	linhas, err := d.db.QueryContext(ctx,
		`SELECT a.id, a.nome, a.repo_url, a.branch, a.porta,
		        COALESCE(a.container_atual, ''), COALESCE(dm.hostname, '')
		 FROM apps a LEFT JOIN domains dm ON dm.app_id = a.id
		 ORDER BY a.nome`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var apps []App
	for linhas.Next() {
		var a App
		if err := linhas.Scan(&a.ID, &a.Nome, &a.RepoURL, &a.Branch, &a.Porta,
			&a.Container, &a.Dominio); err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	if err := linhas.Err(); err != nil {
		return nil, err
	}

	estados := map[string]string{}
	if cs, err := d.dk.listar(ctx); err == nil {
		for _, c := range cs {
			estados[c.Nome] = c.Estado
		}
	}

	for i := range apps {
		apps[i].Estado = estados[apps[i].Container]
		if apps[i].Estado == "" {
			apps[i].Estado = "sem deploy"
		}
		apps[i].UltimoDeploy, _ = d.ultimoDeploy(ctx, apps[i].ID)
	}
	return apps, nil
}

func (d *deploys) ultimoDeploy(ctx context.Context, appID int64) (*Deploy, error) {
	var dp Deploy
	err := d.db.QueryRowContext(ctx,
		`SELECT id, app_id, imagem_tag, status, COALESCE(commit_sha,''), created_at
		 FROM deploys WHERE app_id = ? ORDER BY id DESC LIMIT 1`, appID,
	).Scan(&dp.ID, &dp.AppID, &dp.ImagemTag, &dp.Status, &dp.CommitSHA, &dp.CriadoEm)
	if err != nil {
		return nil, err
	}
	return &dp, nil
}

func (d *deploys) caminhoLog(deployID int64) string {
	return filepath.Join(d.logDir, fmt.Sprintf("deploy-%d.log", deployID))
}

// iniciarDeploy grava o registro e devolve na hora: o build leva minutos e a
// requisição não pode esperar. O acompanhamento é pelo log via SSE.
func (d *deploys) iniciarDeploy(ctx context.Context, appID int64) (int64, error) {
	var a App
	err := d.db.QueryRowContext(ctx,
		`SELECT id, nome, repo_url, branch, porta FROM apps WHERE id = ?`, appID,
	).Scan(&a.ID, &a.Nome, &a.RepoURL, &a.Branch, &a.Porta)
	if err != nil {
		return 0, fmt.Errorf("app %d não existe", appID)
	}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO deploys (app_id, imagem_tag, status) VALUES (?, '', 'building')`, appID)
	if err != nil {
		return 0, err
	}
	deployID, _ := res.LastInsertId()

	if err := os.MkdirAll(d.logDir, 0o755); err != nil {
		return deployID, err
	}
	_, _ = d.db.Exec(`UPDATE deploys SET log_path = ? WHERE id = ?`,
		d.caminhoLog(deployID), deployID)

	// Contexto próprio: o deploy sobrevive ao fim da requisição HTTP.
	go d.executar(context.Background(), a, deployID)
	return deployID, nil
}

func (d *deploys) executar(ctx context.Context, a App, deployID int64) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	log, err := os.Create(d.caminhoLog(deployID))
	if err != nil {
		d.finalizar(deployID, "failed", "", "")
		return
	}
	defer log.Close()

	falhar := func(formato string, args ...any) {
		fmt.Fprintf(log, "\n✗ "+formato+"\n", args...)
		d.finalizar(deployID, "failed", "", "")
	}

	dir, err := os.MkdirTemp("", fmt.Sprintf("zeno-%s-", a.Nome))
	if err != nil {
		falhar("criar diretório temporário: %v", err)
		return
	}
	defer os.RemoveAll(dir)

	fmt.Fprintf(log, "=== clone de %s (%s)\n", a.RepoURL, a.Branch)
	sha, err := clonar(ctx, a.RepoURL, a.Branch, dir, log)
	if err != nil {
		falhar("%v", err)
		return
	}
	fmt.Fprintf(log, "commit %s\n", sha)

	estrategia := detectarEstrategia(dir)
	tag := fmt.Sprintf("zeno/%s:%s", a.Nome, sha)
	fmt.Fprintf(log, "\n=== build (%s) → %s\n", estrategia, tag)

	if err := d.buildar(ctx, dir, tag, estrategia, log); err != nil {
		falhar("%v", err)
		return
	}

	env, err := d.envDaApp(ctx, a.ID)
	if err != nil {
		falhar("montar variáveis de ambiente: %v", err)
		return
	}
	if snap, err := json.Marshal(env); err == nil {
		_, _ = d.db.Exec(`UPDATE deploys SET env_snapshot = ? WHERE id = ?`, string(snap), deployID)
	}

	if err := d.subirEsperarTrocar(ctx, a, deployID, tag, env, log); err != nil {
		falhar("%v", err)
		return
	}

	fmt.Fprintf(log, "\n✓ deploy concluído — %s no ar\n", tag)
	d.finalizar(deployID, "success", tag, sha)
}

// iniciarRollback reaproveita uma imagem já buildada — não clona, não builda.
// É o motivo de existir: trocar de volta para uma versão que já rodou é
// questão de segundos, não de esperar o build inteiro de novo.
func (d *deploys) iniciarRollback(ctx context.Context, appID, deployAlvoID int64) (int64, error) {
	var a App
	if err := d.db.QueryRowContext(ctx,
		`SELECT id, nome, repo_url, branch, porta FROM apps WHERE id = ?`, appID,
	).Scan(&a.ID, &a.Nome, &a.RepoURL, &a.Branch, &a.Porta); err != nil {
		return 0, fmt.Errorf("app %d não existe", appID)
	}

	var alvo Deploy
	err := d.db.QueryRowContext(ctx,
		`SELECT id, imagem_tag, status, COALESCE(commit_sha,''), COALESCE(env_snapshot,'{}')
		 FROM deploys WHERE id = ? AND app_id = ?`, deployAlvoID, appID,
	).Scan(&alvo.ID, &alvo.ImagemTag, &alvo.Status, &alvo.CommitSHA, &alvo.EnvSnapshot)
	if err != nil {
		return 0, fmt.Errorf("deploy %d não existe para essa app", deployAlvoID)
	}
	if alvo.Status != "success" {
		return 0, fmt.Errorf("só dá para voltar a um deploy que funcionou (esse ficou %q)", alvo.Status)
	}

	if _, err := d.dk.ImageInspect(ctx, alvo.ImagemTag); err != nil {
		return 0, fmt.Errorf("imagem %s não existe mais localmente — rebuild necessário, não é possível fazer rollback", alvo.ImagemTag)
	}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO deploys (app_id, imagem_tag, status, commit_sha, env_snapshot) VALUES (?, ?, 'building', ?, ?)`,
		appID, alvo.ImagemTag, alvo.CommitSHA, alvo.EnvSnapshot)
	if err != nil {
		return 0, err
	}
	deployID, _ := res.LastInsertId()

	if err := os.MkdirAll(d.logDir, 0o755); err != nil {
		return deployID, err
	}
	_, _ = d.db.Exec(`UPDATE deploys SET log_path = ? WHERE id = ?`, d.caminhoLog(deployID), deployID)

	go d.executarRollback(context.Background(), a, deployID, alvo)
	return deployID, nil
}

func (d *deploys) executarRollback(ctx context.Context, a App, deployID int64, alvo Deploy) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	log, err := os.Create(d.caminhoLog(deployID))
	if err != nil {
		d.finalizar(deployID, "failed", alvo.ImagemTag, alvo.CommitSHA)
		return
	}
	defer log.Close()

	fmt.Fprintf(log, "=== rollback para o deploy #%d (%s, commit %s)\n", alvo.ID, alvo.ImagemTag, alvo.CommitSHA)
	fmt.Fprintf(log, "reaproveitando a imagem já buildada — sem clone, sem build\n")

	var env map[string]string
	if err := json.Unmarshal([]byte(alvo.EnvSnapshot), &env); err != nil {
		fmt.Fprintf(log, "\n✗ env_snapshot corrompido: %v\n", err)
		d.finalizar(deployID, "failed", alvo.ImagemTag, alvo.CommitSHA)
		return
	}

	if err := d.subirEsperarTrocar(ctx, a, deployID, alvo.ImagemTag, env, log); err != nil {
		fmt.Fprintf(log, "\n✗ %v\n", err)
		d.finalizar(deployID, "failed", alvo.ImagemTag, alvo.CommitSHA)
		return
	}

	fmt.Fprintf(log, "\n✓ rollback concluído — %s no ar\n", alvo.ImagemTag)
	d.finalizar(deployID, "success", alvo.ImagemTag, alvo.CommitSHA)
}

// subirEsperarTrocar é o núcleo comum a deploy e rollback: a partir de uma
// imagem já pronta (buildada agora ou reaproveitada), sobe o container novo,
// só troca o ponteiro quando ele prova que responde, e limpa o anterior.
// Nenhuma etapa aqui builda nada — por isso os dois chamadores reaproveitam.
func (d *deploys) subirEsperarTrocar(ctx context.Context, a App, deployID int64, tag string, env map[string]string, log io.Writer) error {
	fmt.Fprintf(log, "\n=== subindo container novo ao lado do atual\n")
	novo := containerDoDeploy(a.Nome, deployID)
	if err := d.subir(ctx, a, novo, tag, env); err != nil {
		return fmt.Errorf("subir container: %w", err)
	}

	fmt.Fprintf(log, "aguardando %s aceitar conexão na porta %d…\n", novo, a.Porta)
	if err := d.esperarPronto(ctx, novo, a.Porta, log); err != nil {
		// Container novo não serve: derruba ele, o antigo continua no ar.
		fmt.Fprintf(log, "removendo container novo; o anterior segue servindo\n")
		if id, _, e := d.dk.acharContainer(ctx, novo); e == nil && id != "" {
			_, _ = d.dk.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		}
		return err
	}

	anterior := d.containerAtual(a.ID)
	if _, err := d.db.Exec(`UPDATE apps SET container_atual = ? WHERE id = ?`, novo, a.ID); err != nil {
		return fmt.Errorf("gravar container atual: %w", err)
	}

	fmt.Fprintf(log, "\n=== apontando o nginx para o container novo\n")
	if err := d.ng.sincronizar(ctx, d.porta); err != nil {
		// Volta o ponteiro: o nginx continua no container antigo.
		_, _ = d.db.Exec(`UPDATE apps SET container_atual = ? WHERE id = ?`, anterior, a.ID)
		return fmt.Errorf("nginx recusou a config: %w", err)
	}

	if anterior != "" && anterior != novo {
		fmt.Fprintf(log, "\n=== removendo container anterior (%s)\n", anterior)
		if id, _, e := d.dk.acharContainer(ctx, anterior); e == nil && id != "" {
			_, _ = d.dk.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		}
	}
	return nil
}

// listarDeploys é o histórico de uma app, mais recente primeiro, com o deploy
// hoje no ar marcado — lido de volta do nome do container, não de uma coluna
// própria, então não existe uma segunda fonte de verdade para divergir.
func (d *deploys) listarDeploys(ctx context.Context, appID int64) ([]Deploy, error) {
	atualID, _ := containerAtualDeployID(d.containerAtual(appID))

	linhas, err := d.db.QueryContext(ctx,
		`SELECT id, app_id, imagem_tag, status, COALESCE(commit_sha,''), created_at
		 FROM deploys WHERE app_id = ? ORDER BY id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []Deploy
	for linhas.Next() {
		var dp Deploy
		if err := linhas.Scan(&dp.ID, &dp.AppID, &dp.ImagemTag, &dp.Status, &dp.CommitSHA, &dp.CriadoEm); err != nil {
			return nil, err
		}
		dp.Atual = dp.ID == atualID
		out = append(out, dp)
	}
	return out, linhas.Err()
}

func (d *deploys) containerAtual(appID int64) string {
	var atual string
	_ = d.db.QueryRow(`SELECT COALESCE(container_atual, '') FROM apps WHERE id = ?`, appID).Scan(&atual)
	return atual
}

func (d *deploys) finalizar(deployID int64, status, tag, sha string) {
	_, _ = d.db.Exec(
		`UPDATE deploys SET status = ?, imagem_tag = ?, commit_sha = ? WHERE id = ?`,
		status, tag, sha, deployID)
}

func (d *deploys) subir(ctx context.Context, a App, nome, tag string, env map[string]string) error {
	if err := d.dk.garantirRede(ctx, redeZeno); err != nil {
		return err
	}

	criado, err := d.dk.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: nome,
		Config: &container.Config{
			Image: tag,
			Env:   envMapToList(env),
			Labels: map[string]string{
				labelManaged: "true",
				labelTipo:    "app",
				labelNome:    a.Nome,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(redeZeno),
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
	})
	if err != nil {
		return err
	}
	_, err = d.dk.ContainerStart(ctx, criado.ID, client.ContainerStartOptions{})
	return err
}

func envMapToList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// envDaApp junta as variáveis da app com as injetadas pelos serviços
// vinculados — é isso que faz DATABASE_URL aparecer sozinha na app. Devolve
// mapa, não a lista "K=V": o rollback precisa serializar isso como
// env_snapshot, e reconstituir um mapa a partir da lista seria retrabalho.
func (d *deploys) envDaApp(ctx context.Context, appID int64) (map[string]string, error) {
	env := map[string]string{}

	linhas, err := d.db.QueryContext(ctx,
		`SELECT s.preset_id, s.nome, s.config_json FROM services s
		 JOIN app_services asv ON asv.service_id = s.id WHERE asv.app_id = ?`, appID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	for linhas.Next() {
		var presetID, nome, configJSON string
		if err := linhas.Scan(&presetID, &nome, &configJSON); err != nil {
			return nil, err
		}
		preset, ok := d.sv.presets[presetID]
		if !ok {
			continue
		}
		var envServico map[string]string
		_ = json.Unmarshal([]byte(configJSON), &envServico)

		injetadas, err := aplicarTemplateMapa(preset.Injeta, contextoTemplate{
			Nome: nome, Host: containerDoServico(nome), Env: envServico,
		})
		if err != nil {
			return nil, err
		}
		for k, v := range injetadas {
			env[k] = v
		}
	}
	if err := linhas.Err(); err != nil {
		return nil, err
	}

	// Variáveis da própria app ganham das injetadas: se alguém sobrescreveu
	// DATABASE_URL de propósito, é isso que vale.
	proprias, err := d.db.QueryContext(ctx, `SELECT chave, valor FROM app_env WHERE app_id = ?`, appID)
	if err != nil {
		return nil, err
	}
	defer proprias.Close()
	for proprias.Next() {
		var k, v string
		if err := proprias.Scan(&k, &v); err != nil {
			return nil, err
		}
		env[k] = v
	}
	return env, proprias.Err()
}

// esperarPronto só considera o deploy bom quando a porta aceita conexão.
// "Container running" não basta: processo que morre no boot fica "running"
// por um instante e o nginx apontaria para um app que nunca respondeu.
func (d *deploys) esperarPronto(ctx context.Context, nome string, porta int, log io.Writer) error {
	limite := time.Now().Add(90 * time.Second)

	for time.Now().Before(limite) {
		id, rodando, err := d.dk.acharContainer(ctx, nome)
		if err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("container %s sumiu", nome)
		}
		if !rodando {
			saida, _, _ := d.dk.exec(ctx, id, []string{"true"})
			return fmt.Errorf("container parou durante o boot: %s", saida)
		}

		// Testa de dentro da rede zeno — o container não publica porta no host.
		if _, code, err := d.dk.exec(ctx, id, []string{
			"sh", "-c", fmt.Sprintf("nc -z 127.0.0.1 %d || wget -q -O /dev/null http://127.0.0.1:%d/", porta, porta),
		}); err == nil && code == 0 {
			fmt.Fprintf(log, "porta %d respondeu\n", porta)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("porta %d não respondeu em 90s", porta)
}

// vhostsDasApps monta os server blocks gerados: um por domínio cadastrado,
// apontando para o container do deploy atual.
func (d *deploys) vhostsDasApps() ([]nginxServer, error) {
	linhas, err := d.db.Query(
		`SELECT dm.hostname, a.porta, COALESCE(a.container_atual, '')
		 FROM domains dm JOIN apps a ON a.id = dm.app_id
		 WHERE COALESCE(a.container_atual, '') <> '' ORDER BY dm.id`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var servers []nginxServer
	for linhas.Next() {
		var host, containerAtual string
		var porta int
		if err := linhas.Scan(&host, &porta, &containerAtual); err != nil {
			return nil, err
		}
		servers = append(servers, nginxServer{
			ServerName: host,
			Locations: []nginxLocation{{
				Path: "/",
				Tipo: "proxy",
				Alvo: fmt.Sprintf("http://%s:%d", containerAtual, porta),
				// Obrigatório: o nome do container muda a cada deploy.
				Dinamico: true,
				Stream:   true,
			}},
		})
	}
	return servers, linhas.Err()
}
