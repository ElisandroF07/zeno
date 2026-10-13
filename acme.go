package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// acme emite e renova certificados via HTTP-01. O desafio é servido pelo
// próprio processo do Zeno — nenhum listener novo: o vhost de cada domínio
// já faz proxy de /.well-known/acme-challenge/ para host.docker.internal,
// o mesmo endereço que já serve a UI. Um binário, uma porta, sempre.
type acme struct {
	db  *sql.DB
	dk  *docker
	ng  *nginx
	cfg acmeConfig
	// portaZeno guarda o valor fixo do processo (não muda em runtime) para
	// poder re-sincronizar o nginx depois de emitir — ver garantirCertificados.
	portaZeno string

	mu       sync.Mutex
	client   *lego.Client
	desafios sync.Map // token -> keyAuth

	// emitindo trava por hostname. Sem isso, dois sincronizar() em sequência
	// rápida — criar vhost e, logo em seguida, adicionar a primeira location
	// — disparam duas emissões concorrentes para o mesmo domínio: observado
	// de verdade rodando contra Pebble, não hipótese. Contra a CA real isso
	// queima cota do limite de "certificado duplicado" à toa.
	emitindo sync.Map // hostname -> struct{}
}

type acmeConfig struct {
	DirURL      string // endpoint ACME; staging ou Pebble em teste
	Email       string
	InsecureTLS bool // só para apontar num Pebble local com cert self-signed
}

func acmeConfigDoAmbiente() acmeConfig {
	dir := lego.LEDirectoryProduction
	if env("ZENO_ACME_STAGING", "") != "" {
		dir = lego.LEDirectoryStaging
	}
	if u := env("ZENO_ACME_DIRECTORY_URL", ""); u != "" {
		dir = u
	}
	return acmeConfig{
		DirURL:      dir,
		Email:       env("ZENO_ACME_EMAIL", ""),
		InsecureTLS: env("ZENO_ACME_INSECURE_TLS", "") != "",
	}
}

// --- conta ACME --------------------------------------------------------

type acmeUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// carregarOuCriarConta reaproveita a chave de conta gravada no banco — pedir
// conta nova a cada boot esbarraria em limite de registro da CA à toa.
// Sem context: nem lego.Client.Registration.Register aceita um — a chamada
// de rede é síncrona e não cancelável nessa versão da lib.
func (a *acme) carregarOuCriarConta() (*acmeUser, error) {
	keyPEM, temChave, err := a.lerSetting("acme_key_pem")
	if err != nil {
		return nil, err
	}

	user := &acmeUser{email: a.cfg.Email}

	if temChave {
		chave, err := certcrypto.ParsePEMPrivateKey([]byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("chave de conta ACME corrompida: %w", err)
		}
		user.key = chave

		if regJSON, temReg, err := a.lerSetting("acme_reg_json"); err != nil {
			return nil, err
		} else if temReg {
			var reg registration.Resource
			if err := json.Unmarshal([]byte(regJSON), &reg); err != nil {
				return nil, fmt.Errorf("registro ACME corrompido: %w", err)
			}
			user.reg = &reg
			return user, nil
		}
	}

	chave, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, fmt.Errorf("gerar chave de conta ACME: %w", err)
	}
	user.key = chave

	if err := a.gravarSetting("acme_key_pem", string(certcrypto.PEMEncode(chave))); err != nil {
		return nil, err
	}

	client, err := a.novoClienteSemConta(user)
	if err != nil {
		return nil, err
	}
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("registrar conta ACME: %w", err)
	}
	user.reg = reg

	regJSON, err := json.Marshal(reg)
	if err != nil {
		return nil, err
	}
	if err := a.gravarSetting("acme_reg_json", string(regJSON)); err != nil {
		return nil, err
	}
	return user, nil
}

func (a *acme) lerSetting(chave string) (valor string, existe bool, err error) {
	return lerSetting(a.db, chave)
}

func (a *acme) gravarSetting(chave, valor string) error {
	return gravarSetting(a.db, chave, valor)
}

// --- cliente ACME --------------------------------------------------------

