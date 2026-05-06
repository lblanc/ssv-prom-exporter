# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.2.0] - 2026-05-06

### Added
- **Hosts (SAN clients / initiators)** as first-class objects.
  - Inventory collector emits `ssv_host_{state,connection_state,
    maintenance_mode,type,info}` (with labels host_name / description
    / version). Hosts flagged `Internal=true` by SSV (pseudo-hosts
    used for bookkeeping) are skipped.
  - Performance collector fans out `/performance/{host-id}` per host
    and emits `ssv_host_{read,write}_{ops,bytes}_total`,
    `ssv_host_provisioned_bytes`, and three peak gauges
    `ssv_host_max_{read,write,op}_size_bytes`.
- New Grafana dashboard **SSV — Hosts** (`ssv-hosts.json`):
  inventory table (state / maint / IOPS / B/s), per-host IOPS &
  bandwidth time-series, peak IO size, and a provisioned-capacity
  bargauge. Honors the multi-group `Group` filter like the others.

### Notes
- Per-host *latency* is not exposed because SSV's REST does not
  publish time counters under host objects.

[v0.2.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.2.0

## [v0.1.3] - 2026-05-06

### Added
- `ssv_server_info` now also carries `product_name` (the full SKU /
  edition string from SSV's REST `/servers`). Useful for inventory
  panels that want to show the product alongside its version.

### Notes
- This release is the binary backing the Grafana "Server versions"
  table that ships with the test stack under `deploy/`.

[v0.1.3]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.1.3

## [v0.1.2] - 2026-05-06

Initial public release.

### Highlights

- **Three Prometheus collectors** scraping SSV's REST API:
  - `inventory` — server groups, servers, pools, virtual disks,
    capacity, license expiry.
  - `health` — per-resource monitor states, active alert count.
  - `performance` — cumulative byte/op counters and latency timers
    per server, pool and virtual disk; class-tagged latency on the
    server pipeline (`front_end_target`, `mirror_target`,
    `physical_disk`, `pool`, `target`).
- **REST endpoint failover** with auto-discovery from `/servers`,
  sticky preferred endpoint (5 min TTL), CIDR-filtered backup list.
- **Retry/backoff** on transient SSV failures (exponential, capped,
  with jitter, ctx-aware).
- **YAML config** (`-config <path>`) with strict unknown-field
  rejection and clean precedence (flag > env > YAML > default), so
  credentials can live in an ACL'ed file rather than the SCM
  `ImagePath`.
- **Native Windows service**: same `.exe` registers itself with the
  SCM (`-install` / `-uninstall`) and writes to the Event Log; no
  NSSM or wrapper batch file. Cross-compiles cleanly from Linux.

### Install

See the [README](README.md#install) for the end-to-end procedure
(MSI, `config.yaml`, service registration, uninstall).

### Notes

- The MSI installs to `C:\Program Files\ssv-prom-exporter\` (true
  x64 install). It does **not** register the service automatically;
  run `ssv-prom-exporter.exe -install -config <path>` after editing
  `C:\ProgramData\ssv-prom-exporter\config.yaml`.
- SSV `*Time` perf counters are exposed in milliseconds by SSV; the
  exporter multiplies by `1e-3` so all latency series are in seconds
  (Prometheus convention). Verified empirically against PSP 20.

[v0.1.2]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.1.2
