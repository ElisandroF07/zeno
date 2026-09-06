# Zeno

Plataforma de deploy self-hosted: um binário, uma VPS, Docker embaixo.
Ver [plano-implementacao.md](plano-implementacao.md).

> **A UI não tem autenticação e controla o Docker do host.** Quem alcança a
> porta é root da máquina, na prática. Só exponha atrás de VPN/Tailscale ou
> allowlist de IP.

## Rodar

Precisa de Go 1.25+, Node 20+ e Docker rodando.

```bash
cd web && npm install && npm run build && cd ..   # gera web/dist (go:embed exige)
go mod tidy
go run .                                          # http://localhost:8080
```

O front é embutido no binário, então **toda mudança em `web/` precisa de
`npm run build`** antes do `go build`. Durante o desenvolvimento do front, use
os dois processos: `go run .` em um terminal e `npm run dev` em `web/` no
outro (o Vite faz proxy de `/api` para :8080).

**Se o projeto estiver dentro de uma pasta sincronizada pelo OneDrive**
(comum no Windows, ex. `OneDrive\Documentos\...`), dois problemas aparecem e
não são bug do Zeno:

- `go build -o zeno.exe .` falha (`open zeno.exe: ...`) — o Go grava num
  arquivo temporário e faz rename atômico; o OneDrive intercepta o arquivo no
  meio da operação. `go run .` funciona normalmente (não passa por esse
  rename), então isso só afeta quem precisa do binário compilado.
- **O banco não abre** (`sqlite: unable to open database file`) — o SQLite
  roda em modo WAL (`db.go`), que exige criar arquivos `-wal`/`-shm` ao lado
  do `.db`; o OneDrive bloqueia isso. Contorne apontando `ZENO_DB` para fora
  da pasta sincronizada:
  ```bash
  ZENO_DB=/tmp/zeno.db go run .           # Linux/macOS
  $env:ZENO_DB="$env:TEMP\zeno.db"; go run .   # PowerShell
  ```

Se os dois problemas incomodarem, a solução definitiva é mover o projeto
para fora de qualquer pasta sincronizada (`C:\dev\zeno`, por exemplo) — aí
nenhum dos dois contornos é necessário.

No primeiro boot o Zeno cria a rede `zeno`, puxa a imagem do nginx e sobe o
container `zeno-nginx` roteando a própria UI. Isso roda em background: o
servidor responde na hora e a tela mostra a fase.

## Presets

43 serviços prontos em `presets/*.yaml`, embutidos no binário — bancos,
cache, mensageria, dev tools, monitoramento, mídia, produtividade, CMS,
IA/vector DB, IoT. Adicionar um serviço novo é escrever um arquivo, não
recompilar — e `ZENO_PRESETS` aponta para um diretório que sobrepõe os
embutidos sem tocar no binário.

Alguns exigem outro serviço já rodando (WordPress e Ghost precisam de
MySQL/MariaDB, Wekan precisa de MongoDB, Paperless-ngx precisa de Redis) —
a descrição do preset avisa, e os campos de conexão ficam para o usuário
preencher com o que o outro serviço expõe. Não há hoje um jeito de a UI
ligar os dois sozinha; isso é trabalho para uma futura tela de "vincular
serviços" nos moldes do vínculo app↔serviço que já existe.

`docker_socket: true` num preset monta `/var/run/docker.sock` no container
— só para ferramentas cujo propósito é gerir o Docker do host (Portainer,
Dockge). Isso dá a esse container acesso equivalente a root no host: é o
motivo de essas ferramentas existirem, mas também o motivo de nunca marcar
essa flag num preset que não precise dela de verdade.

```yaml
id: redis
image: redis:8-alpine       # imagem real e fixada
porta: 6379
env:                        # vira o formulário da tela
  - key: REDIS_PASSWORD
    label: Senha
    tipo: senha
    gerar: senha            # em branco = gerada com crypto/rand
cmd: ["redis-server", "--requirepass", "{{.Env.REDIS_PASSWORD}}"]
admin_ui: { porta: 15672 }  # console próprio do serviço, se houver
injeta:                     # vai para as apps vinculadas
  REDIS_URL: "redis://:{{.Env.REDIS_PASSWORD}}@{{.Host}}:6379"
```

**Portas.** Só a `admin_ui` sai para o host, presa em `127.0.0.1`. A porta de
dados fica apenas na rede `zeno`: banco publicado no IP público da VPS é banco
na internet aberta. Para alcançar um console de fora, use túnel SSH.

