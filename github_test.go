package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func githubDeTeste(t *testing.T) *github {
	t.Helper()
	db, err := abrirDB(t.TempDir() + string(os.PathSeparator) + "teste.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return novoGithub(db, "id-fake", "secret-fake", "http://localhost:8080/api/github/callback")
}

func TestMascararCredenciais(t *testing.T) {
	casos := map[string]string{
		"https://x-access-token:ghp_ABC123@github.com/dono/repo.git": "https://***@github.com/dono/repo.git",
		"fatal: unable to access 'https://token123@github.com/x/y/'": "fatal: unable to access 'https://***@github.com/x/y/'",
		"https://github.com/dono/repo.git (sem credencial)":           "https://github.com/dono/repo.git (sem credencial)",
	}
	for entrada, quer := range casos {
		if got := mascararCredenciais(entrada); got != quer {
			t.Errorf("mascararCredenciais(%q) = %q, quer %q", entrada, got, quer)
		}
	}
}

// O writer é o que protege o log de deploy (gravado em disco, mostrado na
// tela via SSE) de vazar o token — precisa mascarar mesmo quando a escrita
// chega em pedaços pequenos, contanto que a quebra de linha esteja íntegra.
func TestWriterMascaradoProtegeCredencialNoLog(t *testing.T) {
	var dest bytes.Buffer
	w := &writerMascarado{dest: &dest}

	_, _ = w.Write([]byte("$ git clone https://x-access-token:SEGREDO123@github.com/a/b.git dest\n"))
	_, _ = w.Write([]byte("Cloning into 'dest'...\n"))
	w.Flush()

	saida := dest.String()
	if strings.Contains(saida, "SEGREDO123") {
		t.Errorf("token vazou no log: %q", saida)
	}
	if !strings.Contains(saida, "https://***@github.com/a/b.git") {
		t.Errorf("URL mascarada ausente, veio: %q", saida)
	}
	if !strings.Contains(saida, "Cloning into") {
		t.Errorf("linha sem credencial não deveria ser afetada: %q", saida)
	}
}

// Escrita partida no meio de uma linha (comum com pipe de processo) não pode
// deixar a credencial escapar pela fresta entre dois Write().
func TestWriterMascaradoSobreviveAEscritaPartida(t *testing.T) {
	var dest bytes.Buffer
	w := &writerMascarado{dest: &dest}

	linha := "fatal: https://x-access-token:SEGREDO456@github.com/a/b.git\n"
	meio := len(linha) / 2
	_, _ = w.Write([]byte(linha[:meio]))
	_, _ = w.Write([]byte(linha[meio:]))
	w.Flush()

	if strings.Contains(dest.String(), "SEGREDO456") {
		t.Errorf("token vazou mesmo com escrita partida: %q", dest.String())
	}
}

func TestComAutenticacaoSoMexeEmGithubComToken(t *testing.T) {
	g := githubDeTeste(t)

	// Sem token salvo: URL sai igual, github.com ou não.
	url := "https://github.com/dono/repo.git"
	if got := g.comAutenticacao(url); got != url {
		t.Errorf("sem token deveria devolver a URL original, veio %q", got)
	}
	outro := "https://gitlab.com/dono/repo.git"
	if got := g.comAutenticacao(outro); got != outro {
		t.Errorf("host que não é github.com nunca deveria mudar, veio %q", got)
	}

	// Com token salvo: só github.com ganha credencial embutida.
	if err := gravarSetting(g.db, "github_token", "TOKEN789"); err != nil {
		t.Fatal(err)
	}
	got := g.comAutenticacao(url)
	quer := "https://x-access-token:TOKEN789@github.com/dono/repo.git"
	if got != quer {
		t.Errorf("comAutenticacao = %q, quer %q", got, quer)
	}
	if got := g.comAutenticacao(outro); got != outro {
		t.Errorf("gitlab.com não deveria ganhar o token do GitHub, veio %q", got)
	}
}

// listarRepos e listarBranches contra um servidor HTTP fake — sem rede real,
// sem OAuth de verdade, exercitando o código real (mesmo chamar/chamarComToken
// que a integração de verdade usa, só apontando apiBaseURL pro servidor de
// teste) para confirmar que o parsing da resposta da API do GitHub está certo.
func TestListarReposEBranchesContraServidorFake(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer TOKEN-DE-TESTE" {
			t.Errorf("Authorization header errado: %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"full_name":"dono/app-web","private":false,"clone_url":"https://github.com/dono/app-web.git","default_branch":"main"},
			{"full_name":"dono/app-privado","private":true,"clone_url":"https://github.com/dono/app-privado.git","default_branch":"master"}
		]`))
	})
	mux.HandleFunc("/repos/dono/app-web/branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"main"},{"name":"dev"},{"name":"staging"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := githubDeTeste(t)
	g.apiBaseURL = srv.URL
	if err := gravarSetting(g.db, "github_token", "TOKEN-DE-TESTE"); err != nil {
		t.Fatal(err)
	}

	repos, err := g.listarRepos(context.Background())
	if err != nil {
		t.Fatalf("listarRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("esperava 2 repos, veio %d", len(repos))
	}
	if repos[0].NomeCompleto != "dono/app-web" || repos[0].Privado {
		t.Errorf("repo[0] errado: %+v", repos[0])
	}
	if repos[1].NomeCompleto != "dono/app-privado" || !repos[1].Privado || repos[1].BranchPadrao != "master" {
		t.Errorf("repo[1] errado: %+v", repos[1])
	}

	branches, err := g.listarBranches(context.Background(), "dono/app-web")
	if err != nil {
		t.Fatalf("listarBranches: %v", err)
	}
	quer := []string{"main", "dev", "staging"}
	if len(branches) != len(quer) {
		t.Fatalf("esperava %v, veio %v", quer, branches)
	}
	for i, b := range quer {
		if branches[i] != b {
			t.Errorf("branches[%d] = %q, quer %q", i, branches[i], b)
		}
	}
}

// Sem token salvo, qualquer chamada à API precisa recusar na hora — nunca
// tentar bater em api.github.com sem credencial e devolver um 401 confuso.
func TestChamarSemTokenRecusaNaHora(t *testing.T) {
	g := githubDeTeste(t)
	_, err := g.listarRepos(context.Background())
	if err == nil {
		t.Fatal("esperava erro sem token salvo")
	}
}
