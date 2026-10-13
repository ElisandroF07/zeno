package main

import "net/http"

func rotasGithub(mux *http.ServeMux, gh *github) {
	// GET, não POST: é um link/redirect de navegador ("Conectar com
	// GitHub"), não uma chamada de API feita por fetch.
	mux.HandleFunc("GET /api/github/login", gh.handlerLogin)
	mux.HandleFunc("GET /api/github/callback", gh.handlerCallback)

	mux.HandleFunc("GET /api/github/status", func(w http.ResponseWriter, r *http.Request) {
		conectado, usuario := gh.status()
		responder(w, http.StatusOK, map[string]any{
			"conectado":   conectado,
			"usuario":     usuario,
			"configurado": gh.configurado(),
		})
	})

	mux.HandleFunc("POST /api/github/desconectar", func(w http.ResponseWriter, r *http.Request) {
		if err := gh.desconectar(); err != nil {
			erroHTTP(w, http.StatusInternalServerError, err.Error())
			return
		}
		responder(w, http.StatusOK, map[string]string{"status": "desconectado"})
	})

	mux.HandleFunc("GET /api/github/repos", func(w http.ResponseWriter, r *http.Request) {
		repos, err := gh.listarRepos(r.Context())
		if err != nil {
			erroHTTP(w, http.StatusBadGateway, err.Error())
			return
		}
		if repos == nil {
			repos = []RepoGithub{}
		}
		responder(w, http.StatusOK, repos)
	})

	mux.HandleFunc("GET /api/github/repos/{dono}/{repo}/branches", func(w http.ResponseWriter, r *http.Request) {
		nomeCompleto := r.PathValue("dono") + "/" + r.PathValue("repo")
		branches, err := gh.listarBranches(r.Context(), nomeCompleto)
		if err != nil {
			erroHTTP(w, http.StatusBadGateway, err.Error())
			return
		}
		if branches == nil {
			branches = []string{}
		}
		responder(w, http.StatusOK, branches)
	})
}
