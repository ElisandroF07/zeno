package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/moby/moby/client"
)

// Labels aplicadas em tudo que o Zeno cria. Ver container como "meu" é só
// olhar label — nada de prefixo de nome ou lista paralela no banco.
const (
	labelManaged = "zeno.managed" // "true"
	labelTipo    = "zeno.tipo"    // "app" | "service"
	labelNome    = "zeno.nome"
)

type docker struct{ *client.Client }

func novoDocker() (*docker, error) {
	c, err := client.New(client.FromEnv, client.WithUserAgent("zeno"))
	if err != nil {
		return nil, err
	}
	return &docker{c}, nil
}

type Container struct {
	ID     string   `json:"id"`
	Nome   string   `json:"nome"`
	Imagem string   `json:"imagem"`
	Estado string   `json:"estado"` // running | exited | created ...
	Status string   `json:"status"` // "Up 3 hours"
	Portas []string `json:"portas"`
	Zeno   bool     `json:"zeno"`
	Tipo   string   `json:"tipo,omitempty"`
}

func (d *docker) listar(ctx context.Context) ([]Container, error) {
	res, err := d.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}

	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		var portas []string
		for _, p := range c.Ports {
			portas = append(portas, portaLabel(p.PublicPort, p.PrivatePort, string(p.Type)))
		}
		out = append(out, Container{
			ID:     curto(c.ID),
			Nome:   primeiroNome(c.Names),
			Imagem: c.Image,
			Estado: string(c.State),
			Status: c.Status,
			Portas: unicos(portas),
			Zeno:   c.Labels[labelManaged] == "true",
			Tipo:   c.Labels[labelTipo],
		})
	}

	// Containers do Zeno primeiro, depois alfabético.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Zeno != out[j].Zeno {
			return out[i].Zeno
		}
		return out[i].Nome < out[j].Nome
	})
	return out, nil
}

func curto(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func primeiroNome(nomes []string) string {
	if len(nomes) == 0 {
		return "<sem nome>"
	}
	return strings.TrimPrefix(nomes[0], "/")
}

func portaLabel(publica, privada uint16, proto string) string {
	if publica == 0 {
		return fmt.Sprintf("%d/%s", privada, proto)
	}
	return fmt.Sprintf("%d→%d/%s", publica, privada, proto)
}

// Rede onde tudo que o Zeno sobe se enxerga pelo nome do container.
const redeZeno = "zeno"

func (d *docker) garantirRede(ctx context.Context, nome string) error {
	if _, err := d.NetworkInspect(ctx, nome, client.NetworkInspectOptions{}); err == nil {
		return nil
	}
	_, err := d.NetworkCreate(ctx, nome, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{labelManaged: "true"},
	})
	return err
}

// acharContainer devolve o id de um container pelo nome exato, rodando ou não.
func (d *docker) acharContainer(ctx context.Context, nome string) (id string, rodando bool, err error) {
	res, err := d.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return "", false, err
	}
	for _, c := range res.Items {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == nome {
				return c.ID, string(c.State) == "running", nil
			}
		}
	}
	return "", false, nil
}

func (d *docker) garantirImagem(ctx context.Context, ref string) error {
	res, err := d.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer res.Close()
	// O pull só termina quando o corpo é drenado até o fim.
	_, err = io.Copy(io.Discard, res)
	return err
}

// exec roda um comando no container e devolve a saída combinada e o exit code.
// TTY ligado de propósito: junta stdout e stderr num stream cru, que é
// exatamente o formato de um erro do `nginx -t`.
func (d *docker) exec(ctx context.Context, id string, cmd []string) (saida string, code int, err error) {
	criado, err := d.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          true,
	})
	if err != nil {
		return "", 0, err
	}

	anexo, err := d.ExecAttach(ctx, criado.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return "", 0, err
	}
	defer anexo.Close()

	bruto, err := io.ReadAll(anexo.Reader)
	if err != nil {
		return "", 0, err
	}

	info, err := d.ExecInspect(ctx, criado.ID, client.ExecInspectOptions{})
	if err != nil {
		return string(bruto), 0, err
	}
	return strings.TrimSpace(string(bruto)), info.ExitCode, nil
}

// escreverArquivo joga um arquivo dentro do container. A API só aceita tar,
// então o arquivo vira um tar de uma entrada só.
func (d *docker) escreverArquivo(ctx context.Context, id, dir, nome string, conteudo []byte) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: nome,
		Mode: 0o644,
		Size: int64(len(conteudo)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(conteudo); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	_, err := d.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: dir,
		Content:         &buf,
	})
	return err
}

// O Docker lista a mesma porta uma vez por família de IP (0.0.0.0 e ::).
func unicos(vs []string) []string {
	visto := make(map[string]bool, len(vs))
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if !visto[v] {
			visto[v] = true
			out = append(out, v)
		}
	}
	return out
}
