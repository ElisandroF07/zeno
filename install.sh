#!/usr/bin/env bash
# Instalador do Zeno: baixa o binário, garante Docker e Nixpacks, sobe como
# serviço systemd. Feito para uma VPS Linux limpa — não mexe em nada que já
# não seja do Zeno, e pode ser rodado de novo para atualizar (idempotente).
#
#   curl -fsSL https://get.zeno.dev | bash
#
# Variáveis de override (todas opcionais):
#   ZENO_VERSION        tag a instalar, ex. "v0.3.0" (padrão: latest)
#   ZENO_INSTALL_DIR    onde fica o binário (padrão: /usr/local/bin)
#   ZENO_HOME           onde ficam banco, logs e presets (padrão: /etc/zeno)
#   ZENO_RELEASE_BASE   sobrepõe a URL base de download — usado pelos testes
#                        deste instalador, e por quem hospeda o próprio mirror
set -euo pipefail

readonly REPO="f07nobru/zeno"
readonly INSTALL_DIR="${ZENO_INSTALL_DIR:-/usr/local/bin}"
readonly ZENO_HOME="${ZENO_HOME:-/etc/zeno}"
readonly VERSION="${ZENO_VERSION:-latest}"
readonly RELEASE_BASE="${ZENO_RELEASE_BASE:-https://github.com/${REPO}/releases}"

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
erro() { printf '\033[1;31merro:\033[0m %s\n' "$*" >&2; }
avisar() { printf '\033[1;33maviso:\033[0m %s\n' "$*" >&2; }

falhar() {
  erro "$*"
  exit 1
}

precisa_de_root() {
  [ "$(id -u)" -eq 0 ] || falhar "rode como root (ou com sudo) — o instalador escreve em ${INSTALL_DIR}, ${ZENO_HOME} e /etc/systemd/system."
}

# --- detecção de plataforma -------------------------------------------------

detectar_arquitetura() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "amd64" ;;
    aarch64 | arm64) echo "arm64" ;;
    *) falhar "arquitetura não suportada: $(uname -m). O Zeno publica binários para amd64 e arm64." ;;
  esac
}

garantir_linux() {
  [ "$(uname -s)" = "Linux" ] || falhar "este instalador é para Linux — o Zeno roda numa VPS, não na sua máquina local."
}

comando_existe() { command -v "$1" >/dev/null 2>&1; }

# --- dependências ------------------------------------------------------------

# Docker é reaproveitado do instalador oficial deles — não existe motivo para
# reimplementar detecção de distro (apt/dnf/pacman) que o get.docker.com já
# resolve, testado em muito mais distribuição do que eu conseguiria cobrir.
garantir_docker() {
  if comando_existe docker; then
    log "Docker já instalado ($(docker --version))"
    return
  fi
  log "Docker não encontrado — instalando via get.docker.com"
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
}

# Nixpacks não é lib Go — é um binário Rust separado que precisa estar no
# PATH. Mesmo raciocínio do Docker: o instalador oficial já sabe resolver
# arquitetura sozinho.
garantir_nixpacks() {
  if comando_existe nixpacks; then
    log "Nixpacks já instalado ($(nixpacks --version 2>/dev/null || echo presente))"
    return
  fi
  log "Nixpacks não encontrado — instalando via nixpacks.com/install.sh"
  curl -fsSL https://nixpacks.com/install.sh | bash
}

# --- binário do Zeno ---------------------------------------------------------

url_do_binario() {
  local arch="$1"
  if [ "$VERSION" = "latest" ]; then
    echo "${RELEASE_BASE}/latest/download/zeno-linux-${arch}"
  else
    echo "${RELEASE_BASE}/download/${VERSION}/zeno-linux-${arch}"
  fi
}

instalar_binario() {
  local arch url tmp
  arch="$(detectar_arquitetura)"
  url="$(url_do_binario "$arch")"
  tmp="$(mktemp)"

  log "Baixando Zeno (${VERSION}, linux-${arch})"
  if ! curl -fsSL -o "$tmp" "$url"; then
    rm -f "$tmp"
    falhar "não consegui baixar $url — versão inexistente ou release ainda não publicada para essa arquitetura."
  fi

  chmod +x "$tmp"
  mv "$tmp" "${INSTALL_DIR}/zeno"
  log "Zeno instalado em ${INSTALL_DIR}/zeno"
}

# --- serviço systemd ---------------------------------------------------------

# Unit escrito a cada execução (fonte de verdade é este script, não o que
# already está no disco) — reinstalar sempre deixa a unit atualizada com
# a versão mais recente deste instalador.
escrever_systemd_unit() {
  cat > /etc/systemd/system/zeno.service <<UNIT
[Unit]
Description=Zeno — plataforma de deploy self-hosted
Documentation=https://github.com/${REPO}
After=network-online.target docker.service
Requires=docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/zeno
WorkingDirectory=${ZENO_HOME}
Environment=ZENO_DB=${ZENO_HOME}/zeno.db
Environment=ZENO_LOGS=${ZENO_HOME}/logs
Environment=ZENO_PRESETS=${ZENO_HOME}/presets
Environment=ZENO_ADDR=:8080
Restart=on-failure
RestartSec=2
# O binário fala com o Docker via socket, não via API remota — root é o
# caminho de menor atrito aqui. Isolar isso num usuário de serviço próprio
# ainda exigiria colocá-lo no grupo docker, o que dá o mesmo acesso efetivo.
User=root

[Install]
WantedBy=multi-user.target
UNIT
}

ativar_servico() {
  mkdir -p "${ZENO_HOME}/presets" "${ZENO_HOME}/logs"
  escrever_systemd_unit
  systemctl daemon-reload
  systemctl enable zeno
  systemctl restart zeno
}

# --- main --------------------------------------------------------------------

principal() {
  precisa_de_root
  garantir_linux
  garantir_docker
  garantir_nixpacks
  instalar_binario
  ativar_servico

  log "Zeno no ar — acompanhe com: journalctl -u zeno -f"
  echo
  avisar "A UI não tem autenticação e controla o Docker do host."
  avisar "Não exponha a porta 8080 (nem 80/443 do nginx) direto na internet"
  avisar "sem VPN/Tailscale ou allowlist de IP — quem alcança a UI é root da VPS."
}

principal "$@"
