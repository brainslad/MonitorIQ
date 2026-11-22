#!/usr/bin/env bash
# Copyright (C) 2026 Brainslad <https://github.com/brainslad>
# SPDX-License-Identifier: GPL-3.0-or-later
# Part of MonitorIQ; see LICENSE and NOTICE (author attribution must be kept).
# MonitorIQ installer for Rocky Linux 8, 9 and 10 (also works on AlmaLinux and RHEL).
#
#   sudo ./install.sh [options]
#
# Options:
#   --listen ADDRESS      web server address:port (default 0.0.0.0:8080)
#   --base-url URL        public URL, e.g. https://status.example.com
#   --open-firewall       open the web server port in firewalld
#   --db-url URL          use an existing PostgreSQL database (does not install a local PostgreSQL)
#   --pg-version N        PostgreSQL version to install (default: 16)
#   --build               build from source even if a binary is present
#   -h, --help            show this help
#
# The script is idempotent: run it again to upgrade MonitorIQ.
# An existing /etc/monitoriq/monitoriq.env is never overwritten.
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
die()  { echo "${c_err}error:${c_off} $*" >&2; exit 1; }

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
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "run as root (sudo ./install.sh)"
[[ -r /etc/os-release ]] || die "/etc/os-release not found"
# shellcheck disable=SC1091
. /etc/os-release
EL_MAJOR="${VERSION_ID%%.*}"
case "${ID:-}" in
  rocky|almalinux|rhel|centos|ol) ;;
  *) warn "distribution '${ID:-?}' not tested; continuing as EL${EL_MAJOR}" ;;
esac
case "$EL_MAJOR" in
  8|9|10) ;;
  *) die "unsupported version: ${PRETTY_NAME:-$VERSION_ID} (supported: 8, 9, 10)" ;;
esac
info "System: ${PRETTY_NAME:-EL$EL_MAJOR}"

rand_secret() { tr -dc 'A-Za-z0-9' </dev/urandom | head -c "${1:-32}" || true; }

# ---------------------------------------------------------------- base packages
info "Installing system dependencies (ping, traceroute, …)"
dnf -y -q install iputils traceroute ca-certificates tar curl >/dev/null
ok "Dependencies installed"

# ---------------------------------------------------------------- PostgreSQL
setup_postgres() {
  info "Installing PostgreSQL ${PG_VERSION}"
  if [[ "$EL_MAJOR" == "8" || "$EL_MAJOR" == "9" ]]; then
    if dnf -q module list postgresql 2>/dev/null | grep -qE "^postgresql +${PG_VERSION}\b"; then
      dnf -y -q module reset postgresql >/dev/null
      dnf -y -q module enable "postgresql:${PG_VERSION}" >/dev/null
    else
      warn "stream postgresql:${PG_VERSION} not available; using the system default version"
    fi
  fi
  dnf -y -q install postgresql-server postgresql >/dev/null

  local data=/var/lib/pgsql/data
  if [[ ! -f "$data/PG_VERSION" ]]; then
    postgresql-setup --initdb >/dev/null
  fi
  # Password authentication for the monitoriq user on localhost.
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
  ok "PostgreSQL $(runuser -u postgres -- psql -tAc 'SHOW server_version' | cut -d' ' -f1) running"
}

create_database() {
  local pw="$1"
  runuser -u postgres -- psql -q -v ON_ERROR_STOP=1 -v pw="$pw" <<'SQL'
SELECT 'CREATE ROLE monitoriq LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'monitoriq')\gexec
ALTER ROLE monitoriq WITH LOGIN PASSWORD :'pw';
SELECT 'CREATE DATABASE monitoriq OWNER monitoriq ENCODING ''UTF8'' TEMPLATE template0' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'monitoriq')\gexec
SQL
}

# ---------------------------------------------------------------- binary
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
    ok "Using bundled binary: $candidate"
    return
  fi
  [[ -f "$SCRIPT_DIR/go.mod" ]] || die "no binary found and no source code in $SCRIPT_DIR"
  if ! go_ok; then
    info "Installing the Go compiler (golang)"
    dnf -y -q install golang git >/dev/null || true
  fi
  go_ok || die "Go >= 1.${GO_MIN_MINOR} is required. Install it from https://go.dev/dl/ or use a release package with a binary."
  info "Building MonitorIQ ($(go env GOVERSION))"
  local ver
  ver="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || cat "$SCRIPT_DIR/VERSION" 2>/dev/null || echo dev)"
  (cd "$SCRIPT_DIR" && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -trimpath -ldflags "-s -w -X main.version=${ver}" -o "$SCRIPT_DIR/bin/monitoriq" ./cmd/monitoriq)
  BIN_SRC="$SCRIPT_DIR/bin/monitoriq"
  ok "Built: $BIN_SRC ($ver)"
}

