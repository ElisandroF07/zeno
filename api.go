package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// erroHTTP responde erro em JSON no formato que a UI espera.
func erroHTTP(w http.ResponseWriter, status int, msg string) {
	responder(w, status, map[string]string{"erro": msg})
}

func lerJSON(w http.ResponseWriter, r *http.Request, destino any) bool {
	if err := json.NewDecoder(r.Body).Decode(destino); err != nil {
		erroHTTP(w, http.StatusBadRequest, "JSON inválido: "+err.Error())
		return false
	}
	return true
}

// --- presets e serviços -----------------------------------------------------

func rotasServicos(mux *http.ServeMux, sv *servicos) {
	mux.HandleFunc("GET /api/presets", func(w http.ResponseWriter, r *http.Request) {
		responder(w, http.StatusOK, presetsOrdenados(sv.presets))
	})

	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		lista, err := sv.listar(r.Context())
		if err != nil {
			erroHTTP(w, http.StatusBadGateway, err.Error())
			return
		}
		if lista == nil {
			lista = []Servico{}
		}
		responder(w, http.StatusOK, lista)
	})

	mux.HandleFunc("POST /api/services", func(w http.ResponseWriter, r *http.Request) {
		var corpo struct {
			PresetID string            `json:"preset_id"`
			Nome     string            `json:"nome"`
			Env      map[string]string `json:"env"`
		}
		if !lerJSON(w, r, &corpo) {
			return
		}
		s, err := sv.criar(r.Context(), corpo.PresetID, corpo.Nome, corpo.Env)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		responder(w, http.StatusCreated, s)
	})

	mux.HandleFunc("POST /api/services/{nome}/{acao}", func(w http.ResponseWriter, r *http.Request) {
		if err := sv.acao(r.Context(), r.PathValue("nome"), r.PathValue("acao")); err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		responder(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /api/services/{nome}", func(w http.ResponseWriter, r *http.Request) {
		if err := sv.remover(r.Context(), r.PathValue("nome")); err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Volume preservado de propósito: ver services.remover.
		responder(w, http.StatusOK, map[string]string{"status": "removido"})
	})
}

// --- editor do nginx --------------------------------------------------------

