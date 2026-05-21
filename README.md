# ssv-prom-exporter

[![CI](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/ci.yml)
[![Release](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/release.yml/badge.svg)](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/release.yml)

Prometheus exporter for [DataCore SANsymphony](https://www.datacore.com/products/sansymphony/)
(SSV), packaged as a native Windows service.

The exporter scrapes SSV's REST API and exposes inventory, health and
performance metrics on `/metrics`. It runs anywhere on the network with
TCP/443 reachability to a SSV management server — it does **not** need
to run on the SSV host itself.

## Contents

- [Features](#features)
- [Install](#install)
  - [From a GitHub Release (recommended)](#from-a-github-release-recommended)
  - [From source](#from-source)
- [Install on Linux](#install-on-linux)
- [Run with Docker](#run-with-docker)
- [Uninstall](#uninstall)
- [Usage](#usage)
- [Configuration file](#configuration-file)
- [Exposed metrics](#exposed-metrics)
- [Test stack (Prometheus + Grafana)](#test-stack-prometheus--grafana)
  - [Full stack with the exporter](#full-stack-with-the-exporter)
- [Companion tool: prom-clip](#companion-tool-prom-clip)
- [High availability / failover](#high-availability--failover)
- [Windows service](#windows-service)
- [Requirements](#requirements)
- [Notes / gotchas](#notes--gotchas)
- [Cutting a release](#cutting-a-release)
- [License](#license)

## Features

- Three signal tiers, each with its own refresh cadence and
  `ssv_up{collector}` / `ssv_scrape_duration_seconds{collector}`:
  - **Inventory** — server groups, servers, pools, virtual disks,
    SAN-client hosts, SCSI/iSCSI/FC ports, pool-member physical
    disks, capacity, license expiry.
  - **Health** — per-resource monitor states; one detailed entry
    per active alert with level / message / age.
  - **Performance** — cumulative byte/op counters and latency
    timers per server, pool, virtual disk, host, port and physical
    disk. Server tier is broken down by IO pipeline class
    (front-end target / mirror target / back-end / pool / target).
- **Cross-cutting labels** so Grafana ad-hoc filters work
  end-to-end: `host` on every `ssv_port_*`, `pool` and `tier` on
  every `ssv_physical_disk_*`.
- **REST endpoint failover** with auto-discovery from `/servers`,
  sticky preferred endpoint (5 min TTL), CIDR-filtered backup list.
- **Multi-group aware**: server-level metrics are scoped to the local
  SSV group (`OurGroup=true`). Federated peer groups visible through
  `/serverGroups` are kept on group-level metrics but excluded from
  per-server inventory and performance fan-out, so dashboards stay
  free of empty rows for remote nodes.
- **Retry/backoff** on transient SSV failures (exponential, capped,
  with jitter, ctx-aware).
- **YAML config** with strict unknown-field rejection and a clean
  precedence rule (flag > env > YAML > default), letting credentials
  live in an ACL'ed file rather than the SCM `ImagePath`.
- **Native Windows service**: same binary registers itself with the
  SCM (`-install` / `-uninstall`) and writes to the Event Log; no NSSM
  or wrapper batch file. Cross-compiles cleanly from Linux.

## Install

### From a GitHub Release (recommended)

Download the artifacts from
[the latest release](https://github.com/lblanc/ssv-prom-exporter/releases/latest).
Each release ships:

- `ssv-prom-exporter-vX.Y.Z-windows-amd64.exe` — the standalone binary.
- `ssv-prom-exporter-X.Y.Z-x64.msi` — Windows MSI installer.
- `SHA256SUMS` — checksums for both.

The MSI:

- Installs to `C:\Program Files\ssv-prom-exporter\`.
- Drops `ssv-prom-exporter.exe`, `LICENSE.txt`, `config.example.yaml`.
- Creates an empty `C:\ProgramData\ssv-prom-exporter\` (preserved on
  upgrade — the admin places the real `config.yaml` there).
- Does **not** register the Windows service. Service registration is
  a manual step (see [Windows service](#windows-service)) so that the
  config path and credentials never leak into MSI properties.

End-to-end install on a Windows target, from an **elevated** prompt
(replace `X.Y.Z` with the version you downloaded):

```bat
:: 1. Run the MSI (silent or with the standard wizard).
msiexec /i ssv-prom-exporter-X.Y.Z-x64.msi /qn

:: 2. Drop the YAML config in ProgramData and tighten ACLs.
copy "C:\Program Files\ssv-prom-exporter\config.example.yaml" ^
     "C:\ProgramData\ssv-prom-exporter\config.yaml"
notepad "C:\ProgramData\ssv-prom-exporter\config.yaml"
icacls "C:\ProgramData\ssv-prom-exporter\config.yaml" /inheritance:r ^
       /grant:r SYSTEM:F Administrators:F

:: 3. Register and start the service. Only -config lands in the SCM ImagePath.
"C:\Program Files\ssv-prom-exporter\ssv-prom-exporter.exe" ^
  -install -config "C:\ProgramData\ssv-prom-exporter\config.yaml"
sc start ssv-prom-exporter
```

### From source

```sh
make build           # native (the host's GOOS/GOARCH)
make build-linux     # static linux/amd64 (CGO_ENABLED=0)
make build-windows   # cross-compile to windows/amd64 (CGO_ENABLED=0)
make msi             # build the MSI (requires `wixl`, Debian package)
make tarball-linux   # bundle linux binary + systemd unit + LICENSE + config
make docker-build    # build the OCI image locally
```

## Install on Linux

The Linux tarball ships a static binary, a hardened systemd unit, and
a reference `install-linux.sh` that lays everything out. As **root**:

```sh
tar xzf ssv-prom-exporter-vX.Y.Z-linux-amd64.tar.gz
cd     ssv-prom-exporter-vX.Y.Z-linux-amd64
./install-linux.sh
$EDITOR /etc/ssv-prom-exporter/config.yaml   # set url / user / pass
systemctl enable --now ssv-prom-exporter
```

This places:

- `/usr/local/bin/ssv-prom-exporter` — the static binary.
- `/etc/systemd/system/ssv-prom-exporter.service` — the systemd unit.
- `/etc/ssv-prom-exporter/config.yaml` — created from
  `config.example.yaml` on the first install, never overwritten on
  upgrade.

The unit runs as a `DynamicUser`, with `ProtectSystem=strict`,
`NoNewPrivileges`, a `SystemCallFilter`, and tight memory limits.
Output goes to `journald`:

```sh
journalctl --unit ssv-prom-exporter -f
```

## Run with Docker

Pre-built multi-arch images (`linux/amd64` + `linux/arm64`) are
published on GHCR on every release:

```sh
docker run --rm -p 9876:9876 \
    -e SSV_URL=https://10.0.0.1 \
    -e SSV_USER=administrator \
    -e SSV_PASS='ChangeMe!' \
    ghcr.io/lblanc/ssv-prom-exporter:latest
```

Tags published: `vX.Y.Z`, `X.Y`, `latest`. The image is built on
`alpine:3` (~34 MB), runs as nonroot uid 65532, embeds `tini` for
clean SIGTERM forwarding, and ships a `HEALTHCHECK` against
`/metrics`.

To mount a YAML config instead of passing creds through env vars:

```sh
docker run --rm -p 9876:9876 \
    -v /etc/ssv/config.yaml:/etc/ssv-prom-exporter/config.yaml:ro \
    ghcr.io/lblanc/ssv-prom-exporter:latest \
    -config /etc/ssv-prom-exporter/config.yaml
```

## Uninstall

The service registration is independent from the MSI (it's done by
`-install` after the MSI ran), so it must be removed first. From an
**elevated** prompt:

```bat
:: 1. Stop and unregister the service.
sc stop ssv-prom-exporter
"C:\Program Files\ssv-prom-exporter\ssv-prom-exporter.exe" -uninstall

:: 2. Remove the configuration directory (skip to keep the YAML for a reinstall).
rmdir /s /q "C:\ProgramData\ssv-prom-exporter"

:: 3. Uninstall the MSI. Either by file:
msiexec /x ssv-prom-exporter-X.Y.Z-x64.msi /qn
::    or via Control Panel → Programs and Features →
::    "DataCore SANsymphony Prometheus Exporter".
```

If the MSI file is no longer available, find the ProductCode and pass
it to `msiexec`:

```powershell
Get-WmiObject Win32_Product `
  -Filter "Name='DataCore SANsymphony Prometheus Exporter'" |
  Select-Object IdentifyingNumber

msiexec /x {<the-ProductCode-from-above>} /qn
```

## Usage

The binary reads its connection settings from a YAML config file or
from flags / env vars:

| Flag               | Env var             | Description |
|--------------------|---------------------|-------------|
| `-config`          | `SSV_CONFIG`        | Path to a YAML config file (see [Configuration file](#configuration-file)). |
| `-url`             | `SSV_URL`           | SSV REST base URL, e.g. `https://10.0.0.1`. |
| `-user`            | `SSV_USER`          | SSV username (typically a local Windows account). |
| `-pass`            | `SSV_PASS`          | SSV password. |
| `-host`            | `SSV_HOST`          | `ServerHost` header value; defaults to the host of `-url`. |
| `-insecure`        | —                   | Skip TLS verification (default `true`; SSV ships self-signed certs). |
| `-bases`           | `SSV_BASES`         | Comma-separated backup IPs to seed before the first scrape. |
| `-backup-cidrs`    | `SSV_BACKUP_CIDRS`  | CIDR allowlist for discovered backup IPs. Default: primary's `/24` if `-url` is an IPv4. Pass `0.0.0.0/0` to disable. |
| `-retries`         | —                   | Retries on transient failures after every endpoint has been tried once (default `2`). |
| `-retry-delay`     | —                   | Initial backoff between retries (default `200ms`); doubles each attempt, capped at 2 s, with up to 50 % jitter. |
| `-perf-workers`    | —                   | Concurrent `/performance/{id}` calls per scrape (default `8`). |
| `-listen`          | —                   | Listen address for the Prometheus HTTP exporter, e.g. `:9876`. |
| `-ping`            | —                   | Probe `/serverGroups`, print the response, exit. |
| `-install`         | —                   | Register the binary as a Windows service and exit. |
| `-uninstall`       | —                   | Remove the Windows service and exit. |
| `-svc-name`        | —                   | Service name (default `ssv-prom-exporter`). |
| `-svc-display`     | —                   | Service display name shown in `services.msc`. |
| `-svc-description` | —                   | Service description text. |
| `-version`         | —                   | Print version and exit. |

Quick local run:

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -listen :9876
curl http://127.0.0.1:9876/metrics
```

One-shot probe (raw JSON of `/serverGroups`):

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -ping
```

## Configuration file

Pass `-config <path>` to load settings from a YAML file. See
[`config.example.yaml`](config.example.yaml) for the full schema.
Any field can be overridden by an explicit command-line flag (or its
matching env var); unset fields fall through to the binary's defaults.

Precedence:

```
explicit flag  >  env var (flag default)  >  YAML  >  built-in default
```

Unknown YAML keys are rejected at load time so a typo doesn't silently
leave a setting at its default.

## Exposed metrics

Non-exhaustive — see `/metrics` for the live list.

**Scrape framing**

- `ssv_up{collector="inventory"|"health"|"performance"}` — 1 if the
  last scrape of that tier succeeded.
- `ssv_scrape_duration_seconds{collector="..."}` — duration of the
  last scrape, per tier.

**Inventory**

- `ssv_server_group_{state,storage_used_bytes,storage_max_bytes,
  out_of_compliance,license_expires_seconds}`
- `ssv_server_{state,support_state,power_state,cache_state,
  diagnostic_mode,maintenance_mode,storage_used_bytes,
  memory_total_bytes,memory_available_bytes,info}` — `info` carries
  `host_name`, `product_name`, `product_version`, `product_build`,
  `os_version` labels.
- `ssv_pool_{status,presence_status,type,chunk_size_bytes}`
- `ssv_virtual_disk_{status,size_bytes,type,offline}`
- `ssv_host_{state,connection_state,maintenance_mode,type,info}` —
  `info` carries `host_name`, `description`, `version` labels.
- `ssv_port_{connected,role_capability,info}` — `info` carries
  `host`, `port_name`, `alias`, `port_type`, `port_mode`. The
  `host` label is the resolved owner caption (the SDS server for
  back-end ports, the SAN client for front-end ports).
- `ssv_physical_disk_{status,size_bytes,free_bytes,info}` plus a
  relation gauge `ssv_physical_disk_pool{disk_id, pool_id, pool,
  tier}` mapping each pool-member disk to its pool. The `pool` and
  `tier` labels are also stamped directly on every disk metric, so
  PromQL filters and Grafana ad-hoc filters work without joins.

**Health**

- `ssv_monitor_state{monitor_id,template,target_id,caption}`
- `ssv_alerts_total` — count.
- `ssv_alert_info{alert_id, machine_id, machine, level,
  high_priority, needs_ack, caller, message}` — gauge=1 per alert,
  with all SSV fields as labels (Level: 1 = Info, 2 = Warning,
  3 = Error, vendor-defined).
- `ssv_alert_age_seconds{alert_id}` — `now - alert.TimeStamp`.

**Performance — bytes & ops (counters), capacity & cache (gauges)**

Same family per object: `read_bytes_total`, `write_bytes_total`,
`read_ops_total`, `write_ops_total` (plus a few object-specific
extras). Object families:

- `ssv_server_*` — adds `cache_{read,write}_{hits,misses}_total`
  and `cache_{size,free}_bytes`.
- `ssv_pool_*` — adds `capacity_bytes`, `used_bytes`,
  `available_bytes`, `reserved_bytes`, `reclamation_bytes`,
  `oversubscribed_bytes`.
- `ssv_virtual_disk_*` — adds the four `cache_*` counters.
- `ssv_host_*` (SAN clients) — adds `provisioned_bytes` and three
  peak gauges `max_{read,write,op}_size_bytes`.
- `ssv_port_*` — adds per-direction split
  `{target,initiator}_{ops,bytes}_total`,
  `pending_commands` (gauge), and the link-layer error counters
  `{busy,invalid_crc,link_failure,loss_of_signal,loss_of_sync}_total`.
- `ssv_physical_disk_*` — adds `avg_queue_length` and
  `pending_commands` (gauges).

**Performance — latency (seconds)**

SSV exposes `*Time` counters in milliseconds; the exporter
multiplies by `1e-3` so every latency metric is in seconds.

Server tier is split by IO pipeline class
(`front_end_target` / `mirror_target` / `physical_disk` / `pool` /
`target`):

- `ssv_server_class_io_operations_total{class}`
- `ssv_server_class_io_time_seconds_total{class}`
- `ssv_server_class_io_max_time_seconds{class}`

Per-pool / per-vdisk / per-port-target / per-physical-disk:

- `ssv_pool_{read,write,io}_time_seconds_total`
- `ssv_pool_{read,write,io}_max_time_seconds`
- `ssv_virtual_disk_io_time_seconds_total`
- `ssv_virtual_disk_io_max_time_seconds`
- `ssv_port_target_io_time_seconds_total`,
  `ssv_port_target_io_max_time_seconds`
- `ssv_physical_disk_{read,write,io}_time_seconds_total`
- `ssv_physical_disk_{read,write,io}_max_time_seconds`

Average IO latency, in PromQL:

```promql
rate(ssv_server_class_io_time_seconds_total[$__rate_interval])
  /
rate(ssv_server_class_io_operations_total[$__rate_interval])
```

State integers are exposed as-is — the SSV vendor enum mapping is not
documented in the REST help.

## Test stack (Prometheus + Grafana)

A docker-compose stack ships under [`deploy/`](deploy/) for poking at
the exporter end-to-end on a Linux host. It runs Prometheus 3 +
Grafana 12 with five dashboards pre-provisioned.

```sh
cd deploy
cp .env.example .env
$EDITOR .env          # set EXPORTER_TARGETS, see below
docker compose up -d
```

Targets are declared in `EXPORTER_TARGETS` as
`name1=host:port,name2=host:port` — one entry per SANsymphony group.
The name becomes the `group` Prometheus label; every dashboard panel
filters on it via the `Group` template variable, so several SAN
groups can sit side by side in the same Grafana.

```sh
EXPORTER_TARGETS=lab=10.12.110.11:9876,prod=10.0.0.5:9876
```

Then:

- **Grafana** — http://localhost:3000 (anonymous Viewer is enabled,
  no login needed; admin password is `GF_ADMIN_PASSWORD` from `.env`
  for editing). The **SSV** folder holds five dashboards, all
  cross-linked through a "SSV" dropdown that preserves the time
  range and selected filters when navigating between them:
  - **Overview** — global health (scrape, alert details with
    level/server/age, server states, capacity rollups, total IOPS
    & latency, top-N noisy vdisks, active monitors).
  - **Servers** — per-server (repeated): state, cache, IOPS &
    throughput, IOPS & latency by IO pipeline class
    (front-end target / mirror target / back-end / pool / target).
  - **Storage** — per-pool (status, capacity pie, IOPS, latency)
    with a collapsible Physical disks subsection (table + per-disk
    IOPS / throughput / latency / queue), per-vdisk (status, cache
    hit ratio, IOPS, throughput, latency). Filters: Group, Pool,
    Virtual Disk, Physical Disk.
  - **Hosts** — SAN-client inventory + per-host IOPS &
    bandwidth, peak IO size, provisioned capacity, plus a
    Connections (ports) subsection showing the host's ports with
    their IOPS & bandwidth.
  - **Ports** — per-port (table + IOPS + bandwidth + target IO
    latency + pending commands) with a collapsible Errors row
    (link-layer counters).
- **Prometheus** — http://localhost:9090.

Stop the stack with `docker compose down`. Add `-v` to also wipe the
TSDB and Grafana state.

### Full stack with the exporter

For demos and for sites that prefer to run everything containerized,
a `full` compose profile runs the exporter as a fourth service
alongside Prometheus + Grafana. One SSV group, single command, no
host install needed:

```sh
cd deploy
cp .env.example .env
$EDITOR .env          # set SSV_URL / SSV_USER / SSV_PASS / SSV_GROUP
docker compose --profile full up -d --build
```

Prometheus auto-discovers the in-stack exporter (it scrapes
`exporter:9876` on the compose network), and the `group` label is
taken from `SSV_GROUP` (default: `full`). The exporter's
`/metrics` is not published on the host by default — uncomment the
`ports:` block in `deploy/docker-compose.yml` if you want to curl
it locally while running under `--profile full`.

The stack's Prometheus is pull-only by default. To also accept
inbound backfill from [`prom-clip`](#companion-tool-prom-clip), set
`PROM_REMOTE_WRITE=1` in `.env` (optionally with `PROM_OOO_WINDOW=7d`)
before bringing it up. That turns on both
`--web.enable-remote-write-receiver` and a matching
`storage.tsdb.out_of_order_time_window`, without which Prometheus
silently drops any sample older than the current head block.

## Companion tool: prom-clip

A second binary, `prom-clip`, ships in this repo to **clip a time
window from one Prometheus and replay it into another**. The wire
format is gzipped OpenMetrics (`.txt.gz`); the replay path uses
Prometheus's remote-write protocol.

Typical uses:

- snapshot a customer site that runs the exporter, mail / share the
  `.txt.gz`, replay it into a local lab for triage;
- ship a demo dataset alongside the dashboards;
- carry a "before / after" comparison across sites that aren't
  network-connected.

Two modes from the same binary:

```sh
# 1. Web UI on http://127.0.0.1:8088 (loopback only — no Windows
#    Firewall prompt). Ephemeral by default: state lives in RAM,
#    export files stream straight to the browser's Save-As dialog
#    and are removed server-side as soon as the download completes.
prom-clip

# 2. One-shot CLI — no server, no port, no state directory.
prom-clip export -src http://prom-source:9090 \
                 -from -1h -to now -step 30s \
                 -metric '^ssv_.*' \
                 -out snapshot.txt.gz
prom-clip import -dst http://prom-target:9090 \
                 -in snapshot.txt.gz
```

Pass `-state-dir <path>` to opt into persistent mode (last connection
memorised across sessions, exports accumulated, rotation via
`-keep-exports N`). On Windows the persistent directory follows the
OS convention (`%LOCALAPPDATA%\prom-clip`); on Linux/macOS it follows
XDG (`~/.local/state/prom-clip`).

The target Prometheus must run with `--web.enable-remote-write-receiver`
**and** `storage.tsdb.out_of_order_time_window` set wide enough to cover
the imported window. The bundled `deploy/` stack exposes both behind
the `PROM_REMOTE_WRITE` opt-in (see the section above).

## High availability / failover

The exporter falls over to a backup management server when the primary
is unreachable. Mechanics:

- After each successful inventory scrape, every IP from
  `/servers[].IpAddresses` is added to the backup list (filtered by
  `-backup-cidrs`, default = the primary's `/24`).
- On a transient failure (network error, timeout, HTTP 5xx), the next
  endpoint is tried. HTTP 4xx is **not** a failover trigger — it's a
  configuration bug.
- The last-known-good endpoint is sticky for 5 minutes, so during an
  outage only the first call pays the dial-timeout cost. After 5 min
  the next call retries the primary, detecting recovery.
- The `ServerHost` header is rewritten per endpoint (each backup uses
  its own IP); SSV rejects hostname-based `ServerHost` values with
  HTTP 400.
- If every endpoint still fails transiently, the call is retried
  with exponential backoff (`-retries`, `-retry-delay`).

Pass `-bases ip1,ip2,...` to seed the backup list before the first
scrape (useful on cold start before any inventory has been pulled).

## Multi-group SSV deployments

SSV management servers federate their state: a single
`/serverGroups` call returns the local group plus every peer the
group has been linked to, and `/servers` mixes local nodes with
remote ones (the latter carry compound IDs of the form
`<remote-group-uuid>:<server-uuid>` and have most descriptive fields
empty). `/performance/{id}` is local-only.

The exporter therefore only scrapes per-server inventory and
performance for the local group, identified by `OurGroup=true` in
`/serverGroups`. Group-level metrics (`ssv_server_group_*`) keep
every peer so you can still alert on a federated group going
unreachable, but `ssv_server_*`, `ssv_server_class_*` and the
failover IP pool are scoped to the local nodes.

Practical consequence: **run one exporter per SSV group**. The
`EXPORTER_TARGETS` Prometheus generator already supports this
shape:

```env
EXPORTER_TARGETS=HCI104=10.12.104.121:9876,HCI130=10.12.130.121:9876
```

Each entry becomes a Prometheus `job_name` with a `group=<name>`
label, and the Grafana dashboards filter on `$group` end-to-end.

## Windows service

The exporter is a native Windows service: the same `.exe` runs
interactively from a console or under the SCM, picked at startup.

End-to-end install via the MSI is documented in [Install](#install).
For an install from a hand-copied binary, from an **elevated** prompt:

```bat
:: 1. Drop config.yaml under a directory only Administrators can read.
mkdir C:\ProgramData\ssv-prom-exporter
copy config.example.yaml C:\ProgramData\ssv-prom-exporter\config.yaml
notepad C:\ProgramData\ssv-prom-exporter\config.yaml
icacls C:\ProgramData\ssv-prom-exporter\config.yaml /inheritance:r ^
       /grant:r SYSTEM:F Administrators:F

:: 2. Register the service. Only -config lands in the SCM ImagePath.
ssv-prom-exporter.exe -install -config C:\ProgramData\ssv-prom-exporter\config.yaml
sc start ssv-prom-exporter
```

This:

- Registers a service named `ssv-prom-exporter` (configurable via
  `svc_name` in YAML or `-svc-name`), starting automatically as
  `LocalSystem`.
- Bakes only the explicitly-set flags into the SCM ImagePath. With the
  config-file workflow above, that's just `-config <path>`, so
  credentials stay in the ACL'ed YAML and out of `sc qc`.
- Registers an Event Log source under the service name; service-mode
  logs land in **Windows Logs → Application** filtered on that source.

Plain command-line install still works (e.g. for quick tests):

```bat
ssv-prom-exporter.exe -install -url https://10.0.0.1 -user administrator ^
                      -pass S3cret! -listen :9876
```

In that case the binary prints a warning that `-pass` is now visible
via `sc qc`.

Manage with the standard tools:

```bat
sc start  ssv-prom-exporter
sc stop   ssv-prom-exporter
sc query  ssv-prom-exporter
services.msc
```

Uninstall:

```bat
ssv-prom-exporter.exe -uninstall
```

> **Security note.** Anything passed on the install-time command line
> ends up in the SCM `ImagePath`, readable by local admins via
> `sc qc <name>`. Use the YAML config workflow above to keep `-pass`
> out of that surface.

## Requirements

- DataCore SANsymphony **PSP 20+** (older versions may work, untested).
- Network reachability from the exporter host to the SSV management
  server on TCP/443.
- A target host running:
  - Windows (any version supported by SSV PSP 20+) for the native
    Windows service deployment;
  - any reasonably modern Linux distribution with `systemd` for the
    Linux systemd deployment (kernel 4.x+, `glibc` not required —
    the binary is fully static);
  - or any container host (Linux, Docker Desktop, Kubernetes node…)
    for the OCI image deployment.
- A Windows or Linux **build** host (Go 1.26+). MSI builds also need
  the `wixl` Debian package (`apt install wixl`); Docker images
  additionally need Docker Buildx (for multi-arch).

## Notes / gotchas

- The `ServerHost` HTTP header is mandatory on every REST call; missing
  it returns `HTTP 400` with `ErrorCode 9`. The value must be the IP
  being hit — hostnames are rejected with HTTP 400 even when they
  resolve to a valid mgmt server.
- Some SSV IDs contain colons and curly braces (notably pool IDs of the
  form `<server-uuid>:{<pool-uuid>}`); they must be path-escaped before
  being interpolated into URLs.
- `/performance/{id}` returns an array containing a single snapshot —
  unwrap `[0]`.
- SSV's REST cache is 30 s by default (`RequestExpirationTime` in
  `Web.config` on the mgmt server). Faster scrapes will see stale data.
- SSV `*Time` perf counters are exposed in milliseconds; the exporter
  multiplies by `1e-3` so all latency series are in seconds (Prometheus
  convention).

## Cutting a release

Releases are produced by GitHub Actions when an annotated tag matching
`v*` is pushed. The release body is taken from the matching section in
[`CHANGELOG.md`](CHANGELOG.md) (auto-generated commit/PR notes are
appended below it). To cut a new version:

```sh
# 1. Add a `## [vX.Y.Z] - YYYY-MM-DD` section to CHANGELOG.md and commit.
$EDITOR CHANGELOG.md
git commit -am "CHANGELOG: vX.Y.Z"
git push

# 2. Tag and push.
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

The [release workflow](.github/workflows/release.yml) installs `wixl`,
runs `make msi VERSION=vX.Y.Z`, computes SHA-256 sums, extracts the
`vX.Y.Z` section from `CHANGELOG.md`, and creates a GitHub Release
carrying the windows binary, the MSI, and `SHA256SUMS`.

Existing releases can be regenerated (e.g. to refresh the body after a
`CHANGELOG.md` edit) by running the workflow manually with
`workflow_dispatch` and the existing tag as input.

## License

[MIT](LICENSE).
