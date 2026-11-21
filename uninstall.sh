#!/usr/bin/env bash
# Removes MonitorIQ.
#   sudo ./uninstall.sh           remove the service and binary, keep the configuration and database
#   sudo ./uninstall.sh --purge   also remove /etc/monitoriq and the 'monitoriq' database
set -euo pipefail
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
PURGE=0; [[ "${1:-}" == "--purge" ]] && PURGE=1

systemctl disable --now monitoriq 2>/dev/null || true
rm -f /etc/systemd/system/monitoriq.service /usr/local/bin/monitoriq
systemctl daemon-reload

if [[ $PURGE -eq 1 ]]; then
  read -r -p "PERMANENTLY delete the database and configuration? Type 'yes': " a
  if [[ "$a" == "yes" ]]; then
    if systemctl is-active -q postgresql; then
      runuser -u postgres -- psql -q -c "DROP DATABASE IF EXISTS monitoriq" -c "DROP ROLE IF EXISTS monitoriq" || true
    fi
    rm -rf /etc/monitoriq /var/lib/monitoriq
    userdel monitoriq 2>/dev/null || true
    echo "Data removed."
  fi
fi
echo "MonitorIQ removed."
