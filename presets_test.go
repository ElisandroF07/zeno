package main

import (
	"strings"
	"testing"
)

// Todo preset embutido precisa carregar. Um YAML torto só aparece em runtime
// se ninguém abrir o catálogo — aqui aparece no CI.
func TestPresetsEmbutidosCarregam(t *testing.T) {
	presets, err := carregarPresets("nao-existe")
	if err != nil {
		t.Fatalf("carregarPresets: %v", err)
	}
	if len(presets) < 40 {
		t.Fatalf("esperava ao menos 40 presets, veio %d", len(presets))
	}

	for id, p := range presets {
		if p.Nome == "" || p.Image == "" || p.Categoria == "" {
			t.Errorf("preset %q incompleto: %+v", id, p)
		}
		// Tag fixada: "latest" só onde o upstream não publica versão estável.
		if !strings.Contains(p.Image, ":") {
			t.Errorf("preset %q sem tag na imagem: %q", id, p.Image)
		}
		if p.Porta == 0 {
			t.Errorf("preset %q sem porta", id)
		}
	}

	for _, id := range []string{"redis", "postgres", "mysql", "mongodb", "rabbitmq", "minio", "kafka"} {
		if _, ok := presets[id]; !ok {
			t.Errorf("preset %q sumiu do catálogo", id)
		}
	}
}

func TestResolverEnvPadraoEGeracao(t *testing.T) {
	presets, err := carregarPresets("nao-existe")
	if err != nil {
		t.Fatal(err)
	}
	pg := presets["postgres"]

	env, err := resolverEnv(pg, map[string]string{})
	if err != nil {
		t.Fatalf("resolverEnv: %v", err)
	}
	if env["POSTGRES_USER"] != "zeno" {
		t.Errorf("padrão não aplicado: %q", env["POSTGRES_USER"])
	}
	if len(env["POSTGRES_PASSWORD"]) < 20 {
		t.Errorf("senha não foi gerada: %q", env["POSTGRES_PASSWORD"])
	}
	// A senha entra numa URL de conexão; '/' e '+' quebrariam o parse.
	if strings.ContainsAny(env["POSTGRES_PASSWORD"], "/+=") {
		t.Errorf("senha com caractere problemático em URL: %q", env["POSTGRES_PASSWORD"])
	}

	// O que o usuário informa ganha do padrão.
	env, err = resolverEnv(pg, map[string]string{"POSTGRES_USER": "loja", "POSTGRES_PASSWORD": "s3nh4"})
	if err != nil {
		t.Fatal(err)
	}
	if env["POSTGRES_USER"] != "loja" || env["POSTGRES_PASSWORD"] != "s3nh4" {
		t.Errorf("valor informado foi ignorado: %+v", env)
	}
}

func TestResolverEnvObrigatorioEmBranco(t *testing.T) {
	p := Preset{ID: "x", Image: "x:1", Env: []CampoEnv{
		{Key: "OBRIGATORIO", Obrigatorio: true},
	}}
	if _, err := resolverEnv(p, map[string]string{"OBRIGATORIO": "   "}); err == nil {
		t.Fatal("espaço em branco deveria contar como vazio")
	}
}

func TestInjetaMontaURLDeConexao(t *testing.T) {
	presets, _ := carregarPresets("nao-existe")
	pg := presets["postgres"]

	env, _ := resolverEnv(pg, map[string]string{
		"POSTGRES_USER": "loja", "POSTGRES_PASSWORD": "abc123", "POSTGRES_DB": "vendas",
	})
	injetadas, err := aplicarTemplateMapa(pg.Injeta, contextoTemplate{
		Nome: "meu-pg", Host: "zeno-meu-pg", Env: env,
	})
	if err != nil {
		t.Fatalf("template: %v", err)
	}

	quer := "postgres://loja:abc123@zeno-meu-pg:5432/vendas?sslmode=disable"
	if injetadas["DATABASE_URL"] != quer {
		t.Errorf("DATABASE_URL = %q, quer %q", injetadas["DATABASE_URL"], quer)
	}
}

// O Kafka em KRaft precisa se anunciar com o nome DNS do container; anunciar
// "localhost" faz o cliente conectar e depois falhar ao produzir.
func TestKafkaAnunciaHostDoContainer(t *testing.T) {
	presets, _ := carregarPresets("nao-existe")
	kafka := presets["kafka"]

	fixo, err := aplicarTemplateMapa(kafka.EnvFixo, contextoTemplate{
		Nome: "fila", Host: "zeno-fila", Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	if fixo["KAFKA_ADVERTISED_LISTENERS"] != "PLAINTEXT://zeno-fila:9092" {
		t.Errorf("advertised listeners = %q", fixo["KAFKA_ADVERTISED_LISTENERS"])
	}
}

func TestCmdComTemplate(t *testing.T) {
	presets, _ := carregarPresets("nao-existe")
	redis := presets["redis"]

	cmd, err := aplicarTemplateLista(redis.Cmd, contextoTemplate{
		Nome: "cache", Host: "zeno-cache", Env: map[string]string{"REDIS_PASSWORD": "segredo"},
	})
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	if len(cmd) < 3 || cmd[2] != "segredo" {
		t.Errorf("senha não entrou no comando: %v", cmd)
	}
}

func TestNomeDeServicoValido(t *testing.T) {
	validos := []string{"pg", "meu-redis", "fila-1"}
	invalidos := []string{"", "a", "-pg", "pg-", "Meu-PG", "com espaço", "com_underscore"}

	for _, n := range validos {
		if !nomeValido.MatchString(n) {
			t.Errorf("%q deveria ser válido", n)
		}
	}
	for _, n := range invalidos {
		if nomeValido.MatchString(n) {
			t.Errorf("%q deveria ser inválido", n)
		}
	}
}
