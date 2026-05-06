# Project: ssv-prom-exporter

## Goal
Prometheus exporter for DataCore SANsymphony (SSV), packaged as a native
Windows service. Exposes three signal categories scraped from the SSV REST
API: topology/state, health (monitors + alerts), and performance counters.

## Status
active

## Stack
- Language: Go 1.26
- Metrics: github.com/prometheus/client_golang
- Windows service: golang.org/x/sys/windows/svc (+ /mgr for install/uninstall,
  /eventlog for service-mode logging)
- Config: env vars for v0; YAML once more knobs are needed
- HTTP client: stdlib net/http (Basic auth + ServerHost header +
  InsecureSkipVerify by default for SSV's self-signed certs)
- Runtime target: Windows Server (any version supported by SSV PSP 20+).
  Builds cross-compile cleanly from Linux (CGO_ENABLED=0).

## Repository
- Remote: git@github.com:lblanc/ssv-prom-exporter.git
- Default branch: main

## Directory layout
```
cmd/ssv-prom-exporter/    # entrypoint (CLI flags, dispatch)
internal/ssv/             # REST client, types, .NET-date unmarshaller
internal/collectors/      # one file per signal tier (inventory, health, performance)
internal/svc/             # Windows service wrapper + EventLog
internal/config/          # config loading (later)
```

## How to run / build / test
```
make build              # native (linux/amd64)
make build-windows      # cross-compile to windows/amd64
make run-ping           # probe the lab using SSV_URL / SSV_USER / SSV_PASS env vars
```

Smoke test against the lab:
```
SSV_URL=https://10.12.110.11 SSV_USER=administrator SSV_PASS=*** \
  ./bin/ssv-prom-exporter -ping
```

## Current focus
Feature-complete v0: three collectors + REST failover + Windows
service mode all in. Next round of polish is config + retry + CI.

## Remaining tasks
Coverage gaps surfaced by the Grafana dashboards (the inspiration
boards had panels we couldn't fill from current metrics). Expose:
- [ ] **Pool extras**: `EstimatedDepletionTime` (gauge, seconds),
      `MaxTierNumber`, `TierReservedPct`, `InSharedMode` (info
      labels on `ssv_pool_info` if we add one).
- [ ] **VDisk allocation %** if the field exists on the REST shape
      (the InfluxDB inspiration uses `PercentAllocated`).

## Completed
- 2026-05-05 — REST API discovery against PSP 20 lab; all key endpoints
  validated; `/performance/{instanceId}` confirmed as the perf access pattern.
- 2026-05-05 — Project skeleton: go.mod, cmd entrypoint with `-ping` mode,
  Makefile with linux/windows cross-compile targets, .gitignore, filled
  PROJECT_CONTEXT.md and DECISIONS.md.
- 2026-05-05 — Typed REST client (`internal/ssv`) with Basic auth, mandatory
  `ServerHost` header, .NET-date `Time` wrapper, and list helpers for
  `serverGroups`, `servers`, `pools`, `virtualDisks`. Refactored `-ping`
  to use it.
- 2026-05-05 — Inventory collector (`internal/collectors/inventory.go`)
  exposing 25 `ssv_*` series (group / server / pool / vdisk), wired to
  `/metrics` via promhttp under a new `-listen` flag. Verified against
  the lab: `ssv_up=1`, scrape ~200ms, all labels populated.
- 2026-05-05 — Health collector (`internal/collectors/health.go`)
  exposing `ssv_monitor_state` (252 series in the lab) and
  `ssv_alerts_total`. Refactored multi-collector wiring through a new
  `Scrape` wrapper (`internal/collectors/scrape.go`) so `ssv_up` and
  `ssv_scrape_duration_seconds` are emitted once, with a `collector`
  label, by the wrapper rather than each child.
- 2026-05-05 — REST endpoint failover. Client now holds an ordered list
  of `(baseURL, ServerHost)` pairs (primary first, backups appended
  from `/servers[].IpAddresses` after each scrape). Failover triggers
  on transient errors only (net / 5xx). Sticky preferred endpoint
  (5 min TTL) avoids hammering a dead primary. CIDR allowlist filters
  discovered IPs (default = primary's `/24`). Two new flags:
  `-bases` (cold-start backup seed) and `-backup-cidrs` (filter
  override).
- 2026-05-05 — Performance collector
  (`internal/collectors/performance.go`). Bounded worker pool
  (default 8 concurrent) fans out `GET /performance/{id}` for every
  server, pool and virtual disk known from inventory. Emits
  `ssv_{server,pool,virtual_disk}_*` counters and gauges:
  per-direction IO bytes/ops, cache hits/misses, server cache
  size/free, pool capacity / used / available / reserved /
  reclamation / oversubscribed. New flag `-perf-workers`. 10 perf
  calls in the lab → ~470 ms scrape; 90 new series, 404 total.
- 2026-05-05 — Windows service mode (`internal/svc/`). Same binary
  runs interactively or under the SCM, picked at startup via
  `svc.IsWindowsService()`. New flags `-install` / `-uninstall` /
  `-svc-name` / `-svc-display` / `-svc-description`. Build-tagged
  files (`svc_windows.go` for the real impl, `svc_other.go` for
  Linux stubs) keep the project building on both platforms.
  Service-mode slog handler writes to the registered Event Log
  source. Cross-compiled `bin/ssv-prom-exporter.exe` validated;
  Linux console mode tested with SIGINT graceful shutdown.
- 2026-05-06 — YAML config (`internal/config/`, `gopkg.in/yaml.v3`).
  New `-config` flag loads a typed Config struct; merge-into-flags
  honors explicit flag > env > YAML > default precedence. Unknown
  YAML keys are rejected (typo protection). Recommended Windows
  install flow now bakes only `-config <path>` into the SCM
  ImagePath, keeping `-pass` out of `sc qc`.
  `config.example.yaml` ships in the repo.
- 2026-05-06 — Alerts now exposed in detail. New `ssv.Alert` type
  + `client.Alerts()` replace the old `AlertsCount`. Health
  collector emits `ssv_alert_info{alert_id, machine_id, machine,
  level, high_priority, needs_ack, caller, message}` (gauge=1) plus
  `ssv_alert_age_seconds{alert_id}`. Overview dashboard gains a
  table panel listing every alert, sorted by age, with color-coded
  level cells and an "Alert details" data link on the "Active
  Alerts" stat.
- 2026-05-06 — Storage dashboard: per-pool "Physical disks of
  $pool" panels moved into a collapsed row (click to expand) so
  the at-a-glance pool view stays compact.
- 2026-05-06 — Physical disks + pool members. New `ssv.PhysicalDisk`
  and `ssv.PoolMember` types. Inventory filters /physicalDisks down
  to Type==4 (real pool-member disks) and emits `ssv_physical_disk_
  {status,size_bytes,free_bytes,info}` plus a separate
  `ssv_physical_disk_pool{disk_id, pool_id, pool, tier}` relation
  metric (joinable on disk_id with the perf series). Performance
  emits cumulative ops/bytes, per-direction read/write time
  (TotalReadsTime / TotalWritesTime — note the 's', different from
  pool's TotalReadTime), and gauges
  `physical_disk_{io,read,write}_max_time_seconds`,
  `physical_disk_avg_queue_length`,
  `physical_disk_pending_commands`. Storage dashboard gains a
  per-pool "Physical disks" table + IOPS + latency time-series.
- 2026-05-06 — Ports (SCSI / iSCSI / FC) collector. New `ssv.Port`
  type + `client.Ports()` against `/ports`. Inventory emits
  `ssv_port_connected`, `ssv_port_role_capability` (vendor bitmap),
  `ssv_port_info` (port_name, alias, port_type, port_mode, host_id).
  Performance fans out `/performance/{port-id}` for aggregate IO
  (`ssv_port_{read,write}_{ops,bytes}_total`,
  `ssv_port_pending_commands`), per-direction split
  (`ssv_port_{target,initiator}_{ops,bytes}_total`), target latency
  (`ssv_port_target_io_time_seconds_total`,
  `ssv_port_target_io_max_time_seconds`), and link-layer error
  counters (busy / invalid_crc / link_failure / loss_of_signal /
  loss_of_sync). Internal=true ports are skipped. New Grafana
  dashboard `ssv-ports.json`.
- 2026-05-06 — Group template variable changed to single-select on
  every dashboard (`multi: false`, `includeAll: false`). Better fits
  the "one SAN group at a time" workflow.
- 2026-05-06 — Hosts (SAN-client / initiator) collector. New
  `ssv.Host` type + `client.Hosts()` against `/hosts`. Inventory
  collector emits `ssv_host_state`, `ssv_host_connection_state`,
  `ssv_host_maintenance_mode`, `ssv_host_type`, `ssv_host_info`
  (host_name / description / version labels). Performance collector
  fans out `/performance/{host-id}` for `ssv_host_{read,write}_{ops,bytes}_total`,
  `ssv_host_provisioned_bytes`, `ssv_host_max_{read,write,op}_size_bytes`.
  Hosts flagged Internal=true are skipped (SSV bookkeeping pseudo-hosts).
  New Grafana dashboard `ssv-hosts.json` (inventory table + IOPS /
  bandwidth time-series + IO-size + provisioning bargauge).
- 2026-05-06 — Multi-group test stack + dashboards. Prometheus
  config is now generated at container start by
  `deploy/prometheus/gen-config.sh` from
  `EXPORTER_TARGETS=name1=host:port,name2=host:port` — each target
  gets a `group` label. All 3 dashboards gained a `Group` template
  variable and every PromQL selector is filtered with
  `{group=~"$group"}`. Variables (`server`/`pool`/`vdisk`) cascade on
  the active group via `label_values(metric{group=~"$group"}, ...)`.
  Servers dashboard gained a "Server versions" table reading
  `ssv_server_info` (host_name, product_name, product_version,
  product_build, os_version). Inventory collector now also exposes
  `product_name` on `ssv_server_info`.
- 2026-05-06 — Test stack under `deploy/` with docker-compose
  (Prometheus 3.5 + Grafana 12), datasource + 3 dashboards
  (Overview / Servers / Storage) provisioned. Prometheus config is a
  template substituted at container start with `EXPORTER_TARGET` from
  `.env`. Anonymous Viewer enabled in Grafana for fast read-only
  access. Validated against the lab at `10.12.110.11:9876` —
  `ssv_up=1` on all three collectors.
- 2026-05-06 — MSI packaging + GitHub Releases. New `packaging/windows/installer.wxs`
  (per-machine install, drops exe + LICENSE + config.example.yaml under
  Program Files, creates empty ProgramData dir, no service registration);
  `make msi` target via `wixl` (Debian); release workflow
  (`.github/workflows/release.yml`) triggers on `v*` tags, installs
  wixl, builds binary + MSI, attaches them with SHA256SUMS to a
  GitHub Release. README rewritten around feature list + install via
  MSI; `LICENSE` (MIT) added.
- 2026-05-06 — Latency / IO-time metrics added to the perf collector
  (`internal/collectors/performance.go`). Unit verified empirically
  against PSP 20 (Δ-time / Δ-ops fell in 0.6–2.8 → ms). New metrics:
  per pipeline class on the server
  (`ssv_server_class_io_{operations_total,time_seconds_total,max_time_seconds}`
  with `class ∈ {front_end_target, mirror_target, physical_disk, pool,
  target}`); pool read/write/io time + max-time variants
  (`ssv_pool_{read,write,io}_{time_seconds_total,max_time_seconds}`);
  vdisk `ssv_virtual_disk_io_time_seconds_total` /
  `ssv_virtual_disk_io_max_time_seconds`. ms→s scaling lives in
  `timeScale`. `perfMapping` extended with `scale` and `extraLabels`
  to support per-class fan-out.
- 2026-05-06 — Retry/backoff on transient SSV failures
  (`internal/ssv/client.go`). `GetRaw` now wraps the failover loop in a
  retry loop (default 2 retries) with exponential backoff (200ms base,
  capped at 2s) + 50% jitter, honoring ctx. Non-transient errors (4xx)
  short-circuit. New flags `-retries` / `-retry-delay`, mirrored in
  YAML (`retries`, `retry_delay`). Unit tests cover transient-then-OK,
  exhaustion, 4xx short-circuit, ctx cancellation, and backoff cap.
- 2026-05-06 — GitHub Actions CI (`.github/workflows/ci.yml`):
  single job on `ubuntu-latest` running `go vet`, `go build ./...`,
  `go test ./...`, and a `windows/amd64` cross-compile build. Triggers
  on every push and `workflow_dispatch`. Go version pinned via
  `go-version-file: go.mod`. README badge wired.
