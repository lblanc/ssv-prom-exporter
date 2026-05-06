# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.6.0] - 2026-05-06

### Added
- **Cross-cutting labels for Grafana ad-hoc / "Filter by label values"**.
  Until now, filtering a dashboard by a high-level label (e.g.
  `host=esx101...` on the Hosts board) caused the port panels to go
  blank — `ssv_port_*` had `host_id` but no `host` label, so the
  injected matcher returned an empty vector. Two new cross-cutting
  labels are now stamped at emit time:
  - `host` on `ssv_port_info`, `ssv_port_connected`,
    `ssv_port_role_capability`, and every `ssv_port_*` perf metric.
    Resolved from `port.HostId` against `/hosts` (external SAN
    clients) and `/servers` (the SDS machines themselves).
  - `pool` and `tier` on `ssv_physical_disk_*` (status / size /
    free / info plus every perf metric). Resolved from
    `physicalDisk.PoolMemberId` -> `/poolMembers` -> `/pools`.

### Changed
- Hosts and Storage dashboards drop the `* on(host_id) group_left(host)`
  and `and on(disk_id) ssv_physical_disk_pool{...}` joins; queries now
  filter directly via the new labels. Simpler PromQL, and Grafana's
  "Filter by label values" pill now works on every panel.

[v0.6.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.6.0

## [v0.5.0] - 2026-05-06

### Added
- **Per-alert detail metrics**. The exporter previously emitted only
  the count `ssv_alerts_total`; it now also emits one
  `ssv_alert_info{alert_id, machine_id, machine, level,
  high_priority, needs_ack, caller, message}` gauge per active
  alert plus an `ssv_alert_age_seconds{alert_id}` gauge derived
  from the alert's TimeStamp. SSV's `Level`: 1 = Info,
  2 = Warning, 3 = Error.
- New `ssv.Alert` type and `Client.Alerts()` (replacing the old
  `AlertsCount`).

### Changed
- **Overview dashboard**: new "Active alerts" row with a table
  showing every alert (level / server / source / message / age),
  sorted by age. The "Active Alerts" stat now carries a click-link
  that focuses the table.
- **Storage dashboard**: the per-pool physical-disk section
  (table + 4 time-series) now lives in a row collapsed by default
  ("Physical disks of \$pool — click to expand") so the at-a-glance
  pool view stays compact.

[v0.5.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.5.0

## [v0.4.0] - 2026-05-06

### Added
- **Physical disks (pool members)** as first-class objects.
  - New `ssv.PhysicalDisk` + `ssv.PoolMember` types and matching
    client methods (`Client.PhysicalDisks`, `Client.PoolMembers`).
  - Inventory collector filters `/physicalDisks` down to `Type==4`
    (real pool members; the rest of `/physicalDisks` is mirror
    pseudo-disks, system / boot disks, and client virtual disks)
    and emits `ssv_physical_disk_{status,size_bytes,free_bytes,
    info}` plus a relation gauge
    `ssv_physical_disk_pool{disk_id, pool_id, pool, tier}` joining
    the perf series to a pool by `disk_id`.
  - Performance collector emits cumulative ops / bytes
    (`physical_disk_{read,write}_{ops,bytes}_total`), per-direction
    time (`physical_disk_{read,write,io}_time_seconds_total`,
    note: `TotalReadsTime` / `TotalWritesTime` with the 's',
    differs from the pool spelling), and gauges
    `physical_disk_{io,read,write}_max_time_seconds`,
    `physical_disk_avg_queue_length`,
    `physical_disk_pending_commands`.
- Storage dashboard: per-pool **Physical disks** table (disk, tier,
  status, size, IOPS, avg latency) plus IOPS and latency
  time-series scoped to the pool via
  `... and on(disk_id) ssv_physical_disk_pool{pool=~"$pool"}`.

[v0.4.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.4.0

## [v0.3.0] - 2026-05-06

### Added
- **Ports (SCSI / iSCSI / FC)** as first-class objects.
  - Inventory emits `ssv_port_{connected,role_capability,info}`. The
    `info` labels carry `port_name`, `alias`, `port_type`,
    `port_mode`, and `host_id` (which links to `ssv_host_info`).
    `Internal=true` ports are skipped.
  - Performance fans out `/performance/{port-id}` per port. Metrics:
    aggregate IO (`ssv_port_{read,write}_{ops,bytes}_total`,
    `ssv_port_pending_commands`); per-direction split
    (`ssv_port_{target,initiator}_{ops,bytes}_total`); target latency
    (`ssv_port_target_io_time_seconds_total`,
    `ssv_port_target_io_max_time_seconds`); link-layer error
    counters (`ssv_port_{busy,invalid_crc,link_failure,
    loss_of_signal,loss_of_sync}_total`).
- New Grafana dashboard **SSV — Ports** (`ssv-ports.json`):
  inventory table, IOPS & bandwidth per port, target IO latency,
  pending commands, and a collapsible Errors row plotting all the
  link-layer counters together.

### Changed
- The dashboard `Group` variable is now single-select. Pick one SAN
  group at a time — multi-group analysis is rare in practice and
  having `All` selected by default surfaced confusing aggregates.

[v0.3.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.3.0

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
