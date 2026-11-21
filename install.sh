#!/usr/bin/env bash
# Instalador do MonitorIQ para Rocky Linux 8, 9 e 10 (funciona também em AlmaLinux e RHEL).
#
#   sudo ./install.sh [opções]
#
# Opções:
#   --listen ENDEREÇO     endereço:porta do servidor web (predefinido 0.0.0.0:8080)
#   --base-url URL        URL público, ex.: https://status.exemplo.pt
#   --open-firewall       abre a porta do servidor web no firewalld
#   --db-url URL          usa uma base de dados PostgreSQL existente (não instala o PostgreSQL local)
#   --pg-version N        versão do PostgreSQL a instalar (predefinido: 16)
#   --build               compila a partir do código fonte mesmo que exista um binário
#   -h, --help            mostra esta ajuda
#
# O script é idempotente: pode ser executado novamente para atualizar o MonitorIQ.
# A configuração existente em /etc/monitoriq/monitoriq.env nunca é substituída.
set -euo pipefail

LISTEN="0.0.0.0:8080"
BASE_URL=""
OPEN_FW=0
DB_URL=""
PG_VERSION="16"
FORCE_BUILD=0
GO_MIN_MINOR=22

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DST="/usr/local/bin/monitoriq"
ETC_DIR="/etc/monitoriq"
ENV_FILE="$ETC_DIR/monitoriq.env"
UNIT_DST="/etc/systemd/system/monitoriq.service"

c_info=$'\e[1;34m'; c_ok=$'\e[1;32m'; c_warn=$'\e[1;33m'; c_err=$'\e[1;31m'; c_off=$'\e[0m'
info() { echo "${c_info}==>${c_off} $*"; }
ok()   { echo "${c_ok}✔${c_off} $*"; }
warn() { echo "${c_warn}!${c_off} $*" >&2; }
die()  { echo "${c_err}erro:${c_off} $*" >&2; exit 1; }

usage() { sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --listen) LISTEN="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --open-firewall) OPEN_FW=1; shift ;;
    --db-url) DB_URL="$2"; shift 2 ;;
    --pg-version) PG_VERSION="$2"; shift 2 ;;
    --build) FORCE_BUILD=1; shift ;;
    -h|--help) usage ;;
    *) die "opção desconhecida: $1 (use --help)" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "execute como root (sudo ./install.sh)"
[[ -r /etc/os-release ]] || die "/etc/os-release não encontrado"
# shellcheck disable=SC1091
. /etc/os-release
EL_MAJOR="${VERSION_ID%%.*}"
case "${ID:-}" in
  rocky|almalinux|rhel|centos|ol) ;;
  *) warn "distribuição '${ID:-?}' não testada; a continuar como EL${EL_MAJOR}" ;;
esac
case "$EL_MAJOR" in
  8|9|10) ;;
  *) die "versão não suportada: ${PRETTY_NAME:-$VERSION_ID} (suportado: 8, 9, 10)" ;;
esac
info "Sistema: ${PRETTY_NAME:-EL$EL_MAJOR}"

rand_secret() { tr -dc 'A-Za-z0-9' </dev/urandom | head -c "${1:-32}" || true; }

# ---------------------------------------------------------------- pacotes base
info "A instalar dependências do sistema (ping, traceroute, …)"
dnf -y -q install iputils traceroute ca-certificates tar curl >/dev/null
ok "Dependências instaladas"

