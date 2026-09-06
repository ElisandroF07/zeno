package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed presets/*.yaml
var presetsFS embed.FS

// Preset descreve um serviço pronto. É dado, não código: adicionar um serviço
// novo ao catálogo é escrever um YAML, nunca recompilar.
type Preset struct {
	ID        string            `yaml:"id"        json:"id"`
	Nome      string            `yaml:"nome"      json:"nome"`
	Categoria string            `yaml:"categoria" json:"categoria"`
	Descricao string            `yaml:"descricao" json:"descricao"`
	Icone     string            `yaml:"icone"     json:"icone"`
	Image     string            `yaml:"image"     json:"image"`
	Porta     int               `yaml:"porta"     json:"porta"`
	Volumes   []string          `yaml:"volumes"   json:"volumes"`
	Env       []CampoEnv        `yaml:"env"       json:"env"`
	EnvFixo   map[string]string `yaml:"env_fixo"  json:"-"`
	Cmd       []string          `yaml:"cmd"       json:"-"`
	AdminUI   *AdminUI          `yaml:"admin_ui"  json:"admin_ui,omitempty"`
	Injeta    map[string]string `yaml:"injeta"    json:"injeta"`
	// DockerSocket monta /var/run/docker.sock no container. Só existe para
	// ferramentas cujo propósito É gerir o Docker do host (Portainer,
	// Dockge) — sem o socket elas sobem decorativas, incapazes de fazer a
	// única coisa para que existem.
	DockerSocket bool `yaml:"docker_socket" json:"docker_socket"`
}

// CampoEnv vira um campo do formulário na UI.
type CampoEnv struct {
	Key         string `yaml:"key"         json:"key"`
	Label       string `yaml:"label"       json:"label"`
	Descricao   string `yaml:"descricao"   json:"descricao"`
	Padrao      string `yaml:"padrao"      json:"padrao"`
	Tipo        string `yaml:"tipo"        json:"tipo"`  // texto | senha
	Gerar       string `yaml:"gerar"       json:"gerar"` // senha = gera se vier vazio
	Obrigatorio bool   `yaml:"obrigatorio" json:"obrigatorio"`
}

type AdminUI struct {
	Porta int    `yaml:"porta" json:"porta"`
	Nome  string `yaml:"nome"  json:"nome"`
}

// carregarPresets lê os presets embutidos e, por cima, os do diretório de
// override — é assim que o usuário adiciona os dele sem tocar no binário.
func carregarPresets(dirOverride string) (map[string]Preset, error) {
	presets := map[string]Preset{}

	entradas, err := presetsFS.ReadDir("presets")
	if err != nil {
		return nil, err
	}
	for _, e := range entradas {
		b, err := presetsFS.ReadFile("presets/" + e.Name())
		if err != nil {
			return nil, err
		}
		p, err := lerPreset(b, e.Name())
		if err != nil {
			return nil, err
		}
		presets[p.ID] = p
	}

	// Diretório ausente é o caso normal, não um erro.
	arquivos, _ := filepath.Glob(filepath.Join(dirOverride, "*.yaml"))
	for _, caminho := range arquivos {
		b, err := os.ReadFile(caminho)
		if err != nil {
			return nil, err
		}
		p, err := lerPreset(b, caminho)
		if err != nil {
			return nil, err
		}
		presets[p.ID] = p
	}
	return presets, nil
}

func lerPreset(b []byte, origem string) (Preset, error) {
	var p Preset
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("%s: %w", origem, err)
	}
	if p.ID == "" || p.Image == "" {
		return p, fmt.Errorf("%s: preset precisa de id e image", origem)
	}
	return p, nil
}

func presetsOrdenados(m map[string]Preset) []Preset {
	out := make([]Preset, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Categoria != out[j].Categoria {
			return out[i].Categoria < out[j].Categoria
		}
		return out[i].Nome < out[j].Nome
	})
	return out
}

// resolverEnv aplica padrões, gera o que precisa ser gerado e valida os
// obrigatórios. O que o usuário mandou sempre ganha do padrão.
func resolverEnv(p Preset, informado map[string]string) (map[string]string, error) {
	env := map[string]string{}
	var faltando []string

	for _, campo := range p.Env {
		valor := strings.TrimSpace(informado[campo.Key])
		if valor == "" {
			valor = campo.Padrao
		}
		if valor == "" && campo.Gerar == "senha" {
			valor = senhaAleatoria()
		}
		if valor == "" && campo.Obrigatorio {
			faltando = append(faltando, campo.Key)
			continue
		}
		env[campo.Key] = valor
	}

	if len(faltando) > 0 {
		return nil, fmt.Errorf("campos obrigatórios em branco: %s", strings.Join(faltando, ", "))
	}
	return env, nil
}

// contextoTemplate é o que os templates do preset enxergam.
type contextoTemplate struct {
	Nome string // nome dado pelo usuário
	Host string // nome DNS do container na rede zeno
	Env  map[string]string
}

func aplicarTemplate(texto string, ctx contextoTemplate) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(texto)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func aplicarTemplateMapa(m map[string]string, ctx contextoTemplate) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, err := aplicarTemplate(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

func aplicarTemplateLista(vs []string, ctx contextoTemplate) ([]string, error) {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s, err := aplicarTemplate(v, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// senhaAleatoria devolve 24 bytes de entropia em base64 url-safe. Sem '/' e
// '+' de propósito: essas senhas vão parar dentro de URLs de conexão.
func senhaAleatoria() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("sem entropia para gerar senha: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
