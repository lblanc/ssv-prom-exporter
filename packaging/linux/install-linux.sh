#!/usr/bin/env bash
# Reference installer for the ssv-prom-exporter Linux tarball.
#
# Lays out:
#   /usr/local/bin/ssv-prom-exporter
#   /etc/ssv-prom-exporter/config.yaml         (from config.example.yaml on first run)
#   /etc/systemd/system/ssv-prom-exporter.service
#
# Then `systemctl daemon-reload` + `systemctl enable --now`.
# Idempotent — re-running an existing install upgrades the binary
# and the unit, leaves the config.yaml alone.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "install-linux.sh: must run as root (use sudo)." >&2
    exit 1
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- binary
install -D -m 0755 "${here}/ssv-prom-exporter" /usr/local/bin/ssv-prom-exporter

# --- systemd unit
install -D -m 0644 "${here}/ssv-prom-exporter.service" \
        /etc/systemd/system/ssv-prom-exporter.service

# --- config (only created the first time; never overwrites an existing one)
install -d -m 0750 /etc/ssv-prom-exporter
if [[ ! -e /etc/ssv-prom-exporter/config.yaml ]]; then
    install -m 0640 "${here}/config.example.yaml" /etc/ssv-prom-exporter/config.yaml
    echo "Created /etc/ssv-prom-exporter/config.yaml from config.example.yaml."
    echo "Edit it (set url / user / pass), then run:"
    echo "    systemctl enable --now ssv-prom-exporter"
else
    echo "Kept existing /etc/ssv-prom-exporter/config.yaml."
fi

systemctl daemon-reload

# Re-start (idempotent) only if the service is already enabled — for
# fresh installs the operator runs `enable --now` after editing config.
if systemctl is-enabled --quiet ssv-prom-exporter 2>/dev/null; then
    systemctl restart ssv-prom-exporter
    echo "Restarted ssv-prom-exporter."
    systemctl status --no-pager ssv-prom-exporter
fi
