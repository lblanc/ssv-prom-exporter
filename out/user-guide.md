---
title: "ssv-prom-exporter — User guide"
subtitle: "Deploy and operate the DataCore SANsymphony Prometheus exporter with Docker Compose"
author: "Luc Blanc"
date: "2026-06-04"
---

# 1. What you are looking at

`ssv-prom-exporter` is a single-binary Prometheus exporter for
DataCore SANsymphony (SSV). It scrapes the SSV REST API and exposes
inventory, health and performance signals on `/metrics` in the
standard Prometheus exposition format.

This guide is **Docker-Compose-first**: the fastest path to a working
SANsymphony monitoring stack — Prometheus, Grafana, five ready-made
dashboards, and optionally the exporter itself and the `prom-clip`
companion — is the bundled `deploy/` compose project. Native
Windows-service and Linux-systemd installs are covered in section 9 for
production nodes that don't run a container runtime.

The exporter does **not** need to run on the SSV host. Any host on the
network with TCP/443 reachability to a SANsymphony management server is
enough.

> All screenshots in this guide were captured live against a DataCore
> PSP 20 lab through the compose stack described here. The same content
> is available as an HTML online help in `out/web-help/index.html`.

# 2. The compose stack at a glance

Everything lives under `deploy/`. The stack is built around **three
optional services selected by compose profiles**, so you only run what
you need:

| Service       | Profile     | Host port            | Role |
|---------------|-------------|----------------------|------|
| `prometheus`  | *always*    | `9090`               | Scrapes the exporter(s); 15 d retention. |
| `grafana`     | *always*    | `3000`               | Five SSV dashboards, anonymous Viewer on. |
| `exporter`    | `full`      | *internal*           | Runs `ssv-prom-exporter` itself against one SSV group. |
| `prom-clip`   | `clip`      | `127.0.0.1:8088`     | Clip a Prometheus time window and replay it elsewhere. |

Profiles compose freely — `--profile full --profile clip` brings up all
four. With no profile flag you get just Prometheus + Grafana, which is
Scenario A (section 4).

```
                     deploy/docker-compose.yml
 +--------------------------------------------------------------+
 |  prometheus:9090 ---------> grafana:3000  (5 dashboards)      |
 |      ^ scrape                                                 |
 |      |                                                        |
 |  +---+-------------+       +------------------+               |
 |  | exporter:9876   |       | prom-clip:8088   |               |
 |  | (--profile full)|       | (--profile clip) |               |
 |  +---+-------------+       +------------------+               |
 +------+--------------------------------------------------------+
        | REST / 443
        v
  SANsymphony management server(s)
```

# 3. Requirements

- A Docker host with **Compose v2** (`docker compose`, not the legacy
  `docker-compose`).
- DataCore SANsymphony **PSP 20+** (older may work, untested).
- TCP/443 reachability from wherever the exporter runs to the SSV
  management server.
- Outbound network from the Docker host to your exporter targets
  (Scenario A) or to the SSV REST API (Scenario B).

# 4. Scenario A — monitor exporters that already run elsewhere

**Default profile.** The exporter runs on each SAN host (as a Windows
service / systemd unit / standalone container); the compose stack only
adds Prometheus + Grafana and scrapes those exporters over the network.
This is the normal production topology.

## 4.1 Configure `deploy/.env`

```env
# One entry per SANsymphony group: "groupname=host:port,...".
# groupname becomes the Prometheus `group` label the dashboards filter on.
EXPORTER_TARGETS=LAB-PVE=10.12.110.11:9876,HCI104=10.12.104.121:9876

# Grafana admin password (anonymous Viewer is always on; this gates editing).
GF_ADMIN_PASSWORD=admin
```

A single target is just as valid:
`EXPORTER_TARGETS=lab=10.12.110.11:9876`.

## 4.2 Bring it up

```sh
cd deploy
docker compose up -d
docker compose ps
```

## 4.3 Verify every target is scraped

```sh
curl -s http://localhost:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.group)\t\(.scrapeUrl)\t\(.health)"'
```

Or open Prometheus → **Status → Target health**. Each exporter target
carries its `group` label and should read **UP**:

![Prometheus Target health — the ssv-prom-exporter job shows 2/2 up, one
target per SANsymphony group.](web-help/stack-01-prom-targets.png)

## 4.4 Open Grafana

Browse to `http://localhost:3000` → **Dashboards → SSV**. Five
dashboards are pre-provisioned; pick a group from the **Group** dropdown
on any of them.

![The provisioned SSV folder: Overview, Servers, Storage, Hosts and
Ports.](web-help/stack-02-grafana-list.png)

# 5. Scenario B — run everything in the stack (`--profile full`)

