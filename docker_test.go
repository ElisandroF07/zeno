package main

import (
	"os"
	"reflect"
	"testing"
)

func TestPortaLabel(t *testing.T) {
	casos := []struct {
		pub, priv uint16
		proto     string
		quer      string
	}{
		{0, 5432, "tcp", "5432/tcp"},     // só exposta
		{8080, 80, "tcp", "8080→80/tcp"}, // publicada
		{53, 53, "udp", "53→53/udp"},
	}
	for _, c := range casos {
		if got := portaLabel(c.pub, c.priv, c.proto); got != c.quer {
			t.Errorf("portaLabel(%d,%d,%q) = %q, quer %q", c.pub, c.priv, c.proto, got, c.quer)
		}
	}
}

func TestUnicos(t *testing.T) {
	// 0.0.0.0 e :: geram a mesma porta duas vezes; a ordem deve ser preservada.
	got := unicos([]string{"8080→80/tcp", "8080→80/tcp", "443/tcp"})
	quer := []string{"8080→80/tcp", "443/tcp"}
	if !reflect.DeepEqual(got, quer) {
		t.Errorf("unicos = %v, quer %v", got, quer)
	}
}

func TestCurto(t *testing.T) {
	if got := curto("ae63e8b89a26f01f6b4b2c9a"); got != "ae63e8b89a26" {
		t.Errorf("curto longo = %q", got)
	}
	if got := curto("abc"); got != "abc" { // não pode entrar em pânico com id curto
		t.Errorf("curto curto = %q", got)
	}
}

func TestPrimeiroNome(t *testing.T) {
	if got := primeiroNome([]string{"/meu-app"}); got != "meu-app" {
		t.Errorf("primeiroNome = %q", got)
	}
	if got := primeiroNome(nil); got != "<sem nome>" {
		t.Errorf("primeiroNome(nil) = %q", got)
	}
}

func TestAbrirDB(t *testing.T) {
	caminho := t.TempDir() + string(os.PathSeparator) + "teste.db"
	db, err := abrirDB(caminho)
	if err != nil {
		t.Fatalf("abrirDB: %v", err)
	}
	defer db.Close()

	// Schema aplicado e foreign_keys ligada pela DSN.
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, err = %v", fk, err)
	}
	if _, err := db.Exec(`INSERT INTO apps (nome, repo_url) VALUES ('x', 'git@x')`); err != nil {
		t.Errorf("insert em apps: %v", err)
	}
}
