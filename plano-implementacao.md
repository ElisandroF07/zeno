# Zeno — plano de implementação

Plataforma de hospedagem self-hosted: sobe infra completa em Docker numa VPS,
com presets prontos e interface de gestão para os serviços.

## 1. Objetivo

Um **binário único** que roda numa VPS e transforma ela numa plataforma de
deploy: sobe apps (lê o projeto e descobre sozinho como buildar) e sobe
serviços de infra (Redis, RabbitMQ, Kafka, Postgres, MinIO...) com um clique —
cada um com tela de gestão, sem nunca editar arquivo de config via SSH.

O **nginx é o proxy de borda**, gerido inteiramente pela UI: servers,
locations, redirects e certificados.

Sem autenticação e sem multi-tenancy na v1 (rede privada / VPN / allowlist de
IP). O objetivo é provar o fluxo completo antes de investir em escala.

## 2. Escopo da v1

**Dentro:**
- Deploy de apps: git push (webhook) ou botão manual
- Detecção automática do projeto (Dockerfile → Nixpacks → preset manual)
- Catálogo de presets de serviço, orientado a dados
- UI de gestão por serviço (3 níveis, ver seção 6)
- nginx gerido pela UI + SSL automático
- Env vars, logs ao vivo, start/stop/restart, histórico e rollback
- Dashboard com saúde do host

**Fora (ver seção 12):** autenticação/RBAC, multi-tenancy, Nomad/Kubernetes,
HA do control plane, múltiplas VPSs.

## 3. Stack

| Camada | Escolha | Justificativa |
|---|---|---|
| Control plane | **Go** | Binário estático único, zero runtime na VPS |
| Roteamento HTTP | **`net/http` (Go 1.22+)** | Padrões de rota com método e path params já estão na stdlib — sem chi/gin |
| Frontend | **React + Vite + Tailwind + shadcn/ui** | Pedido explícito; interface 100% componível |
| Empacotamento do front | **`go:embed web/dist`** | Build de JS vira asset embutido — continua **um binário só** |
| Realtime | **SSE** (`EventSource` nativo) | Logs e status são unidirecionais; WebSocket não se paga aqui |
| Banco | **SQLite via `modernc.org/sqlite`** | Driver **puro Go**: mantém o cross-compile estático (o `mattn/go-sqlite3` exige cgo e quebra essa promessa) |
| Build de apps | **Nixpacks** (binário externo) | Detecta o stack sem Dockerfile; não reimplementar |
| Proxy de borda | **nginx** (container, config gerada pela UI) | Escolha do projeto; roteamento e TLS num lugar só |
| Certificados | **`github.com/go-acme/lego`** como **lib no binário** | É a mesma lib que o Traefik usa por dentro. Emite o cert, escreve o `.pem` no volume do nginx, dá reload — sem certbot, sem cron, sem container extra |
| Containers | **Docker Engine SDK** (`github.com/moby/moby/client`) | Mais robusto que shell-out. Atenção: o import antigo `github.com/docker/docker/client` foi substituído por este, com `client.New(...)` e resultados em `.Items` |

## 4. Arquitetura

```
Browser (React/shadcn)
   │ JSON API + SSE
   ▼
┌───────────────────────────────────┐
│  binário Go                       │
│  ├─ API + UI embutida             │
│  ├─ detector de projeto           │
│  ├─ motor de build/deploy         │
│  ├─ engine de presets (YAML)      │
│  ├─ gerador de config do nginx    │
│  ├─ ACME (lego) + renovação       │
│  └─ SQLite                        │
└───┬───────────────────────────┬───┘
    │ Docker SDK                │ escreve conf.d/ + certs, `nginx -s reload`
    ▼                           ▼
 containers (apps+serviços)   nginx ──► internet :80/:443
    └──────── rede `zeno` ──────┘
```

Um processo, uma VPS, nada distribuído. Todos os containers entram na rede
`zeno`, e o nginx alcança cada um pelo nome do container via DNS do Docker.

## 5. Presets são dados, não código

Esta é a decisão que faz o catálogo escalar. Um preset é um arquivo — para
adicionar Kafka, escreve-se um YAML, não um módulo Go.

```yaml
# presets/rabbitmq.yaml
id: rabbitmq
nome: RabbitMQ
categoria: mensageria
image: rabbitmq:3-management
ports: [5672]
volumes:
  - /var/lib/rabbitmq
env:
  - { key: RABBITMQ_DEFAULT_USER, default: admin }
  - { key: RABBITMQ_DEFAULT_PASS, generate: password }
healthcheck: ["CMD", "rabbitmq-diagnostics", "ping"]

admin_ui:                 # console oficial do próprio serviço
  port: 15672

injeta:                   # vai para as apps vinculadas
  AMQP_URL: "amqp://{{.User}}:{{.Pass}}@{{.Host}}:5672/"
```

