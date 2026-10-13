package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

	// BranchUsada é sempre a branch de verdade que foi clonada — nunca um
	// palpite. Se o campo do formulário veio vazio, é a branch padrão do
	// remoto (a mesma que "git clone <url>" sem --branch escolheria).
	BranchUsada         string   `json:"branch_usada"`
	BranchesDisponiveis []string `json:"branches_disponiveis"`
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

// refsRemoto pergunta ao remoto quais branches existem e qual é a padrão —
// sem clonar nada, só consultando refs (o mesmo "git ls-remote" que a Vercel
// e qualquer plataforma de deploy usa pra isso). É o que evita adivinhar:
// "main" é só o palpite mais comum, não existe garantia nenhuma de que é o
// nome certo — perguntar ao remoto é a única forma de saber de verdade.
func refsRemoto(ctx context.Context, repoURL string) (padrao string, branches []string, err error) {
	// Duas chamadas, não uma: `--symref --heads <repo> HEAD` juntos devolvem
	// saída vazia — o filtro --heads também descarta o próprio HEAD symref
	// pedido, porque HEAD não vive sob refs/heads/. Ordem importa pouco;
	// symref primeiro só porque é o que decide a branch padrão.
	saidaSymref, err := gitLsRemote(ctx, "--symref", repoURL, "HEAD")
	if err != nil {
		// mascararCredenciais: repoURL pode carregar token do GitHub
		// (comAutenticacao) — o próprio git ecoa a URL usada na mensagem de
		// erro, e isso não pode vazar pra tela nem pra log nenhum.
		return "", nil, fmt.Errorf("git ls-remote falhou: %s", mascararCredenciais(saidaSymref))
	}
	for _, linha := range strings.Split(saidaSymref, "\n") {
		if resto, ok := strings.CutPrefix(linha, "ref: refs/heads/"); ok {
			padrao = strings.TrimSpace(strings.Fields(resto)[0])
			break
		}
	}
	if padrao == "" {
		return "", nil, fmt.Errorf("não encontrei a branch padrão desse repositório — existe mesmo, e você tem acesso?")
	}

	saidaHeads, err := gitLsRemote(ctx, "--heads", repoURL)
	if err != nil {
		return "", nil, fmt.Errorf("git ls-remote falhou: %s", mascararCredenciais(saidaHeads))
	}
	for _, linha := range strings.Split(saidaHeads, "\n") {
		if _, nome, ok := strings.Cut(linha, "\trefs/heads/"); ok {
			branches = append(branches, strings.TrimSpace(nome))
		}
	}
	return padrao, branches, nil
}

func gitLsRemote(ctx context.Context, args ...string) (saida string, err error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"ls-remote"}, args...)...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// detectarRepo primeiro pergunta ao remoto quais branches existem — nunca
// tenta adivinhar. Se o usuário pediu uma branch específica, ela precisa
// estar na lista (erro claro se não estiver, antes de qualquer clone); se
// veio vazio, usa a branch padrão do próprio remoto. Só então clona — raso,
// num diretório temporário que morre com a função — para inspecionar; nunca
// builda. Reaproveita o mesmo `clonar` do motor de deploy: mesmo git, mesma
// autenticação.
func detectarRepo(ctx context.Context, repoURL, branchPedida string) (Deteccao, error) {
	padrao, disponiveis, err := refsRemoto(ctx, repoURL)
	if err != nil {
		return Deteccao{}, err
	}

	branch := padrao
	if branchPedida != "" {
		achou := false
		for _, b := range disponiveis {
			if b == branchPedida {
				achou = true
				break
			}
		}
		if !achou {
			return Deteccao{}, fmt.Errorf(
				"branch %q não existe nesse repositório — disponíveis: %s",
				branchPedida, strings.Join(disponiveis, ", "))
		}
		branch = branchPedida
	}

	dir, err := os.MkdirTemp("", "zeno-detectar-")
	if err != nil {
		return Deteccao{}, err
	}
	defer os.RemoveAll(dir)

	// A saída do git precisa sobreviver a um erro daqui pra frente também —
	// mesmo com a branch já confirmada que existe, coisas como repo privado
	// sem credencial ainda falham só no clone, não no ls-remote.
	var saida bytes.Buffer
	if _, err := clonar(ctx, repoURL, branch, dir, &saida); err != nil {
		return Deteccao{}, fmt.Errorf("git clone falhou: %s", strings.TrimSpace(saida.String()))
	}

	d := detectarProjeto(dir)
	d.BranchUsada = branch
	d.BranchesDisponiveis = disponiveis
	return d, nil
}
