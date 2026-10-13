package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
)

// Ordem de tentativa do plano: Dockerfile existente → Nixpacks → preset manual.
// Quem sabe buildar o projeto é o Nixpacks; o Zeno não reimplementa isso.
const (
	estrategiaDockerfile = "dockerfile"
	estrategiaNixpacks   = "nixpacks"
)

// clonar traz o repositório raso — histórico não serve para buildar e custa
// minutos em repositório grande.
//
// repoURL pode carregar credencial embutida (repo privado do GitHub via
// comAutenticacao) — por isso todo o log daqui passa por um writer que
// mascara qualquer coisa que pareça `://usuario:token@`, tanto a linha "$
// git clone ..." quanto o que o próprio git ecoa no stderr em caso de erro
// (`fatal: unable to access 'https://TOKEN@github.com/...'`). Vazar o token
// no log do deploy — gravado em arquivo, mostrado na tela via SSE — seria
// pior que qualquer bug que a integração resolve.
func clonar(ctx context.Context, repoURL, branch, destino string, log io.Writer) (sha string, err error) {
	args := []string{"clone", "--depth", "1", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, destino)

	logSeguro := &writerMascarado{dest: log}
	defer logSeguro.Flush()

	if err := rodar(ctx, logSeguro, "", "git", args...); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}

	saida, err := exec.CommandContext(ctx, "git", "-C", destino, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("ler commit: %w", err)
	}
	return strings.TrimSpace(string(saida)), nil
}

// detectarEstrategia devolve como buildar. Dockerfile do projeto ganha sempre:
// se o time escreveu um, é ele que descreve o build de verdade.
func detectarEstrategia(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return estrategiaDockerfile
	}
	return estrategiaNixpacks
}

func (d *deploys) buildar(ctx context.Context, dir, tag, estrategia string, log io.Writer) error {
	switch estrategia {
	case estrategiaDockerfile:
		return d.buildDockerfile(ctx, dir, tag, log)
	case estrategiaNixpacks:
		return buildNixpacks(ctx, dir, tag, log)
	default:
		return fmt.Errorf("estratégia de build desconhecida: %q", estrategia)
	}
}

func (d *deploys) buildDockerfile(ctx context.Context, dir, tag string, log io.Writer) error {
	contexto, err := tarDiretorio(dir)
	if err != nil {
		return fmt.Errorf("empacotar contexto: %w", err)
	}

	res, err := d.dk.ImageBuild(ctx, contexto, client.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()

	return streamBuild(res.Body, log)
}

// buildNixpacks chama o binário externo. Não é lib Go — é um executável Rust
// que precisa estar no PATH, e a falta dele tem que virar mensagem clara.
func buildNixpacks(ctx context.Context, dir, tag string, log io.Writer) error {
	if _, err := exec.LookPath("nixpacks"); err != nil {
		return fmt.Errorf(
			"projeto sem Dockerfile e o Nixpacks não está instalado.\n" +
				"O Nixpacks é um binário separado (não é lib Go) e precisa estar no PATH.\n" +
				"Instale com: curl -sSL https://nixpacks.com/install.sh | bash\n" +
				"Ou adicione um Dockerfile ao projeto.")
	}
	return rodar(ctx, log, dir, "nixpacks", "build", ".", "--name", tag)
}

// streamBuild traduz o NDJSON do build para texto legível no log.
func streamBuild(corpo io.Reader, log io.Writer) error {
	dec := json.NewDecoder(corpo)
	for {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if msg.Stream != "" {
			fmt.Fprint(log, msg.Stream)
		}
		// O build reporta falha no corpo da resposta, não no status HTTP:
		// sem checar isso aqui, um build quebrado passaria por sucesso.
		if msg.Error != "" {
			fmt.Fprintf(log, "\nERRO: %s\n", msg.Error)
			return fmt.Errorf("build falhou: %s", msg.Error)
		}
	}
}

// rodar executa um comando jogando saída combinada no log do deploy.
func rodar(ctx context.Context, log io.Writer, dir, nome string, args ...string) error {
	fmt.Fprintf(log, "$ %s %s\n", nome, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, nome, args...)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}

// tarDiretorio empacota o projeto como contexto de build, pulando .git — que
// costuma ser maior que o código e não entra na imagem.
func tarDiretorio(raiz string) (io.Reader, error) {
	leitor, escritor := io.Pipe()

	go func() {
		tw := tar.NewWriter(escritor)
		err := filepath.Walk(raiz, func(caminho string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(raiz, caminho)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			// O tar do Docker exige separador '/' mesmo vindo do Windows.
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() || !info.Mode().IsRegular() {
				return nil
			}

			f, err := os.Open(caminho)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			escritor.CloseWithError(err)
			return
		}
		escritor.CloseWithError(tw.Close())
	}()

	return leitor, nil
}
