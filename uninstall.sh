#!/usr/bin/env bash
# Remove o MonitorIQ.
#   sudo ./uninstall.sh           remove o serviço e o binário, mantém a configuração e a base de dados
#   sudo ./uninstall.sh --purge   remove também /etc/monitoriq e a base de dados 'monitoriq'
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "execute como root" >&2; exit 1; }
PURGE=0; [[ "${1:-}" == "--purge" ]] && PURGE=1

systemctl disable --now monitoriq 2>/dev/null || true
rm -f /etc/systemd/system/monitoriq.service /usr/local/bin/monitoriq
systemctl daemon-reload

if [[ $PURGE -eq 1 ]]; then
  read -r -p "Apagar DEFINITIVAMENTE a base de dados e a configuração? Escreva 'sim': " a
  if [[ "$a" == "sim" ]]; then
    if systemctl is-active -q postgresql; then
      runuser -u postgres -- psql -q -c "DROP DATABASE IF EXISTS monitoriq" -c "DROP ROLE IF EXISTS monitoriq" || true
    fi
    rm -rf /etc/monitoriq /var/lib/monitoriq
    userdel monitoriq 2>/dev/null || true
    echo "Dados removidos."
  fi
fi
echo "MonitorIQ removido."
