# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.8.1] - 2026-05-20

### Fixed
- **Federated server groups no longer pollute the inventory.** SSV
  exposes the full peer topology via `/serverGroups` and `/servers` —
  on a multi-group install, the local exporter would emit
  `ssv_server_*` series for nodes belonging to a remote group
  (compound IDs of the form `<remote-group-uuid>:<server-uuid>`),
  yet `/performance/{id}` returns no data for them so the
  per-server panels stayed empty in Grafana. The client now exposes
  `LocalServers(ctx)` which keeps only the servers whose `GroupID`
  matches the `OurGroup=true` group; both the inventory and
  performance collectors switched to it, and the failover IP pool
  is restricted to local nodes accordingly. Surfaced on the HCI104
  lab (showing HCI130's Hyper-V hosts) on 2026-05-20. Two regression
  tests added: `TestLocalServers_FiltersForeignGroup`,
  `TestLocalServers_NoOurGroupKeepsAll`. Group-level metrics
  (`ssv_server_group_*`) still expose every visible group so peer
  reachability remains observable.

## [v0.8.0] - 2026-05-20

### Fixed
- **Session reauth on stale-token 400.** SANsymphony returns HTTP 400
  with body `"Passed token is not valid for this connection."` when a
  cached session token is invalidated server-side — not 401 as the
  documented expiry path suggests. The client's stale-session detector
  was only matching 401, so after server-side invalidation both labs
  silently stopped collecting (`ssv_up=0` on all three collectors,
  fast-fail in < 25 ms). `isUnauthorized` has been renamed to
  `isStaleSession` and now matches either 401 or 400 carrying the
  stale-token marker. The throttle response (`"Too many requests
  with wrong credentials/token. You must wait N seconds…"`) is
  deliberately NOT matched — retrying on it would escalate the
  server-side throttle. Two regression tests added:
  `TestSession_ReauthsOnStale400`, `TestSession_Throttle400DoesNotReopen`.
  This is the same bug `ssa-collector` hit in its 30 h lab run on
  2026-05-17 (fixed there in `bac55f9`).

### Added
- **Linux support.** Static `linux/amd64` binary, systemd unit
  (`packaging/linux/ssv-prom-exporter.service`) with full sandbox
  flags (DynamicUser, NoNewPrivileges, ProtectSystem=strict,
  SystemCallFilter=@system-service, RestrictAddressFamilies, memory /
  task limits), and a reference `install-linux.sh` that lays out
  `/usr/local/bin/ssv-prom-exporter` +
  `/etc/ssv-prom-exporter/config.yaml` + the unit file in one step.
  New `make build-linux` and `make tarball-linux` targets; the release
  workflow now attaches `ssv-prom-exporter-vX.Y.Z-linux-amd64.tar.gz`
  alongside the Windows binary and MSI.
- **Docker image.** Multi-stage `Dockerfile` (alpine builder →
  alpine runtime, ~34 MB final), runs as nonroot uid 65532, listens on
  `:9876`, embeds `tini` for clean SIGTERM forwarding and a `wget`
  `HEALTHCHECK` against `/metrics`. New `make docker-build` /
  `make docker-push` targets pushing to
  `ghcr.io/lblanc/ssv-prom-exporter:{vX.Y.Z,latest,X.Y}`. CI gained a
  `docker build` step (no push) so the Dockerfile is gated on every
  push; the release workflow builds and pushes a multi-arch
  (`linux/amd64` + `linux/arm64`) image on every tag.
- **Full-stack `docker compose`.** New `full` compose profile in
  `deploy/docker-compose.yml` runs the exporter as a fourth service
  alongside Prometheus + Grafana. One-command demo against a single
  SSV group:
      `SSV_URL=... SSV_USER=... SSV_PASS=... \`
      `docker compose --profile full up -d --build`
  Without the profile, the historical multi-target external-exporter
  flow (`EXPORTER_TARGETS=name=host:port,...`) is unchanged.

### Changed
- `deploy/.env.example` now documents both scenarios (external
  exporters vs full-stack) side by side.
- README gains "Install on Linux", "Run with Docker", and "Run the
  full stack" sections.

[v0.8.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.8.0

## [v0.7.0] - 2026-05-07

### Added
- **Session-based auth** in the REST client. Each
  `(baseURL, ServerHost)` endpoint opens `/sessions` once with the
  literal `Basic <user> <pass>` form (NOT base64), caches the token,
  and rides every subsequent request on
  `Authorization: Token <token>` (NOT `Bearer`). Transparent
  re-auth on HTTP 401; explicit revocation on shutdown via
  `Client.Close()`. `SetBackups` now preserves session tokens for
  endpoints that survive the rebuild (matched on
  `(baseURL, ServerHost)`).
- **JSON-fault parsing** on `HTTPError`. SSV's WCF
  `{"ErrorCode": int, "ErrorMessage": string}` payload lands on the
  typed `Code` / `Message` fields and surfaces in `Error()`; the
  raw body is still kept for non-JSON intermediaries.
- **NullCounterMap honored explicitly** in `Performance()` — both
  the object (`{name: bool}`) and array (`[name, ...]`) shapes are
  accepted defensively, so counters the SSV API flags as
  unavailable are skipped instead of emitted as fake zeros.

### Tests
- 10 / 10 green. Five new tests cover token caching + `Basic u p`
  literal auth, 401 reauth-and-retry, persistent 401 propagation,
  JSON-fault parsing, and NullCounterMap filtering.

[v0.7.0]: https://github.com/lblanc/ssv-prom-exporter/releases/tag/v0.7.0

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
