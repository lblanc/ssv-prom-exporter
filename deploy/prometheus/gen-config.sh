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

cat > /tmp/prometheus.yml <<HEAD
global:
  scrape_interval: 30s
  evaluation_interval: 30s
  scrape_timeout: 15s
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

cat >> /tmp/prometheus.yml <<TAIL

  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]
TAIL

echo "gen-config: wrote /tmp/prometheus.yml with targets: $TARGETS"