func rotasNginx(mux *http.ServeMux, ng *nginx, portaZeno string) {
	// Toda escrita passa por aqui: grava no banco, re-renderiza, aplica — e
	// desfaz a escrita se o nginx recusar.
	//
	// O `desfazer` não é zelo: o arquivo de config volta sozinho ao anterior,
	// mas a linha ficaria no banco. Como a config é re-renderizada do banco a
	// cada aplicação, uma linha inválida persistida faz TODA aplicação futura
	// falhar no mesmo erro — inclusive as legítimas. O usuário ficaria preso,
	// sem saída pela tela.
	aplicar := func(w http.ResponseWriter, r *http.Request, desfazer func()) bool {
		if err := ng.sincronizar(r.Context(), portaZeno); err != nil {
			if desfazer != nil {
				desfazer()
			}
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return false
		}
		return true
	}

	// Remoção nunca produz config inválida (vhost sem location e arquivo
	// vazio são válidos), então os DELETE passam desfazer = nil.

	mux.HandleFunc("GET /api/nginx/servers", func(w http.ResponseWriter, r *http.Request) {
		gerados, err := ng.carregarServers(true)
		if err != nil {
			erroHTTP(w, http.StatusInternalServerError, err.Error())
			return
		}
		doUsuario, err := ng.carregarServers(false)
		if err != nil {
			erroHTTP(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = ng.marcarCertsProntos(gerados)
		_ = ng.marcarCertsProntos(doUsuario)
		responder(w, http.StatusOK, map[string]any{
			"gerados":      ouVazio(gerados),
			"do_usuario":   ouVazio(doUsuario),
			"conf_gerada":  renderConf(gerados, portaZeno),
			"conf_usuario": renderConf(doUsuario, portaZeno),
		})
	})

	mux.HandleFunc("POST /api/nginx/servers", func(w http.ResponseWriter, r *http.Request) {
		var corpo struct {
			ServerName string `json:"server_name"`
			SSL        bool   `json:"ssl"`
		}
		if !lerJSON(w, r, &corpo) {
			return
		}
		if corpo.ServerName == "" {
			erroHTTP(w, http.StatusBadRequest, "server_name é obrigatório")
			return
		}
		res, err := ng.db.Exec(
			`INSERT INTO nginx_servers (server_name, ssl, gerado) VALUES (?, ?, 0)`,
			corpo.ServerName, corpo.SSL)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		id, _ := res.LastInsertId()
		if !aplicar(w, r, func() {
			_, _ = ng.db.Exec(`DELETE FROM nginx_servers WHERE id = ?`, id)
		}) {
			return
		}
		responder(w, http.StatusCreated, map[string]any{"id": id})
	})

	mux.HandleFunc("DELETE /api/nginx/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		if !editavel(w, ng, id) {
			return
		}
		if _, err := ng.db.Exec(`DELETE FROM nginx_servers WHERE id = ?`, id); err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if !aplicar(w, r, nil) {
			return
		}
		responder(w, http.StatusOK, map[string]string{"status": "removido"})
	})

	mux.HandleFunc("POST /api/nginx/servers/{id}/locations", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		if !editavel(w, ng, id) {
			return
		}
		var l nginxLocation
		if !lerJSON(w, r, &l) {
			return
		}
		if l.Path == "" || l.Alvo == "" {
			erroHTTP(w, http.StatusBadRequest, "path e alvo são obrigatórios")
			return
		}
		if l.Tipo == "" {
			l.Tipo = "proxy"
		}
		res, err := ng.db.Exec(
			`INSERT INTO nginx_locations (server_id, path, tipo, alvo, dinamico, stream)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, l.Path, l.Tipo, l.Alvo, l.Dinamico, l.Stream)
		if err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		locID, _ := res.LastInsertId()
		if !aplicar(w, r, func() {
			_, _ = ng.db.Exec(`DELETE FROM nginx_locations WHERE id = ?`, locID)
		}) {
			return
		}
		responder(w, http.StatusCreated, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /api/nginx/locations/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := idDaRota(w, r)
		if !ok {
			return
		}
		var serverID int64
		if err := ng.db.QueryRow(
			`SELECT server_id FROM nginx_locations WHERE id = ?`, id).Scan(&serverID); err != nil {
			erroHTTP(w, http.StatusNotFound, "location não existe")
			return
		}
		if !editavel(w, ng, serverID) {
			return
		}
		if _, err := ng.db.Exec(`DELETE FROM nginx_locations WHERE id = ?`, id); err != nil {
			erroHTTP(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if !aplicar(w, r, nil) {
			return
		}
		responder(w, http.StatusOK, map[string]string{"status": "removido"})
	})
}

// editavel barra escrita nos vhosts que o Zeno gera — eles são reescritos a
// cada sincronização, então editar ali seria trabalho perdido em silêncio.
func editavel(w http.ResponseWriter, ng *nginx, serverID int64) bool {
	var gerado bool
	if err := ng.db.QueryRow(
		`SELECT gerado FROM nginx_servers WHERE id = ?`, serverID).Scan(&gerado); err != nil {
		erroHTTP(w, http.StatusNotFound, "server não existe")
		return false
	}
	if gerado {
		erroHTTP(w, http.StatusConflict,
			"esse vhost é gerado pelo Zeno e é reescrito a cada deploy — crie um próprio")
		return false
	}
	return true
}

func idDaRota(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		erroHTTP(w, http.StatusBadRequest, "id inválido")
		return 0, false
	}
	return id, true
}

// JSON com null no lugar de lista vazia obriga a UI a checar antes de mapear.
func ouVazio(s []nginxServer) []nginxServer {
	if s == nil {
		return []nginxServer{}
	}
	return s
}