# ---------------------------------------------------------------- main
FIRST_INSTALL=0
[[ -f "$ENV_FILE" ]] || FIRST_INSTALL=1

if [[ $FIRST_INSTALL -eq 1 ]]; then
  if [[ -z "$DB_URL" ]]; then
    setup_postgres
    DB_PW="$(rand_secret 32)"
    create_database "$DB_PW"
    DB_URL="postgres://monitoriq:${DB_PW}@127.0.0.1:5432/monitoriq?sslmode=disable"
    ok "Database 'monitoriq' ready"
  fi
else
  info "Existing configuration found in $ENV_FILE (upgrade)"
  LISTEN="$(sed -n 's/^MONITORIQ_LISTEN=//p' "$ENV_FILE" | tail -1)"; LISTEN="${LISTEN:-127.0.0.1:8080}"
  BASE_URL="$(sed -n 's/^MONITORIQ_BASE_URL=//p' "$ENV_FILE" | tail -1)"
fi

obtain_binary

if ! id monitoriq >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/monitoriq --no-create-home --shell /sbin/nologin \
    --comment "MonitorIQ" monitoriq
  ok "System user 'monitoriq' created"
fi

if systemctl is-active -q monitoriq 2>/dev/null; then systemctl stop monitoriq; fi
install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DST"
command -v restorecon >/dev/null 2>&1 && restorecon -q "$BIN_DST" || true
ok "Binary installed to $BIN_DST ($("$BIN_DST" version))"

install -d -m 0750 -o root -g monitoriq "$ETC_DIR"
if [[ $FIRST_INSTALL -eq 1 ]]; then
  umask 027
  cat >"$ENV_FILE" <<EOF
# MonitorIQ configuration — generated by the installer on $(date -Iseconds)
# After changing: systemctl restart monitoriq
MONITORIQ_DATABASE_URL=${DB_URL}
MONITORIQ_LISTEN=${LISTEN}
MONITORIQ_BASE_URL=${BASE_URL}
MONITORIQ_TRUST_PROXY=false
MONITORIQ_MAX_CONCURRENCY=50
EOF
  chown root:monitoriq "$ENV_FILE"; chmod 0640 "$ENV_FILE"
  ok "Configuration written to $ENV_FILE"
fi
# Key that encrypts 2FA secrets and LDAP/SSO credentials in the database
# (added on upgrade from versions without it). Never change it afterwards.
if ! grep -q '^MONITORIQ_SECRET_KEY=.' "$ENV_FILE"; then
  sed -i '/^MONITORIQ_SECRET_KEY=$/d' "$ENV_FILE"
  printf '\n# Encrypts 2FA secrets and LDAP/SSO credentials. Keep it safe; do not change it.\nMONITORIQ_SECRET_KEY=%s\n' "$(rand_secret 48)" >>"$ENV_FILE"
  ok "Secret key generated in $ENV_FILE"
fi

install -m 0644 "$SCRIPT_DIR/deploy/systemd/monitoriq.service" "$UNIT_DST"
systemctl daemon-reload

info "Applying database migrations"
runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" migrate

ADMIN_PW=""
if [[ $FIRST_INSTALL -eq 1 ]] && ! runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" user list | grep -q '^admin '; then
  ADMIN_PW="$(rand_secret 16)"
  runuser -u monitoriq -- "$BIN_DST" --env "$ENV_FILE" user set admin --password "$ADMIN_PW" >/dev/null
fi

systemctl enable -q monitoriq
systemctl restart monitoriq
sleep 2
systemctl is-active -q monitoriq || { journalctl -u monitoriq -n 30 --no-pager; die "the monitoriq service failed to start"; }
ok "monitoriq service is running"

PORT="${LISTEN##*:}"
if [[ $OPEN_FW -eq 1 ]]; then
  if systemctl is-active -q firewalld; then
    firewall-cmd -q --permanent --add-port="${PORT}/tcp" && firewall-cmd -q --reload
    ok "Port ${PORT}/tcp opened in firewalld"
  else
    warn "firewalld is not running; nothing to do"
  fi
fi

HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
URL="${BASE_URL:-http://${HOST_IP:-localhost}:${PORT}}"
echo
echo "${c_ok}MonitorIQ installed successfully.${c_off}"
echo "  URL:           ${URL}"
if [[ -n "$ADMIN_PW" ]]; then
  echo "  Username:      admin"
  echo "  Password:      ${ADMIN_PW}   (change it under Users after the first login)"
fi
echo "  Configuration: ${ENV_FILE}"
echo "  Logs:          journalctl -u monitoriq -f"
[[ $OPEN_FW -eq 0 && "$LISTEN" != 127.0.0.1:* ]] && echo "  Firewall:      firewall-cmd --permanent --add-port=${PORT}/tcp && firewall-cmd --reload"
exit 0
