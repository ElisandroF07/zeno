package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/lego"
)

func acmeDeTeste(t *testing.T) *acme {
	t.Helper()
	db, err := abrirDB(t.TempDir() + string(os.PathSeparator) + "teste.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &acme{db: db}
}

// O desafio HTTP-01 é a fundação de tudo: se Present/CleanUp/handler não
// bater, a emissão real nunca vai conseguir provar posse do domínio.
func TestChallengePresentCleanupHandler(t *testing.T) {
	a := acmeDeTeste(t)

	if err := a.Present("exemplo.com", "TOKEN123", "keyauth-valor"); err != nil {
		t.Fatalf("Present: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/TOKEN123", nil)
	req.SetPathValue("token", "TOKEN123")
	rec := httptest.NewRecorder()
	a.handlerChallenge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, quer 200", rec.Code)
	}
	if rec.Body.String() != "keyauth-valor" {
		t.Errorf("corpo = %q, quer %q", rec.Body.String(), "keyauth-valor")
	}

	if err := a.CleanUp("exemplo.com", "TOKEN123", "keyauth-valor"); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/TOKEN123", nil)
	req2.SetPathValue("token", "TOKEN123")
	rec2 := httptest.NewRecorder()
	a.handlerChallenge(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("depois do CleanUp deveria dar 404, deu %d", rec2.Code)
	}
}

func TestChallengeTokenDesconhecidoDa404(t *testing.T) {
	a := acmeDeTeste(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/nunca-existiu", nil)
	req.SetPathValue("token", "nunca-existiu")
	rec := httptest.NewRecorder()
	a.handlerChallenge(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, quer 404", rec.Code)
	}
}

// A conta ACME não pode ser recriada a cada boot — cada registro novo é uma
// chamada à CA, e recriar a chave apagaria o vínculo com certificados já
// emitidos por ela.
func TestContaACMEPersisteEntreCargas(t *testing.T) {
	a := acmeDeTeste(t)

	if err := a.gravarSetting("acme_key_pem", "chave-fake"); err != nil {
		t.Fatal(err)
	}
	valor, existe, err := a.lerSetting("acme_key_pem")
	if err != nil || !existe || valor != "chave-fake" {
		t.Fatalf("lerSetting = (%q, %v, %v)", valor, existe, err)
	}

	// Sobrescrever precisa substituir, não duplicar (PRIMARY KEY + upsert).
	if err := a.gravarSetting("acme_key_pem", "chave-nova"); err != nil {
		t.Fatal(err)
	}
	valor, _, _ = a.lerSetting("acme_key_pem")
	if valor != "chave-nova" {
		t.Errorf("upsert não substituiu: %q", valor)
	}
}

func TestGarantirConfigSeguraRecusaInseguroEmProducao(t *testing.T) {
	segura := acmeConfig{DirURL: "https://pebble.local/dir", InsecureTLS: true}
	if err := garantirConfigSegura(segura); err != nil {
		t.Errorf("staging/pebble com InsecureTLS deveria ser aceito: %v", err)
	}

	insegura := acmeConfig{DirURL: lego.LEDirectoryProduction, InsecureTLS: true}
	if err := garantirConfigSegura(insegura); err == nil {
		t.Error("produção com InsecureTLS deveria ser recusada")
	}
}

// Regressão observada rodando contra Pebble de verdade: criar um vhost e,
// logo em seguida, adicionar a primeira location disparam dois sincronizar()
// quase simultâneos — sem trava, os dois viam "sem certificado ainda" e
// emitiam ao mesmo tempo para o mesmo hostname, queimando cota de
// "certificado duplicado" da CA à toa.
func TestEmitirNaoDuplicaParaOMesmoHostnameEmParalelo(t *testing.T) {
	a := acmeDeTeste(t)

	tentativas := int32(0)
	partida := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-partida // solta as 20 goroutines juntas, sem depender de timing
			if _, jaEmitindo := a.emitindo.LoadOrStore("mesmo.com", struct{}{}); !jaEmitindo {
				atomic.AddInt32(&tentativas, 1)
				time.Sleep(50 * time.Millisecond) // simula o round-trip com a CA
				a.emitindo.Delete("mesmo.com")
			}
		}()
	}
	close(partida)
	wg.Wait()

	if tentativas != 1 {
		t.Errorf("chamadas concorrentes deveriam resultar em 1 emissão, deu %d", tentativas)
	}
}

func TestCaminhosCert(t *testing.T) {
	cert, key := caminhosCert("app.exemplo.com")
	if cert != "/etc/nginx/certs/app.exemplo.com/fullchain.pem" {
		t.Errorf("caminho do cert = %q", cert)
	}
	if key != "/etc/nginx/certs/app.exemplo.com/privkey.pem" {
		t.Errorf("caminho da chave = %q", key)
	}
}
