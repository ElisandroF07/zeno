package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type servicos struct {
	dk      *docker
	db      *sql.DB
	presets map[string]Preset
}

type Servico struct {
	ID        int64             `json:"id"`
	PresetID  string            `json:"preset_id"`
	Nome      string            `json:"nome"`
	Preset    *Preset           `json:"preset,omitempty"`
	Env       map[string]string `json:"env"`
	Injetadas map[string]string `json:"injetadas"`
	Estado    string            `json:"estado"`
	Status    string            `json:"status"`
	AdminURL  string            `json:"admin_url,omitempty"`
}

// Nome vira nome de container e nome DNS na rede: precisa ser previsível.
var nomeValido = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

func containerDoServico(nome string) string { return "zeno-" + nome }

func (s *servicos) criar(ctx context.Context, presetID, nome string, informado map[string]string) (*Servico, error) {
	preset, ok := s.presets[presetID]
	if !ok {
		return nil, fmt.Errorf("preset %q não existe", presetID)
	}
	if !nomeValido.MatchString(nome) {
		return nil, fmt.Errorf("nome inválido: use letras minúsculas, números e hífen (2 a 32 caracteres, sem hífen nas pontas)")
	}

	env, err := resolverEnv(preset, informado)
	if err != nil {
		return nil, err
	}

	host := containerDoServico(nome)
	ctxTmpl := contextoTemplate{Nome: nome, Host: host, Env: env}

	injetadas, err := aplicarTemplateMapa(preset.Injeta, ctxTmpl)
	if err != nil {
		return nil, fmt.Errorf("montar variáveis de conexão: %w", err)
	}
	envFixo, err := aplicarTemplateMapa(preset.EnvFixo, ctxTmpl)
	if err != nil {
		return nil, fmt.Errorf("env fixo do preset: %w", err)
	}
	cmd, err := aplicarTemplateLista(preset.Cmd, ctxTmpl)
	if err != nil {
		return nil, fmt.Errorf("comando do preset: %w", err)
	}

	// Grava antes de subir: container órfão sem registro é pior de achar do
	// que registro sem container, que a própria lista mostra como ausente.
	envJSON, _ := json.Marshal(env)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO services (preset_id, nome, config_json, volume_name, status) VALUES (?, ?, ?, ?, 'criando')`,
		presetID, nome, string(envJSON), host)
	if err != nil {
		return nil, fmt.Errorf("gravar serviço (nome já usado?): %w", err)
	}
	id, _ := res.LastInsertId()

	if err := s.subirContainer(ctx, preset, nome, env, envFixo, cmd); err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id)
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE services SET status = 'rodando' WHERE id = ?`, id)

	return &Servico{
		ID: id, PresetID: presetID, Nome: nome, Preset: &preset,
		Env: env, Injetadas: injetadas, Estado: "running",
	}, nil
}

