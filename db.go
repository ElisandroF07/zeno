package main

import (
	"database/sql"
	"net/url"

	_ "modernc.org/sqlite"
)

// ponytail: schema aplicado direto no boot, sem lib de migration. Trocar por
// golang-migrate no dia em que existir banco em produção a preservar.
const schema = `
CREATE TABLE IF NOT EXISTS apps (
  id             INTEGER PRIMARY KEY,
  nome           TEXT NOT NULL UNIQUE,
  repo_url       TEXT NOT NULL,
  branch         TEXT NOT NULL DEFAULT 'main',
  build_strategy TEXT NOT NULL DEFAULT 'auto',
  porta          INTEGER NOT NULL DEFAULT 3000,
  container_atual TEXT,   -- versionado por deploy: é o que o nginx aponta
  created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS app_env (
  app_id    INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  chave     TEXT NOT NULL,
  valor     TEXT NOT NULL,
  is_secret INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (app_id, chave)
);

CREATE TABLE IF NOT EXISTS domains (
  id            INTEGER PRIMARY KEY,
  app_id        INTEGER REFERENCES apps(id) ON DELETE CASCADE,
  hostname      TEXT NOT NULL UNIQUE,
  redirect_para TEXT
);

CREATE TABLE IF NOT EXISTS deploys (
  id           INTEGER PRIMARY KEY,
  app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  imagem_tag   TEXT NOT NULL,
  status       TEXT NOT NULL,          -- building | success | failed
  log_path     TEXT,                   -- arquivo: log de build passa de MB
  env_snapshot TEXT,                   -- JSON; sem isso, rollback quebra
  commit_sha   TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_deploys_app ON deploys(app_id, created_at DESC);

CREATE TABLE IF NOT EXISTS services (
  id          INTEGER PRIMARY KEY,
  preset_id   TEXT NOT NULL,           -- arquivo em presets/<id>.yaml
  nome        TEXT NOT NULL UNIQUE,
  config_json TEXT NOT NULL DEFAULT '{}',
  volume_name TEXT,
  status      TEXT NOT NULL DEFAULT 'criado',
  created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS app_services (
  app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  PRIMARY KEY (app_id, service_id)
);

-- PEM guardado aqui, não só o caminho: o container do nginx pode ser
-- recriado (rebuild, docker rm), e sem o conteúdo persistido o Zeno teria
-- que reemitir certificado — o que esbarra direto no rate limit da CA.
-- No boot, o PEM do banco é regravado no container antes de qualquer coisa.
CREATE TABLE IF NOT EXISTS certs (
  hostname    TEXT PRIMARY KEY,
  cert_pem    TEXT NOT NULL,
  key_pem     TEXT NOT NULL,
  emitido_em  TEXT NOT NULL,
  expira_em   TEXT NOT NULL,
  ultimo_erro TEXT
);

-- Config de singleton (conta ACME, e-mail). Não é preset nem app nem
-- serviço — não cabe em nenhuma tabela existente, e criar uma tabela por
-- flag seria pior que um key-value só para isso.
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS nginx_servers (
  id          INTEGER PRIMARY KEY,
  server_name TEXT NOT NULL,
  ssl         INTEGER NOT NULL DEFAULT 0,
  gerado      INTEGER NOT NULL DEFAULT 0,  -- 1 = dono é o Zeno, UI não edita
  padrao      INTEGER NOT NULL DEFAULT 0   -- default_server: recebe o que não casa
);
-- Exatamente um default_server por porta, ou o nginx nem sobe.
CREATE UNIQUE INDEX IF NOT EXISTS idx_nginx_um_padrao ON nginx_servers(padrao) WHERE padrao = 1;

CREATE TABLE IF NOT EXISTS nginx_locations (
  id          INTEGER PRIMARY KEY,
  server_id   INTEGER NOT NULL REFERENCES nginx_servers(id) ON DELETE CASCADE,
  path        TEXT NOT NULL,
  tipo        TEXT NOT NULL,           -- proxy | redirect | static
  alvo        TEXT NOT NULL,
  dinamico    INTEGER NOT NULL DEFAULT 0,  -- resolve o upstream a cada request
  stream      INTEGER NOT NULL DEFAULT 0,  -- desliga buffering (SSE/websocket)
  extras_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS nginx_versions (
  id          INTEGER PRIMARY KEY,
  arquivo     TEXT NOT NULL,
  conf        TEXT NOT NULL,
  aplicado_em TEXT NOT NULL DEFAULT (datetime('now')),
  ok          INTEGER NOT NULL,
  erro        TEXT
);
`

func abrirDB(caminho string) (*sql.DB, error) {
	// busy_timeout evita "database is locked" quando o reload do nginx e um
	// deploy escrevem ao mesmo tempo; WAL deixa leitura e escrita concorrerem.
	dsn := "file:" + caminho + "?" + url.Values{"_pragma": {
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"foreign_keys(ON)",
	}}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