Presets embutidos via `go:embed presets/*.yaml`, com diretório de override
(`/etc/zeno/presets/`) para o usuário adicionar os dele sem recompilar.

O formulário de criação do serviço é **gerado a partir do bloco `env`** — um
renderer só, para todos os serviços.

## 6. UI de gestão — três níveis

Reimplementar uma tela de gestão para cada serviço é o caminho para não
terminar nunca. Três níveis, do barato ao caro:

**Nível 0 — genérico (cobre todo serviço, custo zero por serviço).**
Env vars, volumes, portas, start/stop/restart, logs ao vivo, CPU/RAM,
healthcheck. Vem de graça do preset.

**Nível 1 — console oficial embutido (uma linha no preset).**
A maioria dos serviços já traz uma UI melhor do que a que eu escreveria:

| Serviço | Console |
|---|---|
| RabbitMQ | management plugin (:15672) |
| MinIO | console (:9001) |
| Kafka | sidecar `redpanda-console` / `kafka-ui` |
| Postgres / MySQL | sidecar `adminer` |
| Redis | sidecar `redisinsight` |

Zeno sobe o console junto, cria a rota no nginx e mostra embutido no painel.
Custo: `admin_ui.port` no YAML. **Não escrever UI própria onde já existe uma.**

**Nível 2 — gestor dedicado (só onde não existe console).**
Hoje isso é o **nginx** — e, como ele é a borda, está no caminho crítico.
Ver seção 7.

## 7. Gestor do nginx

O nginx nunca é editado à mão. A UI escreve num modelo estruturado no SQLite
(`servers → locations → proxy_pass | redirect | static | headers`) e o binário
renderiza o conf a partir dele.

**Duas fontes de config, arquivos separados:**

```
conf.d/
  zeno-apps.conf    ← 100% gerado das tabelas apps/domains. UI não edita.
  user-*.conf       ← editado na UI (nível 2): vhosts e regras próprias.
```

Isso permite ter roteamento de apps funcionando **antes** do editor visual
existir: mesmo renderer, metade do trabalho. O editor vem depois e reaproveita
tudo.

**O fluxo de aplicação, sempre igual:**

```
altera (deploy ou edição na UI)
  → renderiza o conf num arquivo temporário
  → `nginx -t` DENTRO do container
      ├─ ok    → move, `nginx -s reload`, grava a versão
      └─ falha → devolve o erro apontando o campo, config antiga segue no ar
```

Nunca se escreve config inválida em produção, e quem valida é o próprio nginx
— não um parser meu. Cada versão aplicada fica no histórico, com rollback.

**Upstreams: usar resolver, não nome estático.** O nginx resolve o upstream uma
vez no boot e cacheia o IP. Como todo deploy recria o container com IP novo,
config estática vira 502 silencioso. A forma correta:

```nginx
resolver 127.0.0.11 valid=10s;          # DNS embutido do Docker
location / {
  set $upstream http://app-minhaapp:3000;   # variável força resolução em runtime
  proxy_pass $upstream;
}
```

Com isso o deploy fica: sobe container novo → espera healthcheck → reload →
derruba o antigo. Sem janela de 502.

**Certificados.** Cada `domain` cadastrado dispara emissão via `lego`
(HTTP-01, servido pelo próprio nginx num location `/.well-known/acme-challenge`).
Uma goroutine acorda uma vez por dia, renova o que estiver a menos de 30 dias
do vencimento e recarrega o nginx. Certs num volume, não no banco.

## 8. Detecção automática de projeto

Cadeia de detectores; cada um lê um ou dois arquivos e devolve um palpite:

| Encontra | Deduz |
|---|---|
| `Dockerfile` / `docker-compose.yml` | usa direto, não adivinha nada |
| `package.json` | dep `next` → Next.js; `vite`/`react-scripts` → SPA estática; senão Node |
| `pom.xml` / `build.gradle` | Spring Boot; porta lida de `application.properties`/`.yml` |
| `requirements.txt` / `pyproject.toml` | Django / Flask / FastAPI pela dependência |
| `go.mod`, `Gemfile`, `composer.json` | Go, Rails, PHP |
| só `index.html` | site estático |

O detector **não** builda — quem builda é o Nixpacks. Ele existe só para
pré-preencher a tela (comando de build, comando de start, porta, env
esperadas) com tudo editável. Ordem final: Dockerfile → Nixpacks → preset
manual.

## 9. Modelo de dados

