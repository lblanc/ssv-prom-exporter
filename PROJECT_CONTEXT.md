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
- [ ] **Physical disks** (`/physicalDisks` + `/performance/{id}`):
      `ssv_physical_disk_{status,size_bytes,...}` and IOPS / latency
      counters mirroring the server/pool/vdisk shape.
- [ ] **SCSI ports** (`/ports` + `/performance/{id}`): TotalReads /
      TotalWrites / TotalOperations rates per port (front-end /
      mirror role).
- [ ] **Hosts (initiators)** as first-class objects: `/hosts` for
      inventory + `/performance/{id}` for per-host IOPS / bandwidth /
      max-IO-size, instead of relying on the global `Initiator*`
      counters baked into the server perf snapshot.
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
