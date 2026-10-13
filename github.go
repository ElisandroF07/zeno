package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// github faz o papel que a Vercel faz ao "Conectar com GitHub": um clique
// autoriza, e a partir daí listar repositórios, branches e clonar (inclusive
// privado) funciona sem o usuário nunca digitar URL nem token na tela.
type github struct {
	db           *sql.DB
	clientID     string
	clientSecret string
	callbackURL  string
	httpClient   *http.Client
	// apiBaseURL existe para os testes apontarem a um servidor fake em vez
	// de api.github.com de verdade — nunca sobreposto fora deles.
	apiBaseURL string
}

func novoGithub(db *sql.DB, clientID, clientSecret, callbackURL string) *github {
	return &github{
		db: db, clientID: clientID, clientSecret: clientSecret, callbackURL: callbackURL,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		apiBaseURL: "https://api.github.com",
	}
}

func (g *github) configurado() bool {
	return g.clientID != "" && g.clientSecret != ""
}

// --- OAuth -------------------------------------------------------------

const cookieStateGithub = "zeno_github_state"

// handlerLogin redireciona pra tela de autorização do GitHub. O state vai
// num cookie HttpOnly de vida curta — não precisa de tabela nem de sessão
// de usuário (o Zeno não tem nenhuma): é só a defesa contra CSRF que o
// próprio GitHub recomenda, e cookie de 10 minutos cobre isso sozinho.
func (g *github) handlerLogin(w http.ResponseWriter, r *http.Request) {
	if !g.configurado() {
		erroHTTP(w, http.StatusPreconditionFailed,
			"integração com GitHub não configurada — faltam ZENO_GITHUB_CLIENT_ID e ZENO_GITHUB_CLIENT_SECRET")
		return
	}
	state := gerarState()
	http.SetCookie(w, &http.Cookie{
		Name: cookieStateGithub, Value: state, Path: "/",
		HttpOnly: true, MaxAge: 600, SameSite: http.SameSiteLaxMode,
	})

	q := url.Values{
		"client_id":    {g.clientID},
		"redirect_uri": {g.callbackURL},
		"scope":        {"repo"},
		"state":        {state},
	}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func gerarState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("sem entropia para gerar state OAuth: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (g *github) handlerCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieStateGithub)
	if err != nil || cookie.Value == "" || cookie.Value != r.URL.Query().Get("state") {
		erroHTTP(w, http.StatusBadRequest, "state inválido ou expirado — tente conectar de novo")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieStateGithub, Value: "", Path: "/", MaxAge: -1})

	if erroGithub := r.URL.Query().Get("error"); erroGithub != "" {
		erroHTTP(w, http.StatusBadRequest, "autorização recusada: "+erroGithub)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		erroHTTP(w, http.StatusBadRequest, "código de autorização ausente")
		return
	}

	token, err := g.trocarCodigoPorToken(r.Context(), code)
	if err != nil {
		erroHTTP(w, http.StatusBadGateway, "trocar código por token: "+err.Error())
		return
	}
	usuario, err := g.usuarioAutenticado(r.Context(), token)
	if err != nil {
		erroHTTP(w, http.StatusBadGateway, "consultar usuário autenticado: "+err.Error())
		return
	}

	if err := gravarSetting(g.db, "github_token", token); err != nil {
		erroHTTP(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = gravarSetting(g.db, "github_usuario", usuario)

	// Volta pra UI — é lá que a tela de criar app já aparece com o
	// repositório escolhido a partir da lista, sem digitar nada.
	http.Redirect(w, r, "/", http.StatusFound)
}

func (g *github) trocarCodigoPorToken(ctx context.Context, code string) (string, error) {
	corpo := url.Values{
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"code":          {code},
		"redirect_uri":  {g.callbackURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token", strings.NewReader(corpo.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // sem isso a resposta vem form-encoded

	res, err := g.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("%s: %s", resp.Error, resp.ErrorDesc)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("resposta do GitHub sem access_token")
	}
	return resp.AccessToken, nil
}

func (g *github) usuarioAutenticado(ctx context.Context, token string) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.chamarComToken(ctx, token, "/user", &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// --- API do GitHub -------------------------------------------------------

func (g *github) token() (string, bool) {
	tok, ok, _ := lerSetting(g.db, "github_token")
	return tok, ok && tok != ""
}

func (g *github) chamar(ctx context.Context, caminho string, out any) error {
	tok, ok := g.token()
	if !ok {
		return fmt.Errorf("GitHub não conectado")
	}
	return g.chamarComToken(ctx, tok, caminho, out)
}

func (g *github) chamarComToken(ctx context.Context, token, caminho string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiBaseURL+caminho, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	res, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		corpo, _ := io.ReadAll(res.Body)
		return fmt.Errorf("GitHub API %s: %s", res.Status, strings.TrimSpace(string(corpo)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// RepoGithub é o subconjunto do repositório que a tela precisa — a API do
// GitHub devolve dezenas de campos, só uso os que viram UI.
type RepoGithub struct {
	NomeCompleto string `json:"full_name"`
	Privado      bool   `json:"private"`
	CloneURL     string `json:"clone_url"`
	BranchPadrao string `json:"default_branch"`
	AtualizadoEm string `json:"updated_at"`
}

func (g *github) listarRepos(ctx context.Context) ([]RepoGithub, error) {
	var repos []RepoGithub
	// affiliation cobre repo próprio, onde é colaborador, e de organização —
	// é a mesma lista que o próprio GitHub mostra em "Your repositories".
	err := g.chamar(ctx,
		"/user/repos?sort=updated&per_page=100&affiliation=owner,collaborator,organization_member", &repos)
	return repos, err
}

func (g *github) listarBranches(ctx context.Context, nomeCompleto string) ([]string, error) {
	var branches []struct {
		Nome string `json:"name"`
	}
	if err := g.chamar(ctx, "/repos/"+nomeCompleto+"/branches?per_page=100", &branches); err != nil {
		return nil, err
	}
	out := make([]string, len(branches))
	for i, b := range branches {
		out[i] = b.Nome
	}
	return out, nil
}

// --- status / desconectar -------------------------------------------------

func (g *github) status() (conectado bool, usuario string) {
	_, conectado = g.token()
	usuario, _, _ = lerSetting(g.db, "github_usuario")
	return
}

func (g *github) desconectar() error {
	if err := apagarSetting(g.db, "github_token"); err != nil {
		return err
	}
	return apagarSetting(g.db, "github_usuario")
}

// --- autenticar URL de clone e mascarar no log ------------------------------

// comAutenticacao injeta o token salvo na URL — só para github.com, só se
// houver um token guardado (repo público sem conexão nenhuma continua
// clonando igual, sem exigir nada disto). É o que faz repositório privado
// funcionar sem o usuário nunca colar uma credencial na tela.
func (g *github) comAutenticacao(repoURL string) string {
	if !strings.Contains(repoURL, "github.com/") {
		return repoURL
	}
	tok, ok := g.token()
	if !ok {
		return repoURL
	}
	return strings.Replace(repoURL, "https://github.com/", "https://x-access-token:"+tok+"@github.com/", 1)
}

var reCredencialURL = regexp.MustCompile(`://[^\s/@]+@`)

func mascararCredenciais(texto string) string {
	return reCredencialURL.ReplaceAllString(texto, "://***@")
}

// writerMascarado protege o log de deploy contra vazar credencial — não só
// a linha "$ git clone ...", mas também o que o próprio git ecoa em erro
// (`fatal: unable to access 'https://TOKEN@github.com/...'`). Bufferiza por
// linha (não por byte) para não cortar uma URL ao meio entre duas escritas
// do processo filho.
type writerMascarado struct {
	dest io.Writer
	buf  bytes.Buffer
}

func (w *writerMascarado) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		linha, err := w.buf.ReadString('\n')
		if err != nil {
			// Sobra sem quebra de linha: devolve pro buffer, espera mais.
			w.buf.Reset()
			w.buf.WriteString(linha)
			break
		}
		if _, err := w.dest.Write([]byte(mascararCredenciais(linha))); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush escoa o que sobrou sem quebra de linha final — sem isso, a última
// linha de um comando que não termina com \n some do log.
func (w *writerMascarado) Flush() {
	if w.buf.Len() > 0 {
		_, _ = w.dest.Write([]byte(mascararCredenciais(w.buf.String())))
		w.buf.Reset()
	}
}