**Volumes** não são apagados ao remover um serviço. Apagar dado de banco por
clique de botão é o tipo de coisa que não se desfaz.

## Instalação numa VPS

```bash
curl -fsSL https://github.com/f07nobru/zeno/releases/latest/download/install.sh | sudo bash
```

O `install.sh` (na raiz do repo) garante Docker e Nixpacks (via instaladores
oficiais deles — `get.docker.com` e `nixpacks.com/install.sh`, não
reimplementados aqui), baixa o binário certo pra arquitetura (amd64/arm64) e
sobe como serviço systemd (`zeno.service`), reiniciando sozinho se cair.
Rodar de novo atualiza — é idempotente, detecta o que já está instalado e
pula.

**Isso pressupõe uma release publicada** em
`github.com/f07nobru/zeno/releases` com os binários `zeno-linux-amd64` e
`zeno-linux-arm64` — o workflow que gera e publica isso já existe
(`.github/workflows/release.yml`), mas só roda quando o repositório for
empurrado pro GitHub e uma tag `vX.Y.Z` for criada; até lá, `curl | bash`
não tem o que baixar. `ZENO_RELEASE_BASE` permite apontar para outro mirror
(ou servidor local) enquanto isso.

## Pipeline de release

`git push` de uma tag `v*.*.*` (ex. `v0.1.0`) dispara o workflow: builda o
front, cross-compila o binário puro-Go para `linux/amd64` e `linux/arm64`
(`CGO_ENABLED=0` — é o motivo de `modernc.org/sqlite` em vez de
`mattn/go-sqlite3`: sem isso essa cross-compilação nem seria possível sem
toolchain C), embute a própria versão no binário via
`-ldflags -X main.versao=$tag` (aparece no primeiro log, `zeno vX.Y.Z`), e
publica os dois binários + `install.sh` + checksums como release usando o
`gh` CLI que já vem no runner — sem depender de action de terceiro só para
isso.

**Testado**: `actionlint` (que roda `shellcheck` nos blocos `run:`) limpo
contra o workflow. Os dois binários foram cross-compilados aqui de verdade
com os mesmos comandos do workflow, confirmados como ELF válido, e cada um
rodou de ponta a ponta dentro de um container Ubuntu (o arm64 via emulação
QEMU) — subiu, carregou os 43 presets, serviu a UI e a API. O que não dá
para testar sem repositório remoto: o workflow em si rodando no GitHub
Actions (precisa do push) e o `gh release create` de verdade.

**Testado**: as quatro checagens centrais do script — recusa sem root,
recusa arquitetura desconhecida, mensagem clara quando a release não existe,
e o caminho de reexecução (Docker/Nixpacks já instalados) — rodaram de
verdade dentro de um container Ubuntu limpo, com download real via HTTP de
um binário fake servido localmente. `shellcheck` limpo. O que não dá para
testar sem uma VPS de verdade: a instalação real do Docker/Nixpacks (delegada
aos instaladores oficiais deles) e o systemd de fato gerenciando o processo
(um container Docker não roda systemd como PID 1).

## Detector de projeto

`POST /api/apps/detectar` (`{repo_url, branch}`) clona raso, lê um punhado de
arquivos conhecidos (Dockerfile, `package.json`, `pom.xml`/`build.gradle`,
`requirements.txt`/`pyproject.toml`, `go.mod`, `Gemfile`, `composer.json`,
`index.html`) e devolve um palpite — framework, porta sugerida, comentário —
para pré-preencher a tela de criação de app. O clone morre com a chamada;
nada é guardado, nada é buildado. Dockerfile sempre ganha de qualquer
palpite, o mesmo critério que `detectarEstrategia` usa de verdade no deploy
(seção anterior) — os dois nunca podem divergir.

## Deploy de apps

`POST /api/apps` cadastra (repo + branch + porta que a app escuta + domínio
opcional). `POST /api/apps/{id}/deploy` dispara em background e devolve na
hora — acompanhe pelo log ao vivo em `GET /api/deploys/{id}/log` (SSE).

Ciclo de cada deploy: clona raso → Dockerfile do projeto ganha, senão cai no
Nixpacks → tag única por commit → sobe o container novo **ao lado** do atual
→ espera a porta responder → só então aponta o nginx para ele → remove o
antigo. Se qualquer etapa falhar, a versão em produção nunca sai do ar — o
container novo (se chegou a subir) é descartado e o log mostra onde quebrou.