func (a *acme) novoClienteSemConta(user *acmeUser) (*lego.Client, error) {
	cfg := lego.NewConfig(user)
	cfg.CADirURL = a.cfg.DirURL
	if a.cfg.InsecureTLS {
		// Só existe para apontar num Pebble local de teste, cujo endpoint
		// ACME usa certificado self-signed. Nunca em produção — validado
		// no boot, ver garantirConfigSegura.
		cfg.HTTPClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return lego.NewClient(cfg)
}

func (a *acme) obterCliente() (*lego.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}

	user, err := a.carregarOuCriarConta()
	if err != nil {
		return nil, err
	}
	client, err := a.novoClienteSemConta(user)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetHTTP01Provider(a); err != nil {
		return nil, err
	}
	a.client = client
	return client, nil
}

// --- challenge.Provider: Present/CleanUp guardam o token em memória; quem
// serve a resposta HTTP é o handler registrado no mux principal. ------------

func (a *acme) Present(domain, token, keyAuth string) error {
	a.desafios.Store(token, keyAuth)
	return nil
}

func (a *acme) CleanUp(domain, token, keyAuth string) error {
	a.desafios.Delete(token)
	return nil
}

func (a *acme) handlerChallenge(w http.ResponseWriter, r *http.Request) {
	v, ok := a.desafios.Load(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(v.(string)))
}

// --- emissão e renovação --------------------------------------------------

type certInfo struct {
	Hostname string `json:"hostname"`
	// PEM nunca leva `json:"..."`: nunca pode sair pela API. A chave
	// privada do certificado é a coisa mais sensível que este processo
	// guarda — vazar isso é pior que vazar a senha de um serviço.
	CertPEM            string    `json:"-"`
	KeyPEM             string    `json:"-"`
	EmitidoEm          time.Time `json:"emitido_em"`
	ExpiraEm           time.Time `json:"expira_em"`
	UltimoErro         string    `json:"ultimo_erro,omitempty"`
	TentativaEm        time.Time `json:"tentativa_em,omitempty"`
	FalhasConsecutivas int       `json:"falhas_consecutivas"`
}

const (
	certsDir = "/etc/nginx/certs"
)

func caminhosCert(hostname string) (cert, key string) {
	return certsDir + "/" + hostname + "/fullchain.pem", certsDir + "/" + hostname + "/privkey.pem"
}

// emitir pede um certificado novo. Assume que o vhost HTTP de hostname já
// está no ar servindo /.well-known/acme-challenge/ — sincronizar() garante
// isso antes de qualquer chamada aqui.
func (a *acme) emitir(ctx context.Context, hostname string) error {
	client, err := a.obterCliente()
	if err != nil {
		a.gravarErro(hostname, err)
		return err
	}

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{hostname},
		Bundle:  true,
	})
	if err != nil {
		a.gravarErro(hostname, err)
		return fmt.Errorf("emitir certificado para %s: %w", hostname, err)
	}

	x509cert, err := certcrypto.ParsePEMCertificate(res.Certificate)
	if err != nil {
		return fmt.Errorf("ler certificado emitido: %w", err)
	}

	if err := a.gravarCert(hostname, string(res.Certificate), string(res.PrivateKey), x509cert.NotAfter); err != nil {
		return err
	}
	return a.escreverNoNginx(ctx, hostname)
}

// gravarErro precisa fazer upsert, não UPDATE: na primeira tentativa para um
// hostname que nunca teve certificado, um UPDATE simples não acha linha
// nenhuma para atualizar — e o erro mais comum (porta 80 inacessível, DNS
// errado) desapareceria em silêncio, sem sobrar em lugar nenhum pra olhar.
//
// Incrementa falhas_consecutivas em vez de só gravar o texto: é essa
// contagem que faz o backoff crescer a cada tentativa malsucedida seguida —
// ver proximaTentativaPermitida.
func (a *acme) gravarErro(hostname string, err error) {
	agora := time.Now().UTC().Format(time.RFC3339)
	_, e := a.db.Exec(
		`INSERT INTO certs (hostname, cert_pem, key_pem, emitido_em, expira_em, ultimo_erro, tentativa_em, falhas_consecutivas)
		 VALUES (?, '', '', '', '', ?, ?, 1)
		 ON CONFLICT(hostname) DO UPDATE SET
		   ultimo_erro = excluded.ultimo_erro,
		   tentativa_em = excluded.tentativa_em,
		   falhas_consecutivas = falhas_consecutivas + 1`,
		hostname, err.Error(), agora)
	if e != nil {
		log.Printf("gravar erro de cert: %v", e)
	}
}

