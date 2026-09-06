package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Integração de verdade: precisa de Docker e do container zeno-nginx no ar.
//
//	ZENO_TESTE_DOCKER=1 go test -run Integracao ./...
func docker0(t *testing.T) *nginx {
	t.Helper()
	if os.Getenv("ZENO_TESTE_DOCKER") == "" {
		t.Skip("defina ZENO_TESTE_DOCKER=1 para rodar contra o Docker local")
	}
	dk, err := novoDocker()
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	db, err := abrirDB(t.TempDir() + string(os.PathSeparator) + "teste.db")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close(); dk.Close() })
	return &nginx{dk: dk, db: db}
}

// A promessa do plano: config inválida nunca chega a valer. O `nginx -t`
// precisa recusar, o arquivo anterior voltar, e o que estava servindo
// continuar servindo.
func TestIntegracaoConfigInvalidaNaoDerruba(t *testing.T) {
	ng := docker0(t)
	ctx := context.Background()

	id, rodando, err := ng.dk.acharContainer(ctx, nginxNome)
	if err != nil || id == "" || !rodando {
		t.Skipf("container %s não está rodando (id=%q err=%v)", nginxNome, id, err)
	}

	valida := renderConf([]nginxServer{vhostZeno("8080")}, "8080")
	if err := ng.aplicar(ctx, arquivoApps, valida); err != nil {
		t.Fatalf("config válida deveria aplicar: %v", err)
	}

	err = ng.aplicar(ctx, arquivoApps, "server { isso nao é nginx }")
	if err == nil {
		t.Fatal("config inválida foi aceita")
	}
	if !strings.Contains(err.Error(), "nginx -t") {
		t.Errorf("erro deveria vir do nginx -t, veio: %v", err)
	}

	// O arquivo no disco tem que ser o anterior, não o lixo.
	atual, _, err := ng.dk.exec(ctx, id, []string{"cat", nginxConfDir + "/" + arquivoApps})
	if err != nil {
		t.Fatalf("ler conf: %v", err)
	}
	if strings.Contains(atual, "isso nao é nginx") {
		t.Errorf("config inválida ficou no disco:\n%s", atual)
	}
	if !strings.Contains(atual, "proxy_pass http://host.docker.internal:8080;") {
		t.Errorf("config anterior não foi restaurada:\n%s", atual)
	}

	// E o nginx segue de pé com a config boa.
	if saida, code, err := ng.dk.exec(ctx, id, []string{"nginx", "-t"}); err != nil || code != 0 {
		t.Errorf("nginx ficou com config quebrada: code=%d err=%v\n%s", code, err, saida)
	}

	// A falha precisa ter virado registro, senão o histórico mente. Checado
	// antes de reaplicar: a reaplicação vira a última linha.
	var ok bool
	var erro string
	if err := ng.db.QueryRow(
		`SELECT ok, COALESCE(erro,'') FROM nginx_versions ORDER BY id DESC LIMIT 1`,
	).Scan(&ok, &erro); err != nil {
		t.Fatalf("ler versão: %v", err)
	}
	if ok || erro == "" {
		t.Errorf("versão com falha não foi gravada: ok=%v erro=%q", ok, erro)
	}

	// Regressão do banco envenenado: se a linha inválida ficasse gravada, a
	// config seria re-renderizada a partir dela e TODA aplicação seguinte
	// falharia no mesmo erro — sem saída pela tela. Aplicar de novo, logo
	// após a recusa, tem que funcionar.
	if err := ng.aplicar(ctx, arquivoApps, valida); err != nil {
		t.Errorf("aplicação seguinte deveria funcionar após uma recusa: %v", err)
	}
}