For demos, PoCs, or sites that prefer to run everything containerized:
the `full` profile adds the `exporter` service to the stack, so you
install nothing on the SAN host. Prometheus auto-discovers it on the
compose network (`exporter:9876`).

## 5.1 Configure `deploy/.env`

```env
# --- exporter against one SSV group ---
SSV_URL=https://10.12.110.11        # SSV REST base URL (the mgmt server)
SSV_USER=administrator
SSV_PASS=ChangeMe!
SSV_GROUP=lab                       # becomes the Prometheus `group` label

# Optional failover knobs (see section 10)
# SSV_BASES=10.12.110.12,10.12.110.13
# SSV_BACKUP_CIDRS=10.12.110.0/24

# Leave EXPORTER_TARGETS unset — Prometheus auto-targets exporter:9876.
GF_ADMIN_PASSWORD=admin
```

## 5.2 Bring it up

`--build` builds the exporter image locally from the repo. Drop it to
pull `ghcr.io/lblanc/ssv-prom-exporter:latest` instead.

```sh
cd deploy
docker compose --profile full up -d --build
```

## 5.3 Watch it come alive

```sh
docker compose logs -f exporter
docker compose exec exporter wget -qO- http://127.0.0.1:9876/metrics | grep '^ssv_up'
# ssv_up{collector="health"} 1
# ssv_up{collector="inventory"} 1
# ssv_up{collector="performance"} 1
```

`/metrics` is internal by default. To curl it from the host while in
`--profile full`, uncomment the `ports:` block (`- "9876:9876"`) on the
`exporter` service in `deploy/docker-compose.yml`.

Pin a released image instead of building:

```env
EXPORTER_IMAGE=ghcr.io/lblanc/ssv-prom-exporter:v0.9.0
```

```sh
docker compose --profile full up -d        # no --build → pulls EXPORTER_IMAGE
```

# 6. Scenario C — add the prom-clip companion (`--profile clip`)

`prom-clip` clips a time window out of one Prometheus and replays it
into another (gzipped OpenMetrics out, remote-write in). The `clip`
profile runs its web UI alongside the stack, bound on host loopback
`127.0.0.1:8088` — no Windows Firewall prompt, no external exposure.

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

![prom-clip Connection — a single Prometheus endpoint is the source for
Export and the remote-write target for Import.](web-help/clip-01-connection.png)

To **import** a clip back into the stack's Prometheus, it must accept
remote-write. That is an opt-in, set in `deploy/.env`:

```env
PROM_REMOTE_WRITE=1     # turns on --web.enable-remote-write-receiver
PROM_OOO_WINDOW=7d      # out-of-order window; old samples land instead of being dropped
```

```sh
docker compose up -d    # re-create prometheus with the receiver enabled
```

Without `PROM_OOO_WINDOW`, any sample older than the ~2 h head block is
discarded while the write still returns `200 OK` — the import looks
successful but lands nothing.

# 7. Configuration reference

## 7.1 `deploy/.env`

| Variable            | Profile | Default                | Description |
|---------------------|---------|------------------------|-------------|
| `EXPORTER_TARGETS`  | default | `lab=host.docker.internal:9876` | Comma list `name=host:port`. Each `name` becomes the `group` label. |
| `SSV_URL`           | full    | *required*             | SSV REST base URL, e.g. `https://10.0.0.1`. |
| `SSV_USER`          | full    | *required*             | SSV username. |
| `SSV_PASS`          | full    | *required*             | SSV password. |
| `SSV_GROUP`         | full    | `full`                 | Label applied to the in-stack exporter's metrics. |
| `SSV_BASES`         | full    | *empty*                | Cold-start backup IPs (comma list). |
| `SSV_BACKUP_CIDRS`  | full    | primary's `/24`        | CIDR allowlist for discovered backups. `0.0.0.0/0` disables. |
| `EXPORTER_IMAGE`    | full    | `ghcr.io/lblanc/ssv-prom-exporter:latest` | Image when not using `--build`. |
| `GF_ADMIN_PASSWORD` | always  | `admin`                | Grafana admin password (editing only). |
| `PROM_REMOTE_WRITE` | always  | *off*                  | `1` to accept inbound remote-write. |
| `PROM_OOO_WINDOW`   | always  | `7d`                   | Out-of-order storage window; required for backfill. |
| `PROM_CLIP_IMAGE`   | clip    | `ghcr.io/lblanc/prom-clip:latest` | Image for the prom-clip service. |

## 7.2 Exporter settings (env vars & flags)

Inside the stack (`--profile full`) the exporter is configured through
the `SSV_*` env vars above. Run standalone, the same settings are
available as flags and/or a YAML config.