func (a *acme) gravarCert(hostname, certPEM, keyPEM string, expiraEm time.Time) error {
	agora := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.Exec(
		`INSERT INTO certs (hostname, cert_pem, key_pem, emitido_em, expira_em, ultimo_erro, tentativa_em, falhas_consecutivas)
		 VALUES (?, ?, ?, ?, ?, NULL, ?, 0)
		 ON CONFLICT(hostname) DO UPDATE SET
		   cert_pem = excluded.cert_pem, key_pem = excluded.key_pem,
		   emitido_em = excluded.emitido_em, expira_em = excluded.expira_em,
		   ultimo_erro = NULL, tentativa_em = excluded.tentativa_em, falhas_consecutivas = 0`,
		hostname, certPEM, keyPEM, agora, expiraEm.UTC().Format(time.RFC3339), agora)
	return err
}

func (a *acme) certDe(hostname string) (*certInfo, error) {
	var c certInfo
	var expiraStr string
	var tentativaStr sql.NullString
	err := a.db.QueryRow(
		`SELECT hostname, cert_pem, key_pem, expira_em, tentativa_em, falhas_consecutivas
		 FROM certs WHERE hostname = ?`, hostname,
	).Scan(&c.Hostname, &c.CertPEM, &c.KeyPEM, &expiraStr, &tentativaStr, &c.FalhasConsecutivas)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ExpiraEm, _ = time.Parse(time.RFC3339, expiraStr)
	if tentativaStr.Valid {
		c.TentativaEm, _ = time.Parse(time.RFC3339, tentativaStr.String)
	}
	return &c, nil
}

// --- backoff -----------------------------------------------------------

// Exponencial, capado em 1h: 1, 2, 4, 8, 16, 32, 60, 60, 60min... Com isso,
// mesmo que todas as tentativas falhem, o hostname fica bem abaixo do limite
// real da Let's Encrypt (5 falhas de validação por hora por hostname) já a
// partir da terceira tentativa seguida.
const (
	backoffBase = 1 * time.Minute
	backoffMax  = 60 * time.Minute
)

func proximaTentativaPermitida(falhasConsecutivas int, tentativaEm time.Time) time.Time {
	if falhasConsecutivas <= 0 || tentativaEm.IsZero() {
		return time.Time{} // sem histórico de falha, sem espera
	}
	espera := backoffBase << (falhasConsecutivas - 1) // 2^(n-1) minutos
	if espera > backoffMax || espera <= 0 {           // <=0 cobre overflow do shift
		espera = backoffMax
	}
	return tentativaEm.Add(espera)
}

