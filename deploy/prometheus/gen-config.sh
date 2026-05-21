#!/bin/sh
# Emit /tmp/prometheus.yml from EXPORTER_TARGETS — one static_config per
# target, each carrying a `group` label so dashboards can filter
# multiple SANsymphony groups at once.
#
# Format:
#   EXPORTER_TARGETS="name1=host:port,name2=host:port,..."
#
# A single-target EXPORTER_TARGET (legacy) is still honored: it becomes
# the "lab" group. With nothing set, defaults to lab=host.docker.internal:9876.

set -e

TARGETS="${EXPORTER_TARGETS:-${EXPORTER_TARGET:+lab=$EXPORTER_TARGET}}"
TARGETS="${TARGETS:-lab=host.docker.internal:9876}"

# Optional out-of-order storage window. Required when remote-write is
# enabled (PROM_REMOTE_WRITE=1) — without it, Prometheus silently drops
# any imported sample older than the current head block (~2h), and
# prom-clip imports look successful but write nothing.
if [ -n "${PROM_REMOTE_WRITE:-}" ]; then
  cat > /tmp/prometheus.yml <<STORAGE
storage:
  tsdb:
    out_of_order_time_window: ${PROM_OOO_WINDOW:-7d}

STORAGE
else
  : > /tmp/prometheus.yml
fi

cat >> /tmp/prometheus.yml <<'HEAD'
global:
  scrape_interval: 5s
  evaluation_interval: 5s
  scrape_timeout: 4s
  external_labels:
    monitor: ssv-prom-exporter-test

scrape_configs:
  - job_name: ssv-prom-exporter
    metrics_path: /metrics
    static_configs:
HEAD

old_ifs="$IFS"
IFS=','
for entry in $TARGETS; do
  IFS="$old_ifs"
  name="${entry%%=*}"
  addr="${entry#*=}"
  if [ -z "$name" ] || [ -z "$addr" ] || [ "$name" = "$addr" ]; then
    echo "gen-config: invalid entry '$entry' (expected name=host:port)" >&2
    exit 2
  fi
  cat >> /tmp/prometheus.yml <<ENTRY
      - targets: ["$addr"]
        labels:
          group: "$name"
ENTRY
  IFS=','
done
IFS="$old_ifs"

cat >> /tmp/prometheus.yml <<'TAIL'

  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]
TAIL

echo "gen-config: wrote /tmp/prometheus.yml with targets: $TARGETS"