| Flag               | Env var             | Description |
|--------------------|---------------------|-------------|
| `-config`          | `SSV_CONFIG`        | Path to a YAML config file. |
| `-url`             | `SSV_URL`           | SSV REST base URL. |
| `-user` / `-pass`  | `SSV_USER` / `SSV_PASS` | Credentials. |
| `-host`            | `SSV_HOST`          | `ServerHost` header; defaults to the host of `-url`. |
| `-insecure`        | —                   | Skip TLS verification (default `true`). |
| `-bases`           | `SSV_BASES`         | Backup IPs seeded before the first scrape. |
| `-backup-cidrs`    | `SSV_BACKUP_CIDRS`  | CIDR allowlist for discovered backups. |
| `-retries`         | —                   | Retries after every endpoint failed transiently (default `2`). |
| `-retry-delay`     | —                   | Initial backoff (default `200ms`); doubles, capped 2 s, ±50% jitter. |
| `-perf-workers`    | —                   | Concurrent `/performance/{id}` calls (default `8`). |
| `-listen`          | —                   | Exporter HTTP listen address, e.g. `:9876`. |
| `-ping`            | —                   | Probe `/serverGroups`, print, exit. |
| `-install` / `-uninstall` | —            | Register / remove the Windows service. |

Precedence: `explicit flag > env var (flag default) > YAML > built-in
default`. Unknown YAML keys are rejected at load time.

## 7.3 Compose command cheat-sheet

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

# Reload Prometheus config without a restart
curl -X POST http://localhost:9090/-/reload
```

# 8. The Grafana dashboards

Grafana is provisioned with a datasource and **five dashboards** in the
**SSV** folder, all cross-linked through an "SSV" dropdown that
preserves the time range and selected filters as you move between them.
Every panel is filtered by a **Group** template variable.

## 8.1 Overview

Global health: scrape status, active alerts (level / server / age),
server states, capacity rollups, total IOPS & latency, top-N noisy
vdisks, active monitors.

![SSV — Overview](web-help/01-overview.png)

## 8.2 Servers

Per-server (repeated row): state, cache, IOPS & throughput, and IOPS &
latency broken down by IO pipeline class (front-end target / mirror
target / back-end / pool / target). A "Server versions" table reads
product / OS / build from `ssv_server_info`.

![SSV — Servers](web-help/02-servers.png)

## 8.3 Storage

Per-pool (status, capacity, IOPS, latency) with a collapsible
Physical-disks subsection, and per-vdisk (status, cache hit ratio, IOPS,
throughput, latency).

![SSV — Storage](web-help/03-storage.png)

## 8.4 Hosts

SAN-client inventory + per-host IOPS & bandwidth, peak IO size,
provisioned capacity, plus a Connections subsection for the host's
ports.

![SSV — Hosts](web-help/04-hosts.png)

## 8.5 Ports

Per-port (table + IOPS + bandwidth + target IO latency + pending
commands) with a collapsible Errors row (link-layer counters).

![SSV — Ports](web-help/05-ports.png)

Anonymous access is read-only Viewer. Log in as `admin` with
`GF_ADMIN_PASSWORD` to edit.

## 8.6 Exposed metrics

- **Scrape framing**: `ssv_up{collector=...}`,
  `ssv_scrape_duration_seconds{collector=...}`.
- **Inventory**: `ssv_server_group_*`, `ssv_server_*` (+ `info`),
  `ssv_pool_*`, `ssv_virtual_disk_*`, `ssv_host_*`, `ssv_port_*`,
  `ssv_physical_disk_*` (+ `ssv_physical_disk_pool` relation gauge).
- **Health**: `ssv_monitor_state`, `ssv_alerts_total`,
  `ssv_alert_info`, `ssv_alert_age_seconds`.
- **Performance**: per-object `read/write_bytes_total`,
  `read/write_ops_total` and per-object extras; latency in seconds
  (`*_time_seconds_total` + `*_max_time_seconds`), server tier split by
  IO pipeline class.

```promql
# average IO latency per class
rate(ssv_server_class_io_time_seconds_total[$__rate_interval])
  /
rate(ssv_server_class_io_operations_total[$__rate_interval])
```

# 9. Running the exporter without compose

## 9.1 Plain docker run

```sh
docker run --rm -p 9876:9876 \
    -e SSV_URL=https://10.0.0.1 \
    -e SSV_USER=administrator \
    -e SSV_PASS='ChangeMe!' \
    ghcr.io/lblanc/ssv-prom-exporter:latest