# ---------------------------------------------------------------- PostgreSQL
setup_postgres() {
  info "A instalar o PostgreSQL ${PG_VERSION}"
  if [[ "$EL_MAJOR" == "8" || "$EL_MAJOR" == "9" ]]; then
    if dnf -q module list postgresql 2>/dev/null | grep -qE "^postgresql +${PG_VERSION}\b"; then
      dnf -y -q module reset postgresql >/dev/null
      dnf -y -q module enable "postgresql:${PG_VERSION}" >/dev/null
    else
      warn "stream postgresql:${PG_VERSION} indisponível; será usada a versão por omissão do sistema"
    fi
  fi
  dnf -y -q install postgresql-server postgresql >/dev/null

  local data=/var/lib/pgsql/data
  if [[ ! -f "$data/PG_VERSION" ]]; then
    postgresql-setup --initdb >/dev/null
  fi
  # Autenticação por palavra-passe para o utilizador monitoriq em localhost.
  local hba="$data/pg_hba.conf"
  if ! grep -q "# monitoriq" "$hba"; then
    local tmp; tmp="$(mktemp)"
    {
      echo "# monitoriq"
      echo "host    monitoriq    monitoriq    127.0.0.1/32    scram-sha-256"
      echo "host    monitoriq    monitoriq    ::1/128         scram-sha-256"
      cat "$hba"
    } >"$tmp"
    cat "$tmp" >"$hba"; rm -f "$tmp"
  fi
  if ! grep -qE "^\s*password_encryption\s*=\s*'?scram-sha-256" "$data/postgresql.conf"; then
    echo "password_encryption = 'scram-sha-256'" >>"$data/postgresql.conf"
  fi
  systemctl enable -q postgresql
  systemctl restart postgresql
  ok "PostgreSQL $(runuser -u postgres -- psql -tAc 'SHOW server_version' | cut -d' ' -f1) a correr"
}

create_database() {
  local pw="$1"
  runuser -u postgres -- psql -q -v ON_ERROR_STOP=1 -v pw="$pw" <<'SQL'
SELECT 'CREATE ROLE monitoriq LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'monitoriq')\gexec
ALTER ROLE monitoriq WITH LOGIN PASSWORD :'pw';
SELECT 'CREATE DATABASE monitoriq OWNER monitoriq ENCODING ''UTF8'' TEMPLATE template0' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'monitoriq')\gexec
SQL
}

# ---------------------------------------------------------------- binário
go_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local v; v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  local major="${v%%.*}" rest="${v#*.}"; local minor="${rest%%.*}"
  [[ "$major" -gt 1 || ( "$major" -eq 1 && "$minor" -ge $GO_MIN_MINOR ) ]]
}

obtain_binary() {
  local candidate=""
  for f in "$SCRIPT_DIR/monitoriq" "$SCRIPT_DIR/bin/monitoriq"; do
    [[ -x "$f" && -f "$f" ]] && candidate="$f" && break
  done
  if [[ -n "$candidate" && $FORCE_BUILD -eq 0 ]]; then
    BIN_SRC="$candidate"
    ok "A usar o binário incluído: $candidate"
    return
  fi
  [[ -f "$SCRIPT_DIR/go.mod" ]] || die "binário não encontrado e código fonte ausente em $SCRIPT_DIR"
  if ! go_ok; then
    info "A instalar o compilador Go (golang)"
    dnf -y -q install golang git >/dev/null || true
  fi
  go_ok || die "é necessário Go >= 1.${GO_MIN_MINOR}. Instale a partir de https://go.dev/dl/ ou use um pacote de release com binário."
  info "A compilar o MonitorIQ ($(go env GOVERSION))"
  local ver
  ver="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || cat "$SCRIPT_DIR/VERSION" 2>/dev/null || echo dev)"
  (cd "$SCRIPT_DIR" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -trimpath -ldflags "-s -w -X main.version=${ver}" -o "$SCRIPT_DIR/bin/monitoriq" ./cmd/monitoriq)
  BIN_SRC="$SCRIPT_DIR/bin/monitoriq"
  ok "Compilado: $BIN_SRC ($ver)"
}

# ---------------------------------------------------------------- execução
FIRST_INSTALL=0
[[ -f "$ENV_FILE" ]] || FIRST_INSTALL=1

