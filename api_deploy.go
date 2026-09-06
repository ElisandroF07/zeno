package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func rotasDeploy(mux *http.ServeMux, dp *deploys) {
	// Clona raso só para inspecionar e joga fora — não builda, não guarda
	// nada. Existe para pré-preencher a tela de criação de app, nunca para
	// decidir sozinho: o resultado chega editável.
	mux.HandleFunc("POST /api/apps/detectar", func(w http.ResponseWriter, r *http.Request) {
		var corpo struct {
			RepoURL string `json:"repo_url"`
			Branch  string `json:"branch"`
		}
		if !lerJSON(w, r, &corpo) {
			return
		}
		if corpo.RepoURL == "" {
			erroHTTP(w, http.StatusBadRequest, "repo_url é obrigatório")
			return
		}
		// Branch vazio não vira "main": nem todo repositório usa esse nome
		// (projetos mais antigos, ou qualquer coisa fora do GitHub, muitas
		// vezes ainda usam "master"). git clone sem --branch resolve sozinho
		// a branch padrão do remoto — forçar um nome aqui só quebra clone de
		// quem não usa main.

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		deteccao, err := detectarRepo(ctx, corpo.RepoURL, corpo.Branch)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		responder(w, http.StatusOK, deteccao)
	})

	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		apps, err := dp.listarApps(r.Context())
		if err != nil {
			erroHTTP(w, http.StatusBadGateway, err.Error())
			return
		}
		if apps == nil {
			apps = []App{}
		}
		responder(w, http.StatusOK, apps)
	})

	mux.HandleFunc("POST /api/apps", func(w http.ResponseWriter, r *http.Request) {
		var a App
		if !lerJSON(w, r, &a) {
			return
		}
		id, err := dp.criarApp(r.Context(), a)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		responder(w, http.StatusCreated, map[string]any{"id": id})
	})

	mux.HandleFunc("POST /api/apps/{id}/deploy", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		deployID, err := dp.iniciarDeploy(r.Context(), id)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// 202: aceito e rodando em background. O build leva minutos.
		responder(w, http.StatusAccepted, map[string]any{"deploy_id": deployID})
	})

	mux.HandleFunc("GET /api/apps/{id}/deploys", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		lista, err := dp.listarDeploys(r.Context(), id)
		if err != nil {
			erroHTTP(w, http.StatusBadGateway, err.Error())
			return
		}
		if lista == nil {
			lista = []Deploy{}
		}
		responder(w, http.StatusOK, lista)
	})

	mux.HandleFunc("POST /api/apps/{id}/rollback/{deployId}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		alvoID, err := strconv.ParseInt(r.PathValue("deployId"), 10, 64)
		if err != nil {
			erroHTTP(w, http.StatusBadRequest, "deployId inválido")
			return
		}
		novoDeployID, err := dp.iniciarRollback(r.Context(), id, alvoID)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		responder(w, http.StatusAccepted, map[string]any{"deploy_id": novoDeployID})
	})

	mux.HandleFunc("GET /api/deploys/{id}/log", logAoVivo(dp))
}

// logAoVivo transmite o log do deploy por SSE enquanto o build roda.
// Unidirecional e sobre HTTP comum — WebSocket não se pagaria aqui.
func logAoVivo(dp *deploys) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			erroHTTP(w, http.StatusBadRequest, "id inválido")
			return
		}

		flush, ok := w.(http.Flusher)
		if !ok {
			erroHTTP(w, http.StatusInternalServerError, "streaming indisponível")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// O proxy do Zeno já manda proxy_buffering off, mas um proxy no meio
		// do caminho seguraria o stream sem este cabeçalho.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flush.Flush()

		caminho := dp.caminhoLog(id)
		var arquivo *os.File
		limite := time.Now().Add(10 * time.Second)

		// O arquivo aparece um instante depois do POST de deploy.
		for arquivo == nil && time.Now().Before(limite) {
			if f, err := os.Open(caminho); err == nil {
				arquivo = f
			} else if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(w, "event: erro\ndata: %s\n\n", err)
				flush.Flush()
				return
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
		if arquivo == nil {
			fmt.Fprintf(w, "event: erro\ndata: log do deploy %d não apareceu\n\n", id)
			flush.Flush()
			return
		}
		defer arquivo.Close()

		leitor := bufio.NewReader(arquivo)
		ocioso := 0

		for {
			select {
			case <-r.Context().Done():
				return // navegador fechou a aba
			default:
			}

			linha, err := leitor.ReadString('\n')
			if linha != "" {
				// Toda quebra de linha dentro do dado encerraria o evento SSE.
				fmt.Fprintf(w, "data: %s\n\n", limparSSE(linha))
				flush.Flush()
				ocioso = 0
				continue
			}

			if err != nil && err != io.EOF {
				return
			}

			// Sem dado novo: só encerra quando o deploy terminou E o arquivo
			// acabou. Encerrar só pelo status perderia as últimas linhas.
			status := dp.statusDeploy(id)
			if status != "building" {
				ocioso++
				if ocioso > 3 {
					fmt.Fprintf(w, "event: fim\ndata: %s\n\n", status)
					flush.Flush()
					return
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func (d *deploys) statusDeploy(id int64) string {
	var status string
	if err := d.db.QueryRow(`SELECT status FROM deploys WHERE id = ?`, id).Scan(&status); err != nil {
		return "desconhecido"
	}
	return status
}

// limparSSE tira o que quebraria o enquadramento do protocolo.
func limparSSE(s string) string {
	limpo := make([]rune, 0, len(s))
	for _, r := range s {
		if r != '\n' && r != '\r' {
			limpo = append(limpo, r)
		}
	}
	return string(limpo)
}
