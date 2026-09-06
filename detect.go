package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Deteccao é um palpite, não uma decisão: todo campo chega editável na tela.
// Quem builda de verdade é sempre o Nixpacks (ou o Dockerfile do projeto,
// que ganha por si só) — ver detectarEstrategia em build.go. Isto aqui só
// pré-preenche o formulário de criação da app.
type Deteccao struct {
	Framework  string `json:"framework"`
	Estrategia string `json:"estrategia"` // dockerfile | nixpacks — o que build.go vai escolher de qualquer forma
	Porta      int    `json:"porta"`
	Comentario string `json:"comentario"`
}

func arquivoExiste(dir, nome string) bool {
	info, err := os.Stat(filepath.Join(dir, nome))
	return err == nil && !info.IsDir()
}

func lerArquivo(dir, nome string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, nome))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// detectarProjeto lê no máximo um punhado de arquivos conhecidos — nunca
// builda, nunca executa nada do repositório. Ordem importa: Dockerfile
// sempre ganha, porque se o time escreveu um é ele que descreve o build de
// verdade, não um palpite meu.
func detectarProjeto(dir string) Deteccao {
	if arquivoExiste(dir, "Dockerfile") {
		return Deteccao{
			Framework:  "Dockerfile",
			Estrategia: estrategiaDockerfile,
			Comentario: "Projeto já tem Dockerfile — é ele que descreve o build, o Zeno só usa.",
		}
	}
	if arquivoExiste(dir, "docker-compose.yml") || arquivoExiste(dir, "docker-compose.yaml") {
		return Deteccao{
			Framework: "Docker Compose",
			Comentario: "Projeto tem docker-compose — o Zeno hoje builda um serviço por app " +
				"(via Dockerfile de um dos serviços); orquestração multi-container do compose " +
				"não é reproduzida automaticamente.",
		}
	}

	if conteudo, ok := lerArquivo(dir, "package.json"); ok {
		return detectarNode(conteudo)
	}
	if arquivoExiste(dir, "pom.xml") || arquivoExiste(dir, "build.gradle") || arquivoExiste(dir, "build.gradle.kts") {
		return detectarSpringBoot(dir)
	}
	if arquivoExiste(dir, "requirements.txt") || arquivoExiste(dir, "pyproject.toml") {
		return detectarPython(dir)
	}
	if arquivoExiste(dir, "go.mod") {
		return Deteccao{Framework: "Go", Estrategia: estrategiaNixpacks, Porta: 8080,
			Comentario: "Nixpacks builda com `go build`. Porta é só sugestão — ajuste para a que o código usa."}
	}
	if arquivoExiste(dir, "Gemfile") {
		return Deteccao{Framework: "Ruby on Rails", Estrategia: estrategiaNixpacks, Porta: 3000}
	}
	if arquivoExiste(dir, "composer.json") {
		return Deteccao{Framework: "PHP", Estrategia: estrategiaNixpacks, Porta: 8000}
	}
	if arquivoExiste(dir, "index.html") {
		return Deteccao{Framework: "Site estático", Estrategia: estrategiaNixpacks, Porta: 80,
			Comentario: "Só HTML/CSS/JS — Nixpacks serve como estático, sem servidor de aplicação."}
	}

	return Deteccao{
		Framework:  "desconhecido",
		Estrategia: estrategiaNixpacks,
		Comentario: "Não reconheci o projeto por nenhum arquivo esperado — o Nixpacks ainda tenta, " +
			"mas confira comando de build/start manualmente.",
	}
}

// --- Node ------------------------------------------------------------------

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
}