if [[ $FIRST_INSTALL -eq 1 ]]; then
  if [[ -z "$DB_URL" ]]; then
    setup_postgres
    DB_PW="$(rand_secret 32)"
    create_database "$DB_PW"
    DB_URL="postgres://monitoriq:${DB_PW}@127.0.0.1:5432/monitoriq?sslmode=disable"
    ok "Base de dados 'monitoriq' pronta"
  fi
else
  info "Configuração existente encontrada em $ENV_FILE (atualização)"
  LISTEN="$(sed -n 's/^MONITORIQ_LISTEN=//p' "$ENV_FILE" | tail -1)"; LISTEN="${LISTEN:-127.0.0.1:8080}"
  BASE_URL="$(sed -n 's/^MONITORIQ_BASE_URL=//p' "$ENV_FILE" | tail -1)"
fi

obtain_binary

if ! id monitoriq >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/monitoriq --no-create-home --shell /sbin/nologin \
    --comment "MonitorIQ" monitoriq
  ok "Utilizador de sistema 'monitoriq' criado"
fi

if systemctl is-active -q monitoriq 2>/dev/null; then systemctl stop monitoriq; fi
install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DST"
command -v restorecon >/dev/null 2>&1 && restorecon -q "$BIN_DST" || true
ok "Binário instalado em $BIN_DST ($("$BIN_DST" version))"

install -d -m 0750 -o root -g monitoriq "$ETC_DIR"
if [[ $FIRST_INSTALL -eq 1 ]]; then
  umask 027
  cat >"$ENV_FILE" <<EOF
# Configuração do MonitorIQ — gerada pelo instalador em $(date -Iseconds)
# Após alterar: systemctl restart monitoriq
MONITORIQ_DATABASE_URL=${DB_URL}
MONITORIQ_LISTEN=${LISTEN}
MONITORIQ_BASE_URL=${BASE_URL}
MONITORIQ_TRUST_PROXY=false
MONITORIQ_MAX_CONCURRENCY=50
EOF
  chown root:monitoriq "$ENV_FILE"; chmod 0640 "$ENV_FILE"
  ok "Configuração escrita em $ENV_FILE"
fi

install -m 0644 "$SCRIPT_DIR/deploy/systemd/monitoriq.service" "$UNIT_DST"
systemctl daemon-reload

info "A aplicar migrações da base de dados"
runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" migrate

ADMIN_PW=""
if [[ $FIRST_INSTALL -eq 1 ]] && ! runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" user list | grep -qx admin; then
  ADMIN_PW="$(rand_secret 16)"
  runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" user set admin --password "$ADMIN_PW" >/dev/null
fi

systemctl enable -q monitoriq
systemctl restart monitoriq
sleep 2
systemctl is-active -q monitoriq || { journalctl -u monitoriq -n 30 --no-pager; die "o serviço monitoriq não arrancou"; }
ok "Serviço monitoriq ativo"

PORT="${LISTEN##*:}"
if [[ $OPEN_FW -eq 1 ]]; then
  if systemctl is-active -q firewalld; then
    firewall-cmd -q --permanent --add-port="${PORT}/tcp" && firewall-cmd -q --reload
    ok "Porta ${PORT}/tcp aberta no firewalld"
  else
    warn "firewalld não está ativo; nada a fazer"
  fi
fi

HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
URL="${BASE_URL:-http://${HOST_IP:-localhost}:${PORT}}"
echo
echo "${c_ok}MonitorIQ instalado com sucesso.${c_off}"
echo "  Endereço:     ${URL}"
if [[ -n "$ADMIN_PW" ]]; then
  echo "  Utilizador:   admin"
  echo "  Palavra-passe: ${ADMIN_PW}   (altere-a em Utilizadores após o primeiro login)"
fi
echo "  Configuração: ${ENV_FILE}"
echo "  Logs:         journalctl -u monitoriq -f"
[[ $OPEN_FW -eq 0 && "$LISTEN" != 127.0.0.1:* ]] && echo "  Firewall:     firewall-cmd --permanent --add-port=${PORT}/tcp && firewall-cmd --reload"
exit 0
