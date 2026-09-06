package main

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectarEstrategiaPreferemDockerfile(t *testing.T) {
	dir := t.TempDir()
	if got := detectarEstrategia(dir); got != estrategiaNixpacks {
		t.Errorf("sem Dockerfile: esperava nixpacks, veio %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectarEstrategia(dir); got != estrategiaDockerfile {
		t.Errorf("com Dockerfile: esperava dockerfile, veio %q", got)
	}
}

// O .git de um repo real facilmente passa de dezenas de MB e não faz sentido
// dentro da imagem — se ele vazasse para o tar, o build ficaria lento à toa.
func TestTarDiretorioPulaGit(t *testing.T) {
	dir := t.TempDir()
	escrever := func(rel, conteudo string) {
		caminho := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(caminho, []byte(conteudo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	escrever("Dockerfile", "FROM scratch")
	escrever("server.js", "console.log(1)")
	escrever(".git/objects/lixo", "não deveria aparecer no tar")

	r, err := tarDiretorio(dir)
	if err != nil {
		t.Fatalf("tarDiretorio: %v", err)
	}

	tr := tar.NewReader(r)
	achados := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ler tar: %v", err)
		}
		achados[hdr.Name] = true
	}

	if !achados["Dockerfile"] || !achados["server.js"] {
		t.Errorf("arquivos do projeto ausentes no tar: %v", achados)
	}
	for nome := range achados {
		if nome == ".git" || len(nome) >= 5 && nome[:5] == ".git/" {
			t.Errorf(".git vazou para o contexto de build: %q", nome)
		}
	}
}