func (a *acme) todosCerts() ([]certInfo, error) {
	linhas, err := a.db.Query(
		`SELECT hostname, emitido_em, expira_em, COALESCE(ultimo_erro, ''), COALESCE(tentativa_em, ''), falhas_consecutivas
		 FROM certs ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []certInfo
	for linhas.Next() {
		var c certInfo
		var emitidoStr, expiraStr, tentativaStr string
		if err := linhas.Scan(&c.Hostname, &emitidoStr, &expiraStr, &c.UltimoErro, &tentativaStr, &c.FalhasConsecutivas); err != nil {
			return nil, err
		}
		c.EmitidoEm, _ = time.Parse(time.RFC3339, emitidoStr)
		c.ExpiraEm, _ = time.Parse(time.RFC3339, expiraStr)
		if tentativaStr != "" {
			c.TentativaEm, _ = time.Parse(time.RFC3339, tentativaStr)
		}
		out = append(out, c)
	}
	return out, linhas.Err()
}

// escreverNoNginx copia o PEM do banco para dentro do container — sem
// volume, sem depender de mountpoint de host. É o mesmo mecanismo já usado
// para a config (CopyToContainer): reescrever é rehidratar, sempre a partir
// do banco, nunca de reemissão.
func (a *acme) escreverNoNginx(ctx context.Context, hostname string) error {
	c, err := a.certDe(hostname)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("sem certificado gravado para %s", hostname)
	}

	id, rodando, err := a.dk.acharContainer(ctx, nginxNome)
	if err != nil {
		return err
	}
	if id == "" || !rodando {
		return fmt.Errorf("container %s não está rodando", nginxNome)
	}

	certPath, keyPath := caminhosCert(hostname)
	dir := certsDir + "/" + hostname
	if _, _, err := a.dk.exec(ctx, id, []string{"mkdir", "-p", dir}); err != nil {
		return err
	}
	if err := a.dk.escreverArquivo(ctx, id, dir, "fullchain.pem", []byte(c.CertPEM)); err != nil {
		return fmt.Errorf("escrever %s: %w", certPath, err)
	}
	if err := a.dk.escreverArquivo(ctx, id, dir, "privkey.pem", []byte(c.KeyPEM)); err != nil {
		return fmt.Errorf("escrever %s: %w", keyPath, err)
	}
	return nil
}

// rehidratarCerts reescreve todo cert já emitido no container recém-criado.
// Chamado uma vez logo após o container do nginx subir do zero: se ele foi
// recriado, o filesystem é novo e os certificados sumiram do disco — mas
// não do banco.
func (a *acme) rehidratarCerts(ctx context.Context) {
	certs, err := a.todosCerts()
	if err != nil {
		log.Printf("rehidratar certs: %v", err)
		return
	}
	for _, c := range certs {
		if c.ExpiraEm.IsZero() {
			continue // linha só de erro (emissão nunca chegou a funcionar); nada a rehidratar
		}
		if err := a.escreverNoNginx(ctx, c.Hostname); err != nil {
			log.Printf("rehidratar cert de %s: %v", c.Hostname, err)
		}
	}
}

// garantirCertificados emite o que falta para os hostnames passados. Chamado
// em background depois de toda sincronização do nginx — o vhost HTTP já
// está no ar nesse ponto, prontos para o desafio HTTP-01.
func (a *acme) garantirCertificados(ctx context.Context, hostnames []string) {
	for _, h := range hostnames {
		if _, jaEmitindo := a.emitindo.LoadOrStore(h, struct{}{}); jaEmitindo {
			continue // outra chamada (outro sincronizar quase simultâneo) já cuida deste hostname
		}

		c, err := a.certDe(h)
		if err != nil {
			log.Printf("consultar cert de %s: %v", h, err)
			a.emitindo.Delete(h)
			continue
		}
		if c != nil && time.Until(c.ExpiraEm) > 30*24*time.Hour {
			a.emitindo.Delete(h)
			continue // válido por mais de 30 dias, nada a fazer
		}
		if c != nil {
			if espera := proximaTentativaPermitida(c.FalhasConsecutivas, c.TentativaEm); time.Now().Before(espera) {
				log.Printf("cert de %s: aguardando backoff (%d falha(s) seguida(s), próxima tentativa às %s)",
					h, c.FalhasConsecutivas, espera.Format(time.RFC3339))
				a.emitindo.Delete(h)
				continue
			}
		}
		err = a.emitir(ctx, h)
		a.emitindo.Delete(h)
		if err != nil {
			log.Printf("emitir cert de %s: %v", h, err)
			continue
		}
		log.Printf("certificado de %s emitido/renovado", h)

		// Sem isso o cert fica gravado e escrito no container, mas o nginx
		// nunca é avisado — a config que acabou de ser aplicada em
		// sincronizar() é a de ANTES da emissão terminar (ela roda em
		// background bem depois do apply original). Só uma sincronização
		// nova troca o vhost para o bloco 443 ssl.
		if err := a.ng.sincronizar(ctx, a.portaZeno); err != nil {
			log.Printf("re-sincronizar nginx após emitir cert de %s: %v", h, err)
		}
	}
}

// hostnamesComSSL é a união de quem quer certificado: domínios de app (SSL
// automático é a promessa da v1, sempre ligado) e vhosts do usuário com o
// toggle SSL marcado.
func (a *acme) hostnamesComSSL() ([]string, error) {
	linhas, err := a.db.Query(`
		SELECT hostname FROM domains
		UNION
		SELECT server_name FROM nginx_servers WHERE ssl = 1`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []string
	for linhas.Next() {
		var h string
		if err := linhas.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, linhas.Err()
}

// renovarPeriodicamente roda uma vez por dia. Item 8 do plano: renovação
// silenciosa, sem esperar o usuário notar que expirou.
func (a *acme) renovarPeriodicamente(ctx context.Context, hostnames func() []string) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.garantirCertificados(ctx, hostnames())
		}
	}
}

// garantirConfigSegura recusa TLS inseguro fora de um endpoint que não seja
// a CA de produção — não existe cenário legítimo de apontar produção com
// verificação de certificado desligada.
func garantirConfigSegura(cfg acmeConfig) error {
	if cfg.InsecureTLS && cfg.DirURL == lego.LEDirectoryProduction {
		return fmt.Errorf("ZENO_ACME_INSECURE_TLS não pode ser usado com o endpoint de produção")
	}
	return nil
}

var _ challenge.Provider = (*acme)(nil)
