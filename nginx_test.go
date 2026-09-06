package main

import (
	"strings"
	"testing"
)

func TestRenderConfProxyDinamico(t *testing.T) {
	// Container de app: o IP troca a cada deploy, então precisa resolver em
	// runtime. Sem isso o nginx serve 502 apontando pro IP antigo.
	conf := renderConf([]nginxServer{{
		ServerName: "app.exemplo.com",
		Locations: []nginxLocation{{
			Path: "/", Tipo: "proxy", Alvo: "http://app-loja:3000", Dinamico: true,
		}},
	}}, "8080")

	for _, quer := range []string{
		"server_name app.exemplo.com;",
		"resolver 127.0.0.11 valid=10s;",
		"set $upstream http://app-loja:3000;",
		"proxy_pass $upstream;",
	} {
		if !strings.Contains(conf, quer) {
			t.Errorf("conf não contém %q:\n%s", quer, conf)
		}
	}
}

func TestRenderConfProxyEstatico(t *testing.T) {
	// Alvo vindo do /etc/hosts: com variável o nginx consultaria só o
	// resolver e ignoraria o /etc/hosts, quebrando o proxy.
	conf := renderConf([]nginxServer{vhostZeno("8080")}, "8080")

	if !strings.Contains(conf, "proxy_pass http://host.docker.internal:8080;") {
		t.Errorf("proxy_pass estático ausente:\n%s", conf)
	}
	if strings.Contains(conf, "$upstream") {
		t.Errorf("vhost do Zeno não pode usar upstream por variável:\n%s", conf)
	}
	// SSE dos logs ao vivo morre se o nginx bufferizar.
	if !strings.Contains(conf, "proxy_buffering off;") {
		t.Errorf("proxy_buffering off ausente:\n%s", conf)
	}
}

func TestRenderConfRedirectEStatic(t *testing.T) {
	conf := renderConf([]nginxServer{{
		ServerName: "velho.com",
		Locations: []nginxLocation{
			{Path: "/", Tipo: "redirect", Alvo: "https://novo.com$request_uri"},
			{Path: "/assets/", Tipo: "static", Alvo: "/var/www/site"},
		},
	}}, "8080")

	if !strings.Contains(conf, "return 301 https://novo.com$request_uri;") {
		t.Errorf("redirect ausente:\n%s", conf)
	}
	if !strings.Contains(conf, "root /var/www/site;") {
		t.Errorf("static ausente:\n%s", conf)
	}
	// Redirect não pode arrastar diretiva de proxy junto.
	if strings.Contains(conf, "proxy_pass") {
		t.Errorf("redirect não deve gerar proxy_pass:\n%s", conf)
	}
}

// Regressão: sem `default_server` no vhost do Zeno, o catch-all vira o
// primeiro server block carregado. Como conf.d/ carrega em ordem alfabética,
// user.conf vem antes de zeno-apps.conf e o vhost do usuário passa a receber
// todo request sem Host correspondente — inclusive os da própria UI.
func TestVhostZenoEhDefaultServer(t *testing.T) {
	conf := renderConf([]nginxServer{vhostZeno("8080")}, "8080")
	if !strings.Contains(conf, "listen 80 default_server;") {
		t.Errorf("vhost do Zeno precisa ser default_server:\n%s", conf)
	}

	// Vhost do usuário nunca pode reivindicar o default: dois default_server
	// na mesma porta e o nginx recusa a config inteira.
	doUsuario := renderConf([]nginxServer{{
		ServerName: "api.exemplo.com",
		Locations:  []nginxLocation{{Path: "/", Tipo: "proxy", Alvo: "http://app:3000", Dinamico: true}},
	}}, "8080")
	if strings.Contains(doUsuario, "default_server") {
		t.Errorf("vhost do usuário não pode ser default_server:\n%s", doUsuario)
	}
	if !strings.Contains(doUsuario, "listen 80;") {
		t.Errorf("listen simples ausente:\n%s", doUsuario)
	}
}

// Sem certificado ainda: só HTTP, sem redirect (redirecionar antes de ter
// cert deixaria o domínio inacessível), mas já servindo o desafio — é o
// desafio respondendo que faz a emissão do primeiro certificado funcionar.
func TestRenderConfSSLProvisionando(t *testing.T) {
	conf := renderConf([]nginxServer{{
		ServerName: "novo.com",
		SSL:        true,
		CertPronto: false,
		Locations:  []nginxLocation{{Path: "/", Tipo: "proxy", Alvo: "http://app:3000", Dinamico: true}},
	}}, "8080")

	if !strings.Contains(conf, "location /.well-known/acme-challenge/ {") {
		t.Errorf("location do desafio ACME ausente:\n%s", conf)
	}
	if !strings.Contains(conf, "proxy_pass http://host.docker.internal:8080;") {
		t.Errorf("desafio não aponta para o Zeno:\n%s", conf)
	}
	if strings.Contains(conf, "listen 443") {
		t.Errorf("não pode abrir 443 sem certificado:\n%s", conf)
	}
	if strings.Contains(conf, "return 301 https") {
		t.Errorf("não pode redirecionar para https sem certificado:\n%s", conf)
	}
}

// Com certificado pronto: HTTP só existe para desafio de renovação e
// redirect; o tráfego de verdade vai para o bloco 443.
func TestRenderConfSSLPronto(t *testing.T) {
	conf := renderConf([]nginxServer{{
		ServerName: "pronto.com",
		SSL:        true,
		CertPronto: true,
		Locations:  []nginxLocation{{Path: "/", Tipo: "proxy", Alvo: "http://app:3000", Dinamico: true}},
	}}, "8080")

	if !strings.Contains(conf, "listen 443 ssl;") {
		t.Errorf("bloco 443 ausente:\n%s", conf)
	}
	if !strings.Contains(conf, "ssl_certificate /etc/nginx/certs/pronto.com/fullchain.pem;") {
		t.Errorf("ssl_certificate ausente ou caminho errado:\n%s", conf)
	}
	if !strings.Contains(conf, "ssl_certificate_key /etc/nginx/certs/pronto.com/privkey.pem;") {
		t.Errorf("ssl_certificate_key ausente ou caminho errado:\n%s", conf)
	}
	if !strings.Contains(conf, "return 301 https://$host$request_uri;") {
		t.Errorf("HTTP deveria redirecionar para HTTPS uma vez com cert:\n%s", conf)
	}
	if !strings.Contains(conf, "location /.well-known/acme-challenge/ {") {
		t.Errorf("desafio precisa continuar servido para a renovação funcionar:\n%s", conf)
	}
}

func TestPorta(t *testing.T) {
	casos := map[string]string{
		":8080":          "8080",
		"0.0.0.0:9000":   "9000",
		"127.0.0.1:3000": "3000",
		"lixo":           "8080", // fallback, não pode entrar em pânico
	}
	for entrada, quer := range casos {
		if got := porta(entrada); got != quer {
			t.Errorf("porta(%q) = %q, quer %q", entrada, got, quer)
		}
	}
}