func detectarNode(conteudo string) Deteccao {
	var pkg packageJSON
	if err := json.Unmarshal([]byte(conteudo), &pkg); err != nil {
		return Deteccao{Framework: "Node.js", Estrategia: estrategiaNixpacks, Porta: 3000,
			Comentario: "package.json não pôde ser lido (" + err.Error() + "); assumindo Node genérico."}
	}

	tem := func(pacote string) bool {
		_, a := pkg.Dependencies[pacote]
		_, b := pkg.DevDependencies[pacote]
		return a || b
	}

	switch {
	case tem("next"):
		return Deteccao{Framework: "Next.js", Estrategia: estrategiaNixpacks, Porta: 3000}
	case tem("vite") || tem("react-scripts") || tem("@vue/cli-service"):
		return Deteccao{Framework: "SPA estática (Vite/CRA)", Estrategia: estrategiaNixpacks, Porta: 80,
			Comentario: "Build gera arquivos estáticos — Nixpacks serve com um servidor estático, não com `npm start`."}
	case tem("express") || tem("fastify") || tem("koa"):
		return Deteccao{Framework: "Node.js (API)", Estrategia: estrategiaNixpacks, Porta: 3000}
	default:
		porta := 3000
		comentario := ""
		if _, ok := pkg.Scripts["start"]; !ok {
			comentario = `sem script "start" no package.json — Nixpacks pode não achar como iniciar.`
		}
		return Deteccao{Framework: "Node.js", Estrategia: estrategiaNixpacks, Porta: porta, Comentario: comentario}
	}
}

// --- Spring Boot -------------------------------------------------------------

var reServerPort = regexp.MustCompile(`(?m)^\s*server\.port\s*=\s*(\d+)`)
var reServerPortYAML = regexp.MustCompile(`(?ms)^server:\s*\n(?:\s+\S.*\n)*?\s+port:\s*(\d+)`)

func detectarSpringBoot(dir string) Deteccao {
	d := Deteccao{Framework: "Spring Boot", Estrategia: estrategiaNixpacks, Porta: 8080}

	for _, caminho := range []string{
		"src/main/resources/application.properties",
		"src/main/resources/application.yml",
		"src/main/resources/application.yaml",
	} {
		conteudo, ok := lerArquivo(dir, caminho)
		if !ok {
			continue
		}
		if m := reServerPort.FindStringSubmatch(conteudo); m != nil {
			d.Porta, _ = strconv.Atoi(m[1])
			return d
		}
		if m := reServerPortYAML.FindStringSubmatch(conteudo); m != nil {
			d.Porta, _ = strconv.Atoi(m[1])
			return d
		}
	}
	return d
}

// --- Python ------------------------------------------------------------------

func detectarPython(dir string) Deteccao {
	conteudo, _ := lerArquivo(dir, "requirements.txt")
	if pyproject, ok := lerArquivo(dir, "pyproject.toml"); ok {
		conteudo += "\n" + pyproject
	}
	baixo := strings.ToLower(conteudo)

	switch {
	case strings.Contains(baixo, "django"):
		return Deteccao{Framework: "Django", Estrategia: estrategiaNixpacks, Porta: 8000,
			Comentario: "Nixpacks assume `manage.py runserver`; produção normalmente troca para gunicorn."}
	case strings.Contains(baixo, "fastapi"):
		return Deteccao{Framework: "FastAPI", Estrategia: estrategiaNixpacks, Porta: 8000}
	case strings.Contains(baixo, "flask"):
		return Deteccao{Framework: "Flask", Estrategia: estrategiaNixpacks, Porta: 5000}
	default:
		return Deteccao{Framework: "Python", Estrategia: estrategiaNixpacks, Porta: 8000}
	}
}

// detectarRepo clona raso num diretório temporário só para inspecionar —
// nunca builda, e o clone morre com a função (defer RemoveAll). Reaproveita
// o mesmo `clonar` do motor de deploy: é o mesmo git, a mesma autenticação.
func detectarRepo(ctx context.Context, repoURL, branch string) (Deteccao, error) {
	dir, err := os.MkdirTemp("", "zeno-detectar-")
	if err != nil {
		return Deteccao{}, err
	}
	defer os.RemoveAll(dir)

	// A saída do git precisa sobreviver ao erro: "exit status 128" sozinho
	// não diz nada — quem vê isso não sabe se é branch errada, repo privado
	// sem credencial, ou nome digitado errado. O texto que realmente importa
	// (`fatal: Remote branch ... not found`, `fatal: could not read
	// Username`, `repository not found`) só existe no stderr do próprio git.
	var saida bytes.Buffer
	if _, err := clonar(ctx, repoURL, branch, dir, &saida); err != nil {
		return Deteccao{}, fmt.Errorf("git clone falhou: %s", strings.TrimSpace(saida.String()))
	}
	return detectarProjeto(dir), nil
}
