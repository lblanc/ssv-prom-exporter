# ssv-prom-exporter

Prometheus exporter for [DataCore SANsymphony](https://www.datacore.com/products/sansymphony/)
(SSV), packaged as a native Windows service.

> **Status:** early scaffold. The current binary only supports a `-ping`
> probe of the SSV REST API. The Prometheus collector surface (the
> actual `/metrics` endpoint) is not implemented yet.

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

## Usage (current `-ping` mode)

The binary reads its connection settings from flags or env vars:

| Flag         | Env var      | Description                                                  |
|--------------|--------------|--------------------------------------------------------------|
| `-url`       | `SSV_URL`    | SSV REST base URL, e.g. `https://10.0.0.1`                   |
| `-user`      | `SSV_USER`   | SSV username (typically a local Windows account)             |
| `-pass`      | `SSV_PASS`   | SSV password                                                 |
| `-host`      | `SSV_HOST`   | `ServerHost` header value; defaults to the host of `-url`    |
| `-insecure`  | —            | Skip TLS verification (default `true`; SSV ships self-signed)|
| `-ping`      | —            | Probe `/serverGroups` and print the response                 |
| `-version`   | —            | Print version and exit                                       |

Smoke test against a lab:

```sh
SSV_URL=https://10.0.0.1 SSV_USER=administrator SSV_PASS='***' \
  ./bin/ssv-prom-exporter -ping
```

A successful run prints the JSON returned by
`/RestService/rest.svc/1.0/serverGroups`.

## Roadmap

- Typed REST client (`internal/ssv`) with Basic auth, mandatory
  `ServerHost` header, retry, and `.NET /Date(...)/` parsing.
- Inventory collector (`ssv_up`, `ssv_servers_total`, `ssv_server_state`,
  `ssv_pool_capacity_bytes`, `ssv_virtual_disk_state`).
- Health collector (`ssv_monitor_state`, `ssv_alert_active`).
- Performance collector — parallel `/performance/{id}` calls behind a
  bounded worker pool, exposing `*_bytes_total` / `*_operations_total`.
- HTTP server (`/metrics` via `promhttp`).
- Windows service mode (`install` / `uninstall` / run-as-service via
  `golang.org/x/sys/windows/svc`, EventLog wiring).
- YAML config replacing env vars when more knobs are needed.

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