**Histórico e rollback.** `GET /api/apps/{id}/deploys` lista os deploys mais
recentes primeiro, com o que está no ar marcado. `POST
/api/apps/{id}/rollback/{deployId}` volta para um deploy que já funcionou —
reaproveitando a imagem já buildada (tag por commit, nunca apagada), sem
clonar nem buildar de novo. Usa o mesmo caminho de troca sem downtime do
deploy normal, e guarda o `env_snapshot` de cada deploy para recriar o
container com o env exato daquele momento, não o de hoje.

## SSL automático

Cada domínio (de app ou vhost do usuário com o toggle SSL ligado) recebe
certificado via HTTP-01 emitido pelo `go-acme/lego` — sem certbot, sem
container extra. O desafio é servido pelo próprio processo do Zeno em
`GET /.well-known/acme-challenge/{token}`; o nginx só faz proxy desse path
para `host.docker.internal:<porta do Zeno>`, o mesmo endereço que já serve a
UI. Sem novo listener, sem novo container.

O certificado (PEM completo, não só o caminho) é gravado no SQLite. Se o
container do nginx for recriado, o Zeno reescreve o PEM nele antes de
qualquer coisa — sem isso, cada recriação pediria certificado de novo e
esbarraria direto no rate limit da CA.

**Variáveis:**

| Var | Padrão | O quê |
|---|---|---|
| `ZENO_ACME_EMAIL` | vazio | e-mail da conta ACME |
| `ZENO_ACME_STAGING` | vazio | qualquer valor = usa o endpoint de staging do Let's Encrypt |
| `ZENO_ACME_DIRECTORY_URL` | produção | sobrepõe staging; usado para apontar num ACME de teste (Pebble) |
| `ZENO_ACME_INSECURE_TLS` | vazio | pula verificação de TLS do endpoint ACME — só para Pebble local, recusado se `DIRECTORY_URL` for a produção |

**Testado de ponta a ponta contra [Pebble](https://github.com/letsencrypt/pebble)**
(servidor ACME de teste da própria Let's Encrypt), não só com mock: conta
registrada, desafio HTTP-01 resolvido pelo nginx de verdade, certificado
emitido e servido em HTTPS com a cadeia validada contra a CA raiz do Pebble,
e rehidratado sem reemissão depois de destruir e recriar o container do
nginx. HTTP-01 exige que a CA alcance o host publicamente — não é possível
testar contra a Let's Encrypt real sem domínio público, por isso o Pebble.

**Risco não coberto ainda:** não há backoff entre tentativas falhas. Um
domínio mal configurado (DNS não aponta para o host, porta 80 bloqueada)
tenta emitir de novo a cada `sincronizar()` — e o Let's Encrypt real limita a
5 falhas de validação por hora por hostname. Antes de apontar em produção,
confirme que o domínio já resolve para a VPS antes de ligar o toggle SSL.

## Testes

```bash
go test ./...                                                    # unitários
ZENO_TESTE_DOCKER=1 ZENO_TESTE_HTTP_PORT=8081 go test -run Integracao -timeout 6m
                                                                   # com Docker + zeno-nginx no ar
```

Os testes de integração cobrem as duas garantias do ciclo de deploy: troca de
versão sem perder request (sonda em loop durante a troca) e Dockerfile
quebrado não derruba o container que está servindo.

Os testes de ACME (`acme_test.go`) rodam sem rede — desafio HTTP-01,
persistência da conta e a trava contra emissão duplicada em paralelo. A
emissão de verdade contra Pebble foi validada manualmente (ver seção acima),
não é automatizada: precisa do container do Pebble no ar com config própria
(`httpPort: 80`, `-dnsserver 127.0.0.11:53`) para resolver o domínio de teste
pela rede `zeno`.

## Variáveis

| Var | Padrão | O quê |
|---|---|---|
| `ZENO_ADDR` | `:8080` | endereço do servidor |
| `ZENO_DB` | `zeno.db` | arquivo SQLite |
| `ZENO_PRESETS` | `/etc/zeno/presets` | presets que sobrepõem os embutidos |
| `ZENO_LOGS` | `logs` | diretório dos logs de deploy |
| `ZENO_HTTP_PORT` | `80` | porta do host para o nginx (use outra em dev) |
| `ZENO_HTTPS_PORT` | `443` | idem, TLS |
| `DOCKER_HOST` | socket padrão | lido pelo SDK do Docker |
