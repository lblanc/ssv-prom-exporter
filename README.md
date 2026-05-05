# ssv-prom-exporter

Prometheus exporter for [DataCore SANsymphony](https://www.datacore.com/products/sansymphony/)
(SSV), packaged as a native Windows service.

> **Status:** v0. The binary exposes a Prometheus `/metrics` endpoint
> backed by inventory and health collectors (server groups, servers,
> pools, virtual disks, monitors, alerts). The performance collector
> and Windows service mode come next.

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
| `-url`       | `SSV_URL`    | SSV REST base URL, e.g. `https://10.0.0.1`                    |
| `-user`      | `SSV_USER`   | SSV username (typically a local Windows account)              |
| `-pass`      | `SSV_PASS`   | SSV password                                                  |
| `-host`      | `SSV_HOST`   | `ServerHost` header value; defaults to the host of `-url`     |
| `-insecure`  | —            | Skip TLS verification (default `true`; SSV ships self-signed) |
| `-ping`      | —            | Probe `/serverGroups`, print the response, exit               |
| `-listen`    | —            | Listen address for the Prometheus HTTP exporter, e.g. `:9876` |
| `-version`   | —            | Print version and exit                                        |

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

State integers are exposed as-is — the SSV vendor enum mapping is not
documented in the REST help.

For a quick interactive probe (raw JSON of `/serverGroups`):

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -ping
```

## Roadmap

- [x] Typed REST client (`internal/ssv`) with Basic auth, mandatory
      `ServerHost` header, and `.NET /Date(...)/` parsing.
- [x] Inventory collector + HTTP server (`/metrics` via `promhttp`).
- [x] Health collector (`ssv_monitor_state`, `ssv_alerts_total`).
- [ ] Performance collector — parallel `/performance/{id}` calls behind
      a bounded worker pool, exposing `*_bytes_total` /
      `*_operations_total` counters.
- [ ] Windows service mode (`install` / `uninstall` / run-as-service via
      `golang.org/x/sys/windows/svc`, EventLog wiring).
- [ ] Retry/backoff on transient SSV failures.
- [ ] YAML config replacing env vars when more knobs are needed.

## Notes / gotchas

- The `ServerHost` HTTP header is mandatory on every REST call; missing
  it returns `HTTP 400` with `ErrorCode 9`.
- Some SSV IDs contain colons and curly braces (notably pool IDs of the
  form `<server-uuid>:{<pool-uuid>}`); they must be path-escaped before
  being interpolated into URLs.
- `/performance/{id}` returns an array containing a single snapshot —
  unwrap `[0]`.
- SSV's REST cache is 30s by default (`RequestExpirationTime` in
  `Web.config` on the mgmt server). Faster scrapes will see stale data.

## License

TBD.
