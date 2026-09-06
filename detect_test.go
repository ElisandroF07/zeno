package main

import (
	"os"
	"path/filepath"
	"testing"
)

func projetoDeTeste(t *testing.T, arquivos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for nome, conteudo := range arquivos {
		caminho := filepath.Join(dir, nome)
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(caminho, []byte(conteudo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Dockerfile ganha de qualquer palpite — é o comportamento documentado e o
// mesmo usado por detectarEstrategia em build.go; os dois não podem divergir.
func TestDetectarPreferemDockerfileSobreQualquerPalpite(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"Dockerfile":   "FROM node:22-alpine",
		"package.json": `{"dependencies":{"next":"14.0.0"}}`,
	})
	d := detectarProjeto(dir)
	if d.Framework != "Dockerfile" || d.Estrategia != estrategiaDockerfile {
		t.Errorf("esperava Dockerfile ganhar, veio %+v", d)
	}
}

func TestDetectarNextJS(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"package.json": `{"dependencies":{"next":"14.2.0","react":"18.0.0"}}`,
	})
	d := detectarProjeto(dir)
	if d.Framework != "Next.js" || d.Porta != 3000 {
		t.Errorf("esperava Next.js:3000, veio %+v", d)
	}
}

func TestDetectarSPAEstaticaPorVite(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"package.json": `{"devDependencies":{"vite":"5.0.0"}}`,
	})
	d := detectarProjeto(dir)
	if d.Framework != "SPA estática (Vite/CRA)" {
		t.Errorf("esperava SPA estática, veio %+v", d)
	}
}

func TestDetectarNodeGenericoAvisaSemScriptStart(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"package.json": `{"dependencies":{"lodash":"4.0.0"}}`,
	})
	d := detectarProjeto(dir)
	if d.Framework != "Node.js" {
		t.Errorf("esperava Node.js genérico, veio %+v", d)
	}
	if d.Comentario == "" {
		t.Error("deveria avisar que falta script start")
	}
}

func TestDetectarPackageJSONInvalidoNaoQuebra(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{"package.json": `{ isso não é JSON`})
	d := detectarProjeto(dir) // não pode entrar em pânico
	if d.Framework != "Node.js" {
		t.Errorf("esperava fallback Node.js, veio %+v", d)
	}
}

func TestDetectarSpringBootLeitaPortaDeProperties(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"pom.xml": "<project></project>",
		"src/main/resources/application.properties": "spring.application.name=loja\nserver.port=9090\n",
	})
	d := detectarProjeto(dir)
	if d.Framework != "Spring Boot" || d.Porta != 9090 {
		t.Errorf("esperava Spring Boot:9090, veio %+v", d)
	}
}

func TestDetectarSpringBootLeitaPortaDeYAML(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{
		"build.gradle": "plugins { id 'org.springframework.boot' }",
		"src/main/resources/application.yml": "server:\n  port: 9091\napp:\n  nome: loja\n",
	})
	d := detectarProjeto(dir)
	if d.Framework != "Spring Boot" || d.Porta != 9091 {
		t.Errorf("esperava Spring Boot:9091, veio %+v", d)
	}
}

func TestDetectarSpringBootSemConfigUsaPortaPadrao(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{"pom.xml": "<project></project>"})
	d := detectarProjeto(dir)
	if d.Framework != "Spring Boot" || d.Porta != 8080 {
		t.Errorf("esperava Spring Boot:8080 (padrão), veio %+v", d)
	}
}

func TestDetectarPythonPorFramework(t *testing.T) {
	casos := []struct {
		requirements string
		framework    string
		porta        int
	}{
		{"Django==5.0\npsycopg2\n", "Django", 8000},
		{"fastapi\nuvicorn\n", "FastAPI", 8000},
		{"Flask==3.0\n", "Flask", 5000},
		{"requests\npandas\n", "Python", 8000},
	}
	for _, c := range casos {
		dir := projetoDeTeste(t, map[string]string{"requirements.txt": c.requirements})
		d := detectarProjeto(dir)
		if d.Framework != c.framework || d.Porta != c.porta {
			t.Errorf("requirements=%q: esperava %s:%d, veio %+v", c.requirements, c.framework, c.porta, d)
		}
	}
}

func TestDetectarGoRailsPHPEstatico(t *testing.T) {
	casos := []struct {
		arquivo   string
		framework string
	}{
		{"go.mod", "Go"},
		{"Gemfile", "Ruby on Rails"},
		{"composer.json", "PHP"},
		{"index.html", "Site estático"},
	}
	for _, c := range casos {
		dir := projetoDeTeste(t, map[string]string{c.arquivo: "conteúdo qualquer"})
		d := detectarProjeto(dir)
		if d.Framework != c.framework {
			t.Errorf("%s: esperava %s, veio %+v", c.arquivo, c.framework, d)
		}
	}
}

func TestDetectarDesconhecidoNaoQuebra(t *testing.T) {
	dir := projetoDeTeste(t, map[string]string{"README.md": "# projeto misterioso"})
	d := detectarProjeto(dir)
	if d.Framework != "desconhecido" {
		t.Errorf("esperava desconhecido, veio %+v", d)
	}
}
