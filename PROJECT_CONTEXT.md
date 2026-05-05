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
- Remote: <to be set>
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
v0 ship: project skeleton + working REST ping. Next: typed REST client,
inventory collector exposing /metrics.

## Remaining tasks
- [ ] internal/ssv: typed REST client (Basic auth, ServerHost header,
      .NET date parsing, retry on transient failures)
- [ ] internal/collectors/inventory: `ssv_up`, `ssv_servers_total`,
      `ssv_server_state`, `ssv_pool_capacity_bytes`, `ssv_virtual_disk_state`
- [ ] internal/collectors/health: `ssv_monitor_state`, `ssv_alert_active`
- [ ] internal/collectors/performance: parallel /performance/{id} fetch with
      worker pool, `*_bytes_total` / `*_operations_total` counters
- [ ] HTTP server: /metrics endpoint with promhttp
- [ ] internal/svc: Windows service mode (install / uninstall / run as service),
      EventLog wiring, fall-back to console mode under -foreground
- [ ] YAML config replacing env vars when more knobs are needed
- [ ] CI: go vet + go test + cross-compile check

## Completed
- 2026-05-05 — REST API discovery against PSP 20 lab; all key endpoints
  validated; `/performance/{instanceId}` confirmed as the perf access pattern.
- 2026-05-05 — Project skeleton: go.mod, cmd entrypoint with `-ping` mode,
  Makefile with linux/windows cross-compile targets, .gitignore, filled
  PROJECT_CONTEXT.md and DECISIONS.md.