```sql
apps            (id, nome, repo_url, branch, build_strategy, porta, created_at)
app_env         (app_id, chave, valor, is_secret)
domains         (id, app_id, hostname, redirect_para)

deploys         (id, app_id, imagem_tag, status, log_path, env_snapshot, commit_sha, created_at)
                -- log em arquivo, não em coluna: log de build passa de MB
                -- env_snapshot: sem ele, rollback volta a imagem antiga com env nova e quebra

services        (id, preset_id, nome, config_json, volume_name, status, created_at)
app_services    (app_id, service_id)   -- vínculo N:N; gera as env injetadas

certs           (hostname, cert_path, key_path, emitido_em, expira_em, ultimo_erro)

nginx_servers   (id, server_name, ssl, gerado)   -- gerado=1: pertence ao Zeno, UI não edita
nginx_locations (id, server_id, path, tipo, alvo, extras_json)
nginx_versions  (id, arquivo, conf, aplicado_em, ok, erro)   -- histórico + rollback
```

## 10. Roadmap

Como o nginx é a borda, ele sobe antes do primeiro deploy — mas só a metade
gerada (seção 7), não o editor visual.

1. ✅ **Fundação** — binário Go, SQLite, React/shadcn embutido, Docker SDK, lista de containers na tela.
2. ✅ **nginx + renderer + reload** — sobe o container nginx na rede `zeno`, renderiza `zeno-apps.conf`, valida com `nginx -t`, recarrega. Roteia a própria UI do Zeno como primeiro caso de teste.
3. ✅ **Engine de presets** — parser do YAML, formulário gerado, subir um serviço (Redis primeiro: sem estado, sem console).
4. ✅ **Ciclo de deploy** — clone, Nixpacks, tag de imagem, container novo → healthcheck → reload → derruba o antigo, log via SSE.
5. ✅ **Domínios + ACME** — cadastro de domínio, emissão via lego, renovação diária.
6. ✅ **Histórico + rollback** — troca de imagem sem rebuild, com `env_snapshot`.
7. ✅ **Catálogo cheio + consoles (nível 1)** — 43 presets, muito além dos 7 originais (bancos, cache, mensageria, dev tools, monitoramento, mídia, produtividade, CMS, IA/vector DB, IoT), com admin embutido onde existe console oficial.
8. ✅ **Editor visual do nginx (nível 2)** — servers, locations, redirects, versionamento; rollback de config ainda não tem UI dedicada (dá para reaplicar uma versão antiga só via banco).
9. ✅ **Detector de projeto** — clona raso, lê Dockerfile/package.json/pom.xml/requirements.txt/go.mod/Gemfile/composer.json/index.html, pré-preenche framework + porta na tela. Dockerfile sempre ganha, mesmo critério do build de verdade.
10. ✅ **Instalador** — `install.sh` pronto, testado em container Ubuntu limpo (root/arch/dependência-já-instalada/erro-de-release, todos com lógica real). Falta o pipeline de release (CI multi-arch) que publica os binários que o script baixa — sem isso, o comando `curl | bash` não tem o que buscar ainda.
11. ⬜ **Dogfooding** — migrar projetos reais antes de qualquer outra pessoa usar.

## 11. Riscos e decisões

- **Sem autenticação na v1.** A UI controla o Docker do host — quem a alcança é root da VPS na prática. VPN/Tailscale ou `allow`/`deny` no nginx é **obrigatório**, e precisa estar no topo do README, não numa nota de rodapé.
- **nginx na borda = SSL não vem de graça.** Resolvido com `lego` como lib, não com certbot + cron. Se a emissão falhar, o domínio precisa ficar visível como "sem cert" na UI, servindo HTTP — nunca derrubar o vhost inteiro.
- **Rate limit do Let's Encrypt** (50 certs/domínio/semana, 5 duplicados/semana). Testar sempre contra o endpoint de staging; um loop de retry mal feito queima a cota por uma semana inteira.
- **Reload, nunca restart.** `nginx -s reload` não derruba conexões; `docker restart` derruba todos os vhosts do host por causa de um deploy.
- **Nixpacks é binário externo (Rust), não lib Go.** O instalador baixa os dois; falta dele no PATH tem que virar erro claro na UI, não stack trace.
- **Driver SQLite puro Go é inegociável** se o binário estático único for para valer.
- **SQLite não escala para múltiplas instâncias** — aceitável com uma VPS; é a primeira peça a trocar se multi-tenancy voltar.
- **Consoles de nível 1 herdam a exposição da UI.** O adminer do Postgres na internet aberta é um banco na internet aberta — as rotas de admin ficam atrás da mesma restrição de rede da UI.
- **Nada de interface de driver com uma implementação só.** A abstração `deploy/scale/getStatus` nasce quando o segundo motor (Nomad) chegar, não antes.

## 12. Pós-v1

Multi-tenancy com isolamento de rede e quotas · autenticação e RBAC (OIDC) ·
drivers plugáveis (Docker → Nomad → Kubernetes) · HA do control plane
(Postgres + réplicas) · gestão de múltiplas VPSs · backup agendado de volumes.