```

Multi-arch images (`linux/amd64` + `linux/arm64`) are published on every
release as `vX.Y.Z`, `X.Y` and `latest`.

## 9.2 Linux (systemd)

```sh
tar xzf ssv-prom-exporter-vX.Y.Z-linux-amd64.tar.gz
cd     ssv-prom-exporter-vX.Y.Z-linux-amd64
./install-linux.sh
$EDITOR /etc/ssv-prom-exporter/config.yaml   # set url / user / pass
systemctl enable --now ssv-prom-exporter
journalctl --unit ssv-prom-exporter -f
```

## 9.3 Windows (native service / MSI)

From an **elevated** prompt:

```bat
:: 1. Install the MSI
msiexec /i ssv-prom-exporter-X.Y.Z-x64.msi /qn

:: 2. Place config.yaml in ProgramData and tighten ACLs
copy "C:\Program Files\ssv-prom-exporter\config.example.yaml" ^
     "C:\ProgramData\ssv-prom-exporter\config.yaml"
icacls "C:\ProgramData\ssv-prom-exporter\config.yaml" /inheritance:r ^
       /grant:r SYSTEM:F Administrators:F

:: 3. Register and start. Only -config lands in the SCM ImagePath.
"C:\Program Files\ssv-prom-exporter\ssv-prom-exporter.exe" ^
  -install -config "C:\ProgramData\ssv-prom-exporter\config.yaml"
sc start ssv-prom-exporter
```

The MSI does **not** register the service; `-install` does, baking only
the explicitly-set flags into the SCM `ImagePath`. Service-mode logs go
to **Windows Logs → Application** under the service's Event Log source.

# 10. High availability & multi-group

## 10.1 Failover

The exporter falls over to a backup management server when the primary
is unreachable. After each successful inventory scrape, every IP from
`/servers[].IpAddresses` is added to the backup list (filtered by
`SSV_BACKUP_CIDRS`, default = the primary's `/24`). On a transient
failure (network error, timeout, HTTP 5xx) the next endpoint is tried;
HTTP 4xx is **not** a failover trigger. The last-known-good endpoint is
sticky for 5 min. The `ServerHost` header is rewritten per endpoint —
SSV rejects hostname-based `ServerHost` values with HTTP 400. Seed the
list before the first scrape with `SSV_BASES=ip1,ip2,...`.

## 10.2 Multi-group

SSV management servers federate their state, but `/performance/{id}` is
local-only. The exporter scrapes per-server inventory and performance
**only for the local group** (`OurGroup=true`). **Run one exporter per
SSV group** and list them all in `EXPORTER_TARGETS`:

```env
EXPORTER_TARGETS=HCI104=10.12.104.121:9876,HCI130=10.12.130.121:9876
```

# 11. prom-clip in depth

`prom-clip` clips a time window from one Prometheus and replays it into
another — to hand a dataset to support, seed a demo, or carry a
before/after comparison between sites that aren't network-connected.
Wire format: gzipped OpenMetrics (`.txt.gz`); replay uses the
remote-write protocol.

![prom-clip Export — reading from the connected Prometheus, with a
`ssv_.*` metric filter and a 30 s step.](web-help/clip-02-export.png)

![prom-clip Import — replays a clip into the target Prometheus over the
remote-write protocol.](web-help/clip-03-import.png)

The same binary runs headless, with no server, port or state directory:

```sh
prom-clip export -src http://prom-source:9090 \
                 -from -1h -to now -step 30s \
                 -metric '^ssv_.*' \
                 -out snapshot.txt.gz
prom-clip import -dst http://prom-target:9090 \
                 -in snapshot.txt.gz
```

`-from`/`-to` accept RFC3339, a Go duration (`-1h`), or `now`. Pass
`-state-dir <path>` to opt into persistent mode. **Import requires** the
target Prometheus to run with `--web.enable-remote-write-receiver` and a
wide-enough `storage.tsdb.out_of_order_time_window`; the bundled stack
exposes both behind `PROM_REMOTE_WRITE` (section 6).

# 12. Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `ssv_up=0` on all collectors | Bad credentials, wrong `ServerHost`, or a stale session token. Check `docker compose logs exporter`. |
| HTTP 400 / `ErrorCode 9` | Missing/hostname `ServerHost` header. It must be the IP being hit. |
| Grafana panels empty for a group | The Group dropdown doesn't match a scraped target, or that exporter's performance tier is down. |
| prom-clip import "succeeds" but nothing lands | Target Prometheus has no out-of-order window — set `PROM_REMOTE_WRITE=1` + `PROM_OOO_WINDOW`. |
| Data looks stale | SSV's REST cache is 30 s by default (`RequestExpirationTime`). |

# 13. Releases

Releases are produced by GitHub Actions on an annotated `v*` tag. Each
release ships the Windows `.exe`, the MSI, a Linux tarball,
`SHA256SUMS`, and multi-arch GHCR images for both `ssv-prom-exporter`
and `prom-clip`. The latest release is **v0.9.0** (2026-06-04). See the
project's GitHub Releases for the full list and `CHANGELOG.md` for
per-version notes.
