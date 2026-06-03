# ssv-prom-exporter

[![CI](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/ci.yml)
[![Release](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/release.yml/badge.svg)](https://github.com/lblanc/ssv-prom-exporter/actions/workflows/release.yml)

Prometheus exporter for [DataCore SANsymphony](https://www.datacore.com/products/sansymphony/)
(SSV). It scrapes SSV's REST API and exposes inventory, health and
performance metrics on `/metrics`. It runs anywhere with TCP/443
reachability to a SSV management server — it does **not** have to run on
the SSV host itself.

**This README is Docker-Compose-first.** The fastest way to get a
working SANsymphony monitoring stack — Prometheus, Grafana, five
pre-built dashboards, and optionally the exporter and the `prom-clip`
companion — is the bundled [`deploy/`](deploy/) compose project. Native
Windows-service and Linux-systemd installs are documented further down
for production deployments that don't want a container runtime.

## Contents

- [TL;DR](#tldr)
- [The compose stack at a glance](#the-compose-stack-at-a-glance)
- [Scenario A — monitor exporters that already run elsewhere](#scenario-a--monitor-exporters-that-already-run-elsewhere)
- [Scenario B — run everything in the stack (`--profile full`)](#scenario-b--run-everything-in-the-stack---profile-full)
- [Scenario C — add the prom-clip companion (`--profile clip`)](#scenario-c--add-the-prom-clip-companion---profile-clip)
- [`.env` reference](#env-reference)
- [Exporter configuration (env vars & flags)](#exporter-configuration-env-vars--flags)
- [Compose command cheat-sheet](#compose-command-cheat-sheet)
- [What you get in Grafana](#what-you-get-in-grafana)
- [Exposed metrics](#exposed-metrics)
- [prom-clip — clip & replay a time window](#prom-clip--clip--replay-a-time-window)
- [Running the exporter without compose](#running-the-exporter-without-compose)
  - [Plain `docker run`](#plain-docker-run)
  - [Linux (systemd)](#linux-systemd)
  - [Windows (native service / MSI)](#windows-native-service--msi)
- [High availability / failover](#high-availability--failover)
- [Multi-group SSV deployments](#multi-group-ssv-deployments)
- [Requirements](#requirements)
- [Notes / gotchas](#notes--gotchas)
- [Releases](#releases)
- [License](#license)

## TL;DR

You already run one or more `ssv-prom-exporter` instances and just want
the Prometheus + Grafana dashboards:

```sh
git clone https://github.com/lblanc/ssv-prom-exporter.git
cd ssv-prom-exporter/deploy
cp .env.example .env
# point EXPORTER_TARGETS at your exporter(s), e.g.
#   EXPORTER_TARGETS=lab=10.12.110.11:9876,prod=10.0.0.5:9876
$EDITOR .env
docker compose up -d
```

Grafana → http://localhost:3000 (anonymous read-only, no login).
Prometheus → http://localhost:9090.

You don't run an exporter yet and want the stack to host it too:

```sh
cd ssv-prom-exporter/deploy
cp .env.example .env
# set SSV_URL / SSV_USER / SSV_PASS / SSV_GROUP in .env
$EDITOR .env
docker compose --profile full up -d --build
```

That's it — the sections below expand each scenario with full `.env`
and command examples.

## The compose stack at a glance

Everything lives under [`deploy/`](deploy/). The stack is built around
**three optional services selected by compose profiles**, so you only
run what you need:

| Service       | Profile     | Host port            | Role |
|---------------|-------------|----------------------|------|
| `prometheus`  | *(always)*  | `9090`               | Scrapes the exporter(s); 15 d retention. |
| `grafana`     | *(always)*  | `3000`               | Five SSV dashboards, anonymous Viewer on. |
| `exporter`    | `full`      | *(internal only)*    | Runs `ssv-prom-exporter` itself against one SSV group. |
| `prom-clip`   | `clip`      | `127.0.0.1:8088`     | Clip a Prometheus time window and replay it elsewhere. |

Profiles compose freely — `--profile full --profile clip` brings up all
four. Without a profile flag you get just Prometheus + Grafana, which is
**Scenario A**.

```
                       deploy/docker-compose.yml
   ┌──────────────────────────────────────────────────────────────┐
   │  prometheus:9090 ──────────► grafana:3000  (5 dashboards)      │
   │       ▲  scrape                                                │
   │       │                                                        │
   │   ┌───┴────────────┐        ┌───────────────┐                 │
   │   │ exporter:9876  │        │ prom-clip:8088 │                 │
   │   │ (--profile full)│       │ (--profile clip)│                │
   │   └───┬────────────┘        └───────────────┘                 │
   └───────┼────────────────────────────────────────────────────────┘
           │ REST/443
           ▼
     SANsymphony mgmt server(s)
```

## Scenario A — monitor exporters that already run elsewhere

This is the **default profile**. The exporter runs on each SAN host (as
a Windows service / systemd unit / standalone container); the compose
stack only adds Prometheus + Grafana and scrapes those exporters over
the network.

`deploy/.env`:

```env
# One entry per SANsymphony group: "groupname=host:port,...".
# groupname becomes the Prometheus `group` label the dashboards filter on.
EXPORTER_TARGETS=LAB-PVE=10.12.110.11:9876,HCI104=10.12.104.121:9876

# Grafana admin password (anonymous Viewer is always on; this gates editing).
GF_ADMIN_PASSWORD=admin
```

Bring it up:

```sh
cd deploy
docker compose up -d
docker compose ps
```

Verify Prometheus picked up every target:

```sh
curl -s http://localhost:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.group // "-")\t\(.scrapeUrl)\t\(.health)"'
# LAB-PVE   http://10.12.110.11:9876/metrics   up
# HCI104    http://10.12.104.121:9876/metrics  up
```

Open Grafana on http://localhost:3000 and pick a group from the
**Group** dropdown on any dashboard.

A single target is fine too:

```env
EXPORTER_TARGETS=lab=10.12.110.11:9876
```

## Scenario B — run everything in the stack (`--profile full`)

For demos, or for sites that prefer to run everything containerized: the
`full` profile adds the `exporter` service to the stack, so you don't
install anything on the SAN host. Prometheus auto-discovers it on the
compose network (`exporter:9876`).

`deploy/.env`:

```env
# --- exporter against one SSV group ---
SSV_URL=https://10.12.110.11        # SSV REST base URL (the mgmt server)
SSV_USER=administrator
SSV_PASS=ChangeMe!
SSV_GROUP=lab                       # becomes the Prometheus `group` label

# Optional failover knobs (see "High availability" below)
# SSV_BASES=10.12.110.12,10.12.110.13
# SSV_BACKUP_CIDRS=10.12.110.0/24

# Leave EXPORTER_TARGETS unset — Prometheus auto-targets exporter:9876.

GF_ADMIN_PASSWORD=admin
```

Bring it up (the `--build` builds the exporter image locally from the
repo; drop it to pull `ghcr.io/lblanc/ssv-prom-exporter:latest`
instead):

```sh
cd deploy
docker compose --profile full up -d --build
```

Watch the exporter come alive:

```sh
docker compose logs -f exporter
docker compose exec exporter wget -qO- http://127.0.0.1:9876/metrics | grep '^ssv_up'
# ssv_up{collector="health"} 1
# ssv_up{collector="inventory"} 1
# ssv_up{collector="performance"} 1
```

`/metrics` is internal by default. To curl it from the host while in
`--profile full`, uncomment the `ports:` block on the `exporter`
service in `deploy/docker-compose.yml`:

```yaml
  exporter:
    # ...
    ports:
      - "9876:9876"
```

Pin a released image instead of building locally:

```env
EXPORTER_IMAGE=ghcr.io/lblanc/ssv-prom-exporter:v0.8.1
```

```sh
docker compose --profile full up -d        # no --build → pulls EXPORTER_IMAGE
```

## Scenario C — add the prom-clip companion (`--profile clip`)

`prom-clip` clips a time window out of one Prometheus and replays it
into another (gzipped OpenMetrics out, remote-write in). The `clip`
profile runs its web UI alongside the stack, on host loopback
`127.0.0.1:8088` (no Windows Firewall prompt, no external exposure).

```sh
cd deploy
docker compose --profile clip up -d --build
# UI: http://127.0.0.1:8088
```

Combine with `--profile full` to run the exporter, Prometheus, Grafana
**and** prom-clip together:

```sh
docker compose --profile full --profile clip up -d --build
```

From the UI, reach the stack's own Prometheus at
`http://prometheus:9090` (compose network) or a Prometheus running on
the docker host at `http://host.docker.internal:9090`.

To **import** a clip back into the stack's Prometheus, it must accept
remote-write. That is an opt-in (`deploy/.env`):

```env
PROM_REMOTE_WRITE=1     # turns on --web.enable-remote-write-receiver
PROM_OOO_WINDOW=7d      # out-of-order window; old samples land instead of
                        # being silently dropped. MUST be set for backfill.
```

```sh
docker compose up -d    # re-create prometheus with the receiver enabled
```

Without `PROM_OOO_WINDOW`, any sample older than the ~2 h head block is
discarded while the write still returns `200 OK` — the import looks
successful but lands nothing. See [prom-clip](#prom-clip--clip--replay-a-time-window).

## `.env` reference

All knobs read by `deploy/docker-compose.yml`. Copy `deploy/.env.example`
to `deploy/.env` and edit. Everything is optional except the per-scenario
essentials called out above.

| Variable            | Used by profile | Default                | Description |
|---------------------|-----------------|------------------------|-------------|
| `EXPORTER_TARGETS`  | default         | `lab=host.docker.internal:9876` | Comma list `name=host:port`. Each `name` becomes the `group` label. |
| `SSV_URL`           | `full`          | *(required)*           | SSV REST base URL, e.g. `https://10.0.0.1`. |
| `SSV_USER`          | `full`          | *(required)*           | SSV username. |
| `SSV_PASS`          | `full`          | *(required)*           | SSV password. |
| `SSV_GROUP`         | `full`          | `full`                 | Label applied to the in-stack exporter's metrics. |
| `SSV_BASES`         | `full`          | *(empty)*              | Cold-start backup IPs (comma list) seeded before the first scrape. |
| `SSV_BACKUP_CIDRS`  | `full`          | primary's `/24`        | CIDR allowlist for discovered backup IPs. `0.0.0.0/0` disables filtering. |
| `EXPORTER_IMAGE`    | `full`          | `ghcr.io/lblanc/ssv-prom-exporter:latest` | Image to run when not using `--build`. |
| `GF_ADMIN_PASSWORD` | always          | `admin`                | Grafana admin password (editing only; viewing is anonymous). |
| `PROM_REMOTE_WRITE` | always          | *(off)*                | Set to `1` to accept inbound remote-write (backfill from prom-clip). |
| `PROM_OOO_WINDOW`   | always          | `7d`                   | Out-of-order storage window; required for backfill to land. |
| `PROM_CLIP_IMAGE`   | `clip`          | `ghcr.io/lblanc/prom-clip:latest` | Image for the prom-clip service when not using `--build`. |

## Exporter configuration (env vars & flags)

When the exporter runs **inside the stack** (`--profile full`) it is
configured through the `SSV_*` env vars above. When run **standalone**
(docker run, systemd, Windows service) the same settings are available
as env vars and/or CLI flags, and can also come from a YAML config.

| Flag               | Env var             | Description |
|--------------------|---------------------|-------------|
| `-config`          | `SSV_CONFIG`        | Path to a YAML config file (`config.example.yaml` has the full schema). |
| `-url`             | `SSV_URL`           | SSV REST base URL, e.g. `https://10.0.0.1`. |
| `-user`            | `SSV_USER`          | SSV username. |
| `-pass`            | `SSV_PASS`          | SSV password. |
| `-host`            | `SSV_HOST`          | `ServerHost` header; defaults to the host of `-url`. |
| `-insecure`        | —                   | Skip TLS verification (default `true`; SSV ships self-signed certs). |
| `-bases`           | `SSV_BASES`         | Comma-separated backup IPs seeded before the first scrape. |
| `-backup-cidrs`    | `SSV_BACKUP_CIDRS`  | CIDR allowlist for discovered backups. Default: primary's `/24`. `0.0.0.0/0` disables. |
| `-retries`         | —                   | Retries after every endpoint has failed transiently once (default `2`). |
| `-retry-delay`     | —                   | Initial backoff (default `200ms`); doubles, capped 2 s, ±50 % jitter. |
| `-perf-workers`    | —                   | Concurrent `/performance/{id}` calls per scrape (default `8`). |
| `-listen`          | —                   | Exporter HTTP listen address, e.g. `:9876`. |
| `-ping`            | —                   | Probe `/serverGroups`, print the response, exit. |
| `-install` / `-uninstall` | —            | Register / remove the Windows service and exit. |
| `-svc-name` / `-svc-display` / `-svc-description` | — | Windows service identity overrides. |
| `-version`         | —                   | Print version and exit. |

Precedence when several sources set the same value:

```
explicit flag  >  env var (flag default)  >  YAML  >  built-in default
```

Unknown YAML keys are rejected at load time, so a typo can't silently
leave a setting at its default.

## Compose command cheat-sheet

All commands run from `deploy/`.

```sh
# Bring up / tear down
docker compose up -d                                  # Scenario A
docker compose --profile full up -d --build           # Scenario B
docker compose --profile full --profile clip up -d --build  # B + C
docker compose down                                   # stop, keep data
docker compose down -v                                # stop AND wipe TSDB + Grafana

# Status & logs
docker compose ps
docker compose logs -f prometheus
docker compose logs -f exporter         # only with --profile full

# Apply an .env change to one service
docker compose up -d --force-recreate prometheus

# Rebuild only the exporter image after a code change
docker compose --profile full build exporter
docker compose --profile full up -d exporter

# Reload Prometheus config without a restart (lifecycle API is enabled)
curl -X POST http://localhost:9090/-/reload

# Poke the exporter from inside the stack
docker compose exec exporter wget -qO- http://127.0.0.1:9876/metrics | head
```

## What you get in Grafana

Grafana is provisioned with a datasource and **five dashboards** in the
**SSV** folder, all cross-linked through an "SSV" dropdown that
preserves the time range and selected filters as you move between them.
Every panel is filtered by a **Group** template variable, so several
SANsymphony groups can sit side by side.

- **Overview** — global health: scrape status, active alerts (level /
  server / age), server states, capacity rollups, total IOPS & latency,
  top-N noisy vdisks, active monitors.
- **Servers** — per-server (repeated row): state, cache, IOPS &
  throughput, and IOPS & latency broken down by IO pipeline class
  (front-end target / mirror target / back-end / pool / target).
- **Storage** — per-pool (status, capacity, IOPS, latency) with a
  collapsible Physical-disks subsection (table + per-disk IOPS /
  throughput / latency / queue), and per-vdisk (status, cache hit ratio,
  IOPS, throughput, latency).
- **Hosts** — SAN-client inventory + per-host IOPS & bandwidth, peak IO
  size, provisioned capacity, plus a Connections subsection for the
  host's ports.
- **Ports** — per-port (table + IOPS + bandwidth + target IO latency +
  pending commands) with a collapsible Errors row (link-layer counters).

Anonymous access is read-only Viewer. To edit, log in as `admin` with
`GF_ADMIN_PASSWORD`.

## Exposed metrics

Non-exhaustive — see `/metrics` for the live list.

**Scrape framing**

- `ssv_up{collector="inventory"|"health"|"performance"}` — 1 if the last
  scrape of that tier succeeded.
- `ssv_scrape_duration_seconds{collector="..."}` — last scrape duration, per tier.

**Inventory** — `ssv_server_group_*`, `ssv_server_*` (with an `info`
series carrying `host_name` / `product_name` / `product_version` /
`product_build` / `os_version`), `ssv_pool_*`, `ssv_virtual_disk_*`,
`ssv_host_*`, `ssv_port_*` (`info` carries the resolved owner `host`),
`ssv_physical_disk_*` plus a `ssv_physical_disk_pool{disk_id, pool_id,
pool, tier}` relation gauge (the `pool`/`tier` labels are also stamped
on every disk metric so PromQL filters need no joins).

**Health** — `ssv_monitor_state{...}`, `ssv_alerts_total`,
`ssv_alert_info{alert_id, machine, level, message, ...}` (gauge=1 per
alert), `ssv_alert_age_seconds{alert_id}`.

**Performance** — per object: `read_bytes_total`, `write_bytes_total`,
`read_ops_total`, `write_ops_total`, plus per-object extras
(server cache hits/misses + cache size/free; pool capacity / used /
available / reserved / reclamation / oversubscribed; vdisk cache
counters; host provisioned + peak IO sizes; port per-direction split +
link-layer error counters; physical-disk queue + pending commands).

**Performance — latency (seconds).** SSV exposes `*Time` counters in
milliseconds; the exporter multiplies by `1e-3`. The server tier is
split by IO pipeline class:

```promql
# average IO latency per class
rate(ssv_server_class_io_time_seconds_total[$__rate_interval])
  /
rate(ssv_server_class_io_operations_total[$__rate_interval])
```

Pool / vdisk / port-target / physical-disk latency follow the same
`*_time_seconds_total` + `*_max_time_seconds` shape.

## prom-clip — clip & replay a time window

`prom-clip` ships in this repo as a second binary. It **clips a time
window from one Prometheus and replays it into another** — to hand a
dataset to support, seed a demo, or carry a before/after comparison
between sites that aren't network-connected. Wire format: gzipped
OpenMetrics (`.txt.gz`); replay uses the remote-write protocol.

The compose `clip` profile is covered in
[Scenario C](#scenario-c--add-the-prom-clip-companion---profile-clip).
Standalone, the same binary has two modes:

```sh
# 1. Web UI on http://127.0.0.1:8088 (loopback only). Ephemeral by
#    default: state in RAM, exports stream to the browser's Save-As and
#    are removed server-side as soon as the download completes.
prom-clip

# 2. One-shot CLI — no server, no port, no state directory.
prom-clip export -src http://prom-source:9090 \
                 -from -1h -to now -step 30s \
                 -metric '^ssv_.*' \
                 -out snapshot.txt.gz
prom-clip import -dst http://prom-target:9090 \
                 -in snapshot.txt.gz
```

`-from`/`-to` accept RFC3339, a Go duration (`-1h` = "1 hour ago"), or
`now`. Pass `-state-dir <path>` to opt into persistent mode (last
connection remembered, exports kept, rotation via `-keep-exports N`).

A pre-built multi-arch image is published on GHCR:

```sh
docker run --rm -p 127.0.0.1:8088:8088 \
    -v prom-clip-state:/var/lib/prom-clip \
    ghcr.io/lblanc/prom-clip:latest
```

> **Import requires** the target Prometheus to run with
> `--web.enable-remote-write-receiver` **and**
> `storage.tsdb.out_of_order_time_window` set wide enough to cover the
> imported window. The bundled stack exposes both behind
> `PROM_REMOTE_WRITE` (see Scenario C).

## Running the exporter without compose

The compose `exporter` service is convenient, but production deployments
usually run the exporter as a long-lived service on each SAN host.

### Plain `docker run`

```sh
docker run --rm -p 9876:9876 \
    -e SSV_URL=https://10.0.0.1 \
    -e SSV_USER=administrator \
    -e SSV_PASS='ChangeMe!' \
    ghcr.io/lblanc/ssv-prom-exporter:latest
```

Or mount a YAML config instead of passing creds through env:

```sh
docker run --rm -p 9876:9876 \
    -v /etc/ssv/config.yaml:/etc/ssv-prom-exporter/config.yaml:ro \
    ghcr.io/lblanc/ssv-prom-exporter:latest \
    -config /etc/ssv-prom-exporter/config.yaml
```

Multi-arch images (`linux/amd64` + `linux/arm64`) are published on every
release as `vX.Y.Z`, `X.Y` and `latest`. ~34 MB, nonroot uid 65532,
`tini` for clean SIGTERM, `HEALTHCHECK` on `/metrics`.

### Linux (systemd)

The Linux tarball ships a static binary, a hardened systemd unit, and a
reference `install-linux.sh`. As **root**:

```sh
tar xzf ssv-prom-exporter-vX.Y.Z-linux-amd64.tar.gz
cd     ssv-prom-exporter-vX.Y.Z-linux-amd64
./install-linux.sh
$EDITOR /etc/ssv-prom-exporter/config.yaml   # set url / user / pass
systemctl enable --now ssv-prom-exporter
journalctl --unit ssv-prom-exporter -f
```

The unit runs as a `DynamicUser` with `ProtectSystem=strict`,
`NoNewPrivileges`, a `SystemCallFilter`, and tight memory limits.

### Windows (native service / MSI)

The exporter is a native Windows service — the same `.exe` runs
interactively or under the SCM. Releases ship a standalone `.exe` and an
MSI. End-to-end from an **elevated** prompt:

```bat
:: 1. Install the MSI (drops exe + LICENSE + config.example.yaml under Program Files).
msiexec /i ssv-prom-exporter-X.Y.Z-x64.msi /qn

:: 2. Place the YAML config in ProgramData and tighten ACLs.
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

The MSI does **not** register the service (so config paths and
credentials never leak into MSI properties); `-install` does, baking
only the explicitly-set flags into the SCM `ImagePath`. With the
config-file workflow above that's just `-config <path>`, keeping `-pass`
out of `sc qc`. Service-mode logs go to **Windows Logs → Application**
under the service's Event Log source.

Uninstall (service first, it's independent of the MSI):

```bat
sc stop ssv-prom-exporter
"C:\Program Files\ssv-prom-exporter\ssv-prom-exporter.exe" -uninstall
msiexec /x ssv-prom-exporter-X.Y.Z-x64.msi /qn
```

## High availability / failover

The exporter falls over to a backup management server when the primary
is unreachable:

- After each successful inventory scrape, every IP from
  `/servers[].IpAddresses` is added to the backup list (filtered by
  `-backup-cidrs` / `SSV_BACKUP_CIDRS`, default = the primary's `/24`).
- On a transient failure (network error, timeout, HTTP 5xx) the next
  endpoint is tried. HTTP 4xx is **not** a failover trigger — it's a
  config bug.
- The last-known-good endpoint is sticky for 5 min, so during an outage
  only the first call pays the dial-timeout cost; after 5 min the next
  call retries the primary, detecting recovery.
- The `ServerHost` header is rewritten per endpoint (each backup uses
  its own IP); SSV rejects hostname-based `ServerHost` values with HTTP 400.
- If every endpoint still fails transiently, the call is retried with
  exponential backoff (`-retries` / `-retry-delay`).

Seed the backup list before the first scrape with
`-bases ip1,ip2,...` (or `SSV_BASES`).

## Multi-group SSV deployments

SSV management servers federate their state: `/serverGroups` returns the
local group plus every linked peer, and `/servers` mixes local nodes
with remote ones, but `/performance/{id}` is local-only. The exporter
therefore scrapes per-server inventory and performance **only for the
local group** (`OurGroup=true`); group-level metrics keep every peer so
you can still alert on a federated group going unreachable.

Practical consequence: **run one exporter per SSV group**, and list them
all in `EXPORTER_TARGETS`:

```env
EXPORTER_TARGETS=HCI104=10.12.104.121:9876,HCI130=10.12.130.121:9876
```

Each becomes a Prometheus target carrying its `group=<name>` label, and
the Grafana dashboards filter on `$group` end-to-end.

## Requirements

- DataCore SANsymphony **PSP 20+** (older may work, untested).
- TCP/443 reachability from the exporter to the SSV management server.
- A Docker host with Compose v2 for the stack; or Windows / Linux /
  any OCI host for a standalone exporter.
- For building from source: Go 1.26+. MSI builds need `wixl`
  (`apt install wixl`); multi-arch images need Docker Buildx.

## Notes / gotchas

- The `ServerHost` HTTP header is mandatory on every REST call; missing
  it returns `HTTP 400` / `ErrorCode 9`. Its value must be the IP being
  hit — hostnames are rejected with HTTP 400 even when they resolve.
- Some SSV IDs contain colons and curly braces (pool IDs of the form
  `<server-uuid>:{<pool-uuid>}`); they're path-escaped before being put
  in URLs.
- `/performance/{id}` returns an array with a single snapshot — unwrap `[0]`.
- SSV's REST cache is 30 s by default (`RequestExpirationTime` in the
  mgmt server's `Web.config`); faster scrapes see stale data.
- SSV `*Time` perf counters are milliseconds; the exporter multiplies by
  `1e-3` so all latency series are in seconds.

## Releases

Releases are produced by GitHub Actions on an annotated `v*` tag. The
release body comes from the matching [`CHANGELOG.md`](CHANGELOG.md)
section. Each release ships the Windows `.exe`, the MSI, a Linux tarball,
`SHA256SUMS`, and multi-arch GHCR images for both
`ssv-prom-exporter` and `prom-clip`.

```sh
$EDITOR CHANGELOG.md                 # add a ## [vX.Y.Z] - YYYY-MM-DD section
git commit -am "CHANGELOG: vX.Y.Z"
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push && git push origin vX.Y.Z
```

The [build/install matrix](deploy/) and the from-source targets:

```sh
make build            # native binary
make build-linux      # static linux/amd64
make build-windows    # cross-compile windows/amd64
make msi              # MSI (needs wixl)
make tarball-linux    # linux tarball (binary + systemd unit + config)
make docker-build     # OCI image
make build-prom-clip  # the companion binary
```

## License

[MIT](LICENSE).
