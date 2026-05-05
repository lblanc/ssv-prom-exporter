# ssv-prom-exporter

Prometheus exporter for [DataCore SANsymphony](https://www.datacore.com/products/sansymphony/)
(SSV), packaged as a native Windows service.

> **Status:** v0. The binary exposes a Prometheus `/metrics` endpoint
> backed by inventory, health and performance collectors, and ships
> with native Windows service install / uninstall / run-as-service
> support.

## What it will expose

Three signal tiers, all sourced from SSV's REST API:

- **Inventory / state** — servers, pools, virtual disks, hosts, ports,
  capacity, license expiry.
- **Health** — per-resource monitor states, active alerts.
- **Performance** — cumulative byte and operation counters per object,
  via `GET /performance/{instanceId}`.

## Requirements

- DataCore SANsymphony **PSP 20+** (older versions may work, untested).
- Network reachability from the exporter host to the SSV management
  server on TCP/443. The exporter does **not** need to run on the SSV
  server itself.
- A Windows or Linux build host (Go 1.26+).

## Build

```sh
make build           # native (the host's GOOS/GOARCH)
make build-windows   # cross-compile to windows/amd64 (CGO_ENABLED=0)
```

## Usage

The binary reads its connection settings from flags or env vars:

| Flag         | Env var      | Description                                                   |
|--------------|--------------|---------------------------------------------------------------|
| `-url`            | `SSV_URL`           | SSV REST base URL, e.g. `https://10.0.0.1`                                                                          |
| `-user`           | `SSV_USER`          | SSV username (typically a local Windows account)                                                                    |
| `-pass`           | `SSV_PASS`          | SSV password                                                                                                        |
| `-host`           | `SSV_HOST`          | `ServerHost` header value; defaults to the host of `-url`                                                           |
| `-insecure`       | —                   | Skip TLS verification (default `true`; SSV ships self-signed)                                                       |
| `-bases`          | `SSV_BASES`         | Comma-separated list of backup IPs to fall through to before the first scrape (overwritten by discovered IPs)       |
| `-backup-cidrs`   | `SSV_BACKUP_CIDRS`  | CIDRs that filter discovered backup IPs. Default: primary's `/24` if `-url` is an IPv4. Pass `0.0.0.0/0` to disable. |
| `-ping`           | —                   | Probe `/serverGroups`, print the response, exit                                                                     |
| `-listen`         | —                   | Listen address for the Prometheus HTTP exporter, e.g. `:9876`                                                       |
| `-perf-workers`   | —                   | Concurrent `/performance/{id}` calls per scrape (default `8`)                                                       |
| `-install`        | —                   | Register the binary as a Windows service and exit (Windows only)                                                    |
| `-uninstall`      | —                   | Remove the Windows service and exit (Windows only)                                                                  |
| `-svc-name`       | —                   | Service name (default `ssv-prom-exporter`)                                                                          |
| `-svc-display`    | —                   | Service display name shown in `services.msc`                                                                        |
| `-svc-description`| —                   | Service description text                                                                                            |
| `-version`        | —                   | Print version and exit                                                                                              |

Run as exporter:

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -listen :9876
```

Then `curl http://127.0.0.1:9876/metrics`. The current series include
(non-exhaustive):

- `ssv_up{collector="inventory"|"health"}`,
  `ssv_scrape_duration_seconds{collector="..."}`
- `ssv_server_group_{state,storage_used_bytes,storage_max_bytes,
  out_of_compliance,license_expires_seconds}`
- `ssv_server_{state,support_state,power_state,cache_state,
  diagnostic_mode,maintenance_mode,storage_used_bytes,
  memory_total_bytes,memory_available_bytes,info}`
- `ssv_pool_{status,presence_status,type,chunk_size_bytes}`
- `ssv_virtual_disk_{status,size_bytes,type,offline}`
- `ssv_monitor_state{monitor_id,template,target_id,caption}`
- `ssv_alerts_total`
- `ssv_server_{read_bytes_total,write_bytes_total,read_ops_total,
  write_ops_total,cache_read_hits_total,cache_read_misses_total,
  cache_write_hits_total,cache_write_misses_total,cache_size_bytes,
  cache_free_bytes}`
- `ssv_pool_{read_bytes_total,write_bytes_total,read_ops_total,
  write_ops_total,capacity_bytes,used_bytes,available_bytes,
  reserved_bytes,reclamation_bytes,oversubscribed_bytes}`
- `ssv_virtual_disk_{read_bytes_total,write_bytes_total,
  read_ops_total,write_ops_total,cache_read_hits_total,
  cache_read_misses_total,cache_write_hits_total,
  cache_write_misses_total}`

State integers are exposed as-is — the SSV vendor enum mapping is not
documented in the REST help.

For a quick interactive probe (raw JSON of `/serverGroups`):

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -ping
```

## High availability / failover

The exporter falls over to a backup management server when the primary is
unreachable. Mechanics:

- After each successful inventory scrape, every IP from
  `/servers[].IpAddresses` is added to the backup list (filtered through
  `-backup-cidrs`, default = the primary's `/24`).
- On a transient failure (network error, timeout, HTTP 5xx), the next
  endpoint is tried. HTTP 4xx (auth, missing header) is **not** a
  failover trigger — those are configuration bugs.
- The last-known-good endpoint is "sticky" for 5 minutes, so during an
  outage only the first call pays the dial-timeout cost. After 5 min
  the next call retries the primary, detecting recovery.
- The `ServerHost` header is rewritten per endpoint (each backup uses
  its own IP); SSV rejects hostname-based `ServerHost` values with
  HTTP 400.

Pass `-bases ip1,ip2,...` to seed the backup list before the first
scrape (useful so the exporter is HA-resilient even on cold start).

## Windows service

Cross-compile from Linux:

```sh
make build-windows   # produces bin/ssv-prom-exporter.exe
```

Copy the `.exe` to the Windows target, then from an **elevated** prompt:

```bat
ssv-prom-exporter.exe ^
  -install ^
  -url https://10.0.0.1 ^
  -user administrator ^
  -pass S3cret! ^
  -listen :9876
```

This:

- Registers a service named `ssv-prom-exporter` (configurable via
  `-svc-name`), starting automatically as `LocalSystem`.
- Bakes the runtime flags above into the SCM ImagePath, so the service
  comes back up with the same arguments after every reboot.
- Registers an Event Log source under the service name; service-mode
  logs land in **Windows Logs → Application** filtered on that source.

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

> **Security caveat.** Command-line arguments end up in the SCM
> `ImagePath`, which is readable by any local admin (and dumped by
> `sc qc <name>`). Until the YAML config lands, treat the service host
> as a place where the SSV credentials are visible to the local
> Administrators group. Either accept that, or use a low-privilege SSV
> account just for the exporter.

## Roadmap

- [x] Typed REST client (`internal/ssv`) with Basic auth, mandatory
      `ServerHost` header, and `.NET /Date(...)/` parsing.
- [x] Inventory collector + HTTP server (`/metrics` via `promhttp`).
- [x] Health collector (`ssv_monitor_state`, `ssv_alerts_total`).
- [x] REST endpoint failover (auto-discovery from `/servers`, sticky
      preferred endpoint, CIDR-filtered backup list).
- [x] Performance collector — parallel `/performance/{id}` calls behind
      a bounded worker pool, emitting per-server / per-pool /
      per-virtual-disk IO counters and capacity gauges.
- [x] Windows service mode (`install` / `uninstall` / run-as-service via
      `golang.org/x/sys/windows/svc`, EventLog wiring).
- [ ] Retry/backoff on transient SSV failures.
- [ ] YAML config replacing env vars when more knobs are needed.

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
- SSV's REST cache is 30s by default (`RequestExpirationTime` in
  `Web.config` on the mgmt server). Faster scrapes will see stale data.

## License

TBD.