func (s *servicos) subirContainer(ctx context.Context, p Preset, nome string, env, envFixo map[string]string, cmd []string) error {
	if err := s.dk.garantirRede(ctx, redeZeno); err != nil {
		return err
	}
	if err := s.dk.garantirImagem(ctx, p.Image); err != nil {
		return fmt.Errorf("baixar %s: %w", p.Image, err)
	}

	lista := make([]string, 0, len(env)+len(envFixo))
	for k, v := range env {
		lista = append(lista, k+"="+v)
	}
	for k, v := range envFixo {
		lista = append(lista, k+"="+v)
	}
	sort.Strings(lista) // ordem estável facilita diff e depuração

	host := containerDoServico(nome)

	// Volume nomeado por caminho montado. O Docker cria sozinho no primeiro uso.
	binds := make([]string, 0, len(p.Volumes)+1)
	for i, caminho := range p.Volumes {
		binds = append(binds, fmt.Sprintf("%s-vol%d:%s", host, i, caminho))
	}
	if p.DockerSocket {
		// :ro não serve: gerir containers (start/stop/create) precisa de
		// escrita no socket, não só leitura de estado.
		binds = append(binds, "/var/run/docker.sock:/var/run/docker.sock")
	}

	expostas := network.PortSet{}
	if p.Porta > 0 {
		expostas[network.MustParsePort(fmt.Sprintf("%d/tcp", p.Porta))] = struct{}{}
	}

	// Só o painel de admin sai para o host, e preso em 127.0.0.1. A porta de
	// dados fica só na rede zeno: Postgres publicado no IP público da VPS é
	// um banco na internet aberta.
	portas := network.PortMap{}
	if p.AdminUI != nil && p.AdminUI.Porta > 0 {
		porta := network.MustParsePort(fmt.Sprintf("%d/tcp", p.AdminUI.Porta))
		expostas[porta] = struct{}{}
		portas[porta] = []network.PortBinding{{
			HostIP:   netip.MustParseAddr("127.0.0.1"),
			HostPort: fmt.Sprintf("%d", p.AdminUI.Porta),
		}}
	}

	criado, err := s.dk.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: host,
		Config: &container.Config{
			Image: p.Image,
			Env:   lista,
			Cmd:   cmd,
			Labels: map[string]string{
				labelManaged: "true",
				labelTipo:    "service",
				labelNome:    nome,
			},
			ExposedPorts: expostas,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(redeZeno),
			Binds:         binds,
			PortBindings:  portas,
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
	})
	if err != nil {
		return fmt.Errorf("criar container: %w", err)
	}
	if _, err := s.dk.ContainerStart(ctx, criado.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("iniciar container: %w", err)
	}
	return nil
}

func (s *servicos) listar(ctx context.Context) ([]Servico, error) {
	linhas, err := s.db.QueryContext(ctx,
		`SELECT id, preset_id, nome, config_json FROM services ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []Servico
	for linhas.Next() {
		var sv Servico
		var envJSON string
		if err := linhas.Scan(&sv.ID, &sv.PresetID, &sv.Nome, &envJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(envJSON), &sv.Env)

		if p, ok := s.presets[sv.PresetID]; ok {
			sv.Preset = &p
			host := containerDoServico(sv.Nome)
			sv.Injetadas, _ = aplicarTemplateMapa(p.Injeta, contextoTemplate{
				Nome: sv.Nome, Host: host, Env: sv.Env,
			})
			if p.AdminUI != nil {
				sv.AdminURL = fmt.Sprintf("http://127.0.0.1:%d", p.AdminUI.Porta)
			}
		}
		out = append(out, sv)
	}
	if err := linhas.Err(); err != nil {
		return nil, err
	}

	// Estado real vem do Docker, não do banco: alguém pode ter parado o
	// container por fora e o banco não ficaria sabendo.
	if cs, err := s.dk.listar(ctx); err == nil {
		estados := map[string]Container{}
		for _, c := range cs {
			estados[c.Nome] = c
		}
		for i := range out {
			if c, ok := estados[containerDoServico(out[i].Nome)]; ok {
				out[i].Estado, out[i].Status = c.Estado, c.Status
			} else {
				out[i].Estado, out[i].Status = "ausente", "container não existe"
			}
		}
	}
	return out, nil
}

func (s *servicos) acao(ctx context.Context, nome, acao string) error {
	id, _, err := s.dk.acharContainer(ctx, containerDoServico(nome))
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container do serviço %q não existe", nome)
	}

	switch acao {
	case "start":
		_, err = s.dk.ContainerStart(ctx, id, client.ContainerStartOptions{})
	case "stop":
		_, err = s.dk.ContainerStop(ctx, id, client.ContainerStopOptions{})
	case "restart":
		_, err = s.dk.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	default:
		err = fmt.Errorf("ação desconhecida: %q", acao)
	}
	return err
}

// remover apaga container e registro. O volume fica de propósito: apagar
// dado de banco por clique de botão é o tipo de coisa que não se desfaz.
func (s *servicos) remover(ctx context.Context, nome string) error {
	id, _, err := s.dk.acharContainer(ctx, containerDoServico(nome))
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := s.dk.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM services WHERE nome = ?`, nome)
	return err
}
