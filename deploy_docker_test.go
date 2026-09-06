package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// Integração de verdade: precisa de Docker e do zeno-nginx no ar (ver
// nginx_docker_test.go). Cria um repo git local com Dockerfile e faz deploy
// dele de ponta a ponta.
//
//	ZENO_TESTE_DOCKER=1 go test -run TestIntegracaoDeploy ./...
func repoDeTeste(t *testing.T, versao string) string {
	t.Helper()
	dir := t.TempDir()

	dockerfile := "FROM node:22-alpine\nWORKDIR /app\nCOPY server.js .\nEXPOSE 3000\nCMD [\"node\", \"server.js\"]\n"
	servidor := `const http = require('http')
http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'text/plain'})
  res.end('` + versao + `')
}).listen(3000)
`
	for nome, conteudo := range map[string]string{"Dockerfile": dockerfile, "server.js": servidor} {
		if err := os.WriteFile(filepath.Join(dir, nome), []byte(conteudo), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-C", dir, "config", "user.email", "teste@zeno.local"},
		{"-C", dir, "config", "user.name", "Zeno Teste"},
		{"-C", dir, "add", "-A"},
		{"-C", dir, "commit", "-q", "-m", versao},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func servicosDeTeste(t *testing.T) *servicos {
	t.Helper()
	dk, err := novoDocker()
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	presets, err := carregarPresets("nao-existe")
	if err != nil {
		t.Fatal(err)
	}
	return &servicos{dk: dk, db: nil, presets: presets}
}

func TestIntegracaoDeployZeroDowntimeEBuildQuebradoNaoDerruba(t *testing.T) {
	ng := docker0(t)
	ctx := context.Background()

	id, rodando, err := ng.dk.acharContainer(ctx, nginxNome)
	if err != nil || id == "" || !rodando {
		t.Skipf("container %s não está rodando (id=%q err=%v)", nginxNome, id, err)
	}

	dp := &deploys{
		dk: ng.dk, db: ng.db, ng: ng, sv: servicosDeTeste(t),
		logDir: t.TempDir(), porta: "8080",
	}
	ng.vhostsExtra = dp.vhostsDasApps

	repo := repoDeTeste(t, "v1")
	app := App{Nome: "itg", RepoURL: repo, Branch: "main", Porta: 3000, Dominio: "itg.local"}
	appID, err := dp.criarApp(ctx, app)
	if err != nil {
		t.Fatalf("criarApp: %v", err)
	}
	t.Cleanup(func() {
		if id, _, _ := dp.dk.acharContainer(ctx, dp.containerAtual(appID)); id != "" {
			_, _ = dp.dk.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		}
	})

	deploy1 := esperarDeploy(t, dp, appID)
	if s := dp.statusDeploy(deploy1); s != "success" {
		t.Fatalf("primeiro deploy: esperava success, veio %q — ver log", s)
	}

	// v2: precisa trocar sem que o vhost fique um instante sem responder.
	novoRepo := repoDeTeste(t, "v2")
	_, _ = dp.db.Exec(`UPDATE apps SET repo_url = ? WHERE id = ?`, novoRepo, appID)

	pararSonda, resultado := sondarSemQueda(t, "itg.local")
	deploy2 := esperarDeploy(t, dp, appID)
	pararSonda()

	if s := dp.statusDeploy(deploy2); s != "success" {
		t.Fatalf("segundo deploy: esperava success, veio %q", s)
	}
	if falhas := <-resultado; falhas > 0 {
		t.Errorf("%d requisições falharam durante a troca de versão", falhas)
	}

	// Deploy quebrado: a versão que está no ar não pode sair.
	quebrado := t.TempDir()
	if err := os.WriteFile(filepath.Join(quebrado, "Dockerfile"), []byte("FROM node:22-alpine\nRUN false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "quebrado"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = quebrado
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	containerAntes := dp.containerAtual(appID)
	_, _ = dp.db.Exec(`UPDATE apps SET repo_url = ? WHERE id = ?`, quebrado, appID)

	deploy3 := esperarDeploy(t, dp, appID)
	if s := dp.statusDeploy(deploy3); s != "failed" {
		t.Fatalf("deploy com Dockerfile quebrado: esperava failed, veio %q", s)
	}
	if atual := dp.containerAtual(appID); atual != containerAntes {
		t.Errorf("container atual mudou após deploy falho: era %q, ficou %q", containerAntes, atual)
	}
	if idAtual, rodandoAtual, _ := dp.dk.acharContainer(ctx, containerAntes); idAtual == "" || !rodandoAtual {
		t.Errorf("container da versão anterior não está mais rodando após deploy falho")
	}

	// Rollback: volta para o deploy 1 (v1) sem clonar nem buildar de novo.
	historico, err := dp.listarDeploys(ctx, appID)
	if err != nil {
		t.Fatalf("listarDeploys: %v", err)
	}
	if len(historico) != 3 {
		t.Fatalf("esperava 3 deploys no histórico, veio %d", len(historico))
	}
	// Mais recente primeiro: [3 failed, 2 success, 1 success]. O atual é o 2.
	if !historico[1].Atual || historico[1].ID != deploy2 {
		t.Errorf("deploy atual mal marcado no histórico: %+v", historico[1])
	}

	rollbackID, err := dp.iniciarRollback(ctx, appID, deploy1)
	if err != nil {
		t.Fatalf("iniciarRollback: %v", err)
	}
	aguardarStatus(t, dp, rollbackID)
	if s := dp.statusDeploy(rollbackID); s != "success" {
		t.Fatalf("rollback: esperava success, veio %q", s)
	}

	corpo, err := exec.Command("curl", "-s", "-H", "Host: itg.local", "http://localhost:"+env("ZENO_TESTE_HTTP_PORT", "8081")+"/").Output()
	if err != nil {
		t.Fatalf("curl pós-rollback: %v", err)
	}
	if strings.TrimSpace(string(corpo)) != "v1" {
		t.Errorf("rollback não voltou para v1: resposta = %q", corpo)
	}
}

func esperarDeploy(t *testing.T, dp *deploys, appID int64) int64 {
	t.Helper()
	deployID, err := dp.iniciarDeploy(context.Background(), appID)
	if err != nil {
		t.Fatalf("iniciarDeploy: %v", err)
	}
	aguardarStatus(t, dp, deployID)
	return deployID
}

func aguardarStatus(t *testing.T, dp *deploys, deployID int64) {
	t.Helper()
	limite := time.Now().Add(3 * time.Minute)
	for time.Now().Before(limite) {
		if s := dp.statusDeploy(deployID); s != "building" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("deploy %d não terminou em 3 minutos", deployID)
}

// sondarSemQueda bate no vhost em loop e conta falhas até pararSonda ser
// chamado; o canal devolve a contagem final. A porta HTTP do nginx de teste
// é configurável porque a 80 costuma estar ocupada em máquina de dev.
func sondarSemQueda(t *testing.T, host string) (parar func(), falhas <-chan int) {
	t.Helper()
	portaHTTP := env("ZENO_TESTE_HTTP_PORT", "8081")
	fim := make(chan struct{})
	resultado := make(chan int, 1)

	go func() {
		n := 0
		for {
			select {
			case <-fim:
				resultado <- n
				return
			default:
			}
			cmd := exec.Command("curl", "-s", "-o", os.DevNull, "-w", "%{http_code}",
				"-H", "Host: "+host, "--max-time", "2", "http://localhost:"+portaHTTP+"/")
			out, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(out)) != "200" {
				n++
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	return func() { close(fim) }, resultado
}
