# Handoff — ssv-prom-exporter

Snapshot for whoever picks this up next. Pairs with `README.md` (user
view), `PROJECT_CONTEXT.md` (current state), and `DECISIONS.md` (the
"why" behind non-obvious choices). Read those three first if you've
never touched this codebase before — this file is the executive
summary plus what's still open.

## What this is

Prometheus exporter for DataCore SANsymphony, packaged as a Windows
service. Talks to SSV's REST API at `/RestService/rest.svc/1.0/`,
exposes three signal tiers — inventory, health, performance — under
the namespace `ssv_*`. Three Grafana dashboards (Overview / Storage /
Hosts / Servers / Ports) ship in `deploy/grafana/` along with a
docker-compose stack for local validation.

Single Go binary, cross-compiled from Linux. Same exe runs as a
Windows service (auto-detected via `svc.IsWindowsService()`) or
interactively with `-listen :9876`.

## Current status — v0.7.0 (2026-05-07)

- Tag `v0.7.0` pushed; GitHub Actions release workflow built the
  Windows binary + MSI, attached them with `SHA256SUMS` to the
  GitHub Release. CI green.
- Validated against the lab at `https://10.12.110.11` (SSV PSP 20).
  All three collectors report `ssv_up=1`; full scrape ~700 ms with 8
  perf workers.
- Last functional change is internal: REST client switched from
  per-call HTTP Basic to session-based auth (matching the official
  Self-Service Portal contract). No metrics changed; behaviour is
  the same from Prometheus's side, just one OpenSession per process
  instead of one auth roundtrip per request. See
  `DECISIONS.md → "Session-based auth, with reauth on 401 and
  per-endpoint tokens"`.

## Install on a Windows host

The MSI is attached to the v0.7.0 GitHub Release. For convenience a
short-lived presigned mirror is also on S3:

- Direct: `s3://devbox/ssv-prom-exporter/ssv-prom-exporter-0.7.0-x64.msi`
- Presigned URL (expires; regenerate with `proj share
  ssv-prom-exporter-0.7.0-x64.msi --ttl 24h` from this project dir).

Steps:

1. Run the MSI. It drops `ssv-prom-exporter.exe`,
   `config.example.yaml`, and `LICENSE` under
   `C:\Program Files\ssv-prom-exporter\`.
2. Copy `config.example.yaml` to a writable location (e.g.
   `C:\ProgramData\ssv-prom-exporter\config.yaml`) and fill in
   `url`, `user`, `pass`, `listen`. ACL the file so only the service
   account can read it — credentials live in there.
3. Install as a service from an elevated prompt:
   ```
   cd "C:\Program Files\ssv-prom-exporter"
   .\ssv-prom-exporter.exe -install -config "C:\ProgramData\ssv-prom-exporter\config.yaml"
   sc start ssv-prom-exporter
   ```
4. Scrape from Prometheus at `http://<host>:9876/metrics`.

The MSI does **not** auto-register the service (so reinstalls don't
clobber a customised config path). The `-install` step is explicit.

## Operation

- Logs in service mode go to the Windows Event Log under source
  `ssv-prom-exporter` (Application channel). Console mode logs to
  stderr.
- Service mgmt: `sc start|stop|query ssv-prom-exporter`. Uninstall
  with `ssv-prom-exporter.exe -uninstall`.
- Smoke test (any host with network reach to the SSV mgmt server):
  ```
  ssv-prom-exporter -url https://<ssv> -user <u> -pass <p> -ping
  ```
- Endpoint failover: backups are auto-discovered from `/servers` after
  the first successful scrape and filtered by `-backup-cidrs`
  (defaults to the primary's `/24`). Override with `0.0.0.0/0` if
  multi-subnet.

## Build / release

- `make build-windows` — cross-compile to `bin/ssv-prom-exporter.exe`.
- `make msi VERSION=v0.7.0` — needs `wixl` (Debian package). Produces
  `bin/ssv-prom-exporter-0.7.0-x64.msi`.
- Tag and push:
  ```
  git tag -a vX.Y.Z -m "..."
  git push origin vX.Y.Z
  ```
  The `release.yml` workflow builds + publishes the GitHub Release
  automatically. CI on every push runs `go vet`, `go build`, `go
  test`, plus the windows cross-compile (see `.github/workflows/`).

## Open items

Coverage gaps surfaced by the inspiration Grafana boards (panels we
couldn't fill from current metrics):

- **Pool extras**: `EstimatedDepletionTime` (gauge, seconds),
  `MaxTierNumber`, `TierReservedPct`, `InSharedMode` (info labels on
  a future `ssv_pool_info` series).
- **VDisk allocation %**: emit `PercentAllocated` if the field exists
  on the REST shape (the InfluxDB inspiration uses it).

These are listed in `PROJECT_CONTEXT.md → "Remaining tasks"`. None
are blockers; pick them up if a dashboard requirement shows up.

## Things to be careful about

The full list lives in `DECISIONS.md → "What to watch out for"`.
Highlights:

- The `ServerHost` HTTP header is **mandatory** on every call; if
  you see HTTP 400 with `ErrorCode 9`, that's why.
- Pool IDs contain `:` and `{...}` — they MUST be path-escaped
  before going into a URL. The client does this; if you add a new
  resource type, mirror the pattern.
- `/performance/{id}` returns an array of length one — always unwrap
  `[0]`. There is no `/performanceCounters` (plural).
- TLS verification is **disabled by default** because SSV ships
  self-signed certs. The `-insecure=false` path expects a custom CA
  pool that is NOT yet wired in (TODO if a customer requires strict
  TLS — see the `crypto/tls.Config` in `client.go`).
- Authentication uses a non-standard literal "Basic <user> <pass>"
  (no base64) on `/sessions`, then "Token <token>" (not "Bearer")
  everywhere else. RFC 7617 / OAuth 2 clients **will be rejected on
  the session call**. Don't be tempted to "clean this up".

## Contacts

- Primary author: lblanc (GitHub `lblanc`, repo
  `lblanc/ssv-prom-exporter`, private).
- Lab: `10.12.110.11` (PSP 20). For PSP 21 sanity-check the URI
  templates against the local `DataCore.RestService.dll` — the 1.0
  contract has been stable historically but minor additions show up
  release-to-release.

## When you take over

1. Skim `README.md` for the user-facing view.
2. Skim `PROJECT_CONTEXT.md` for current state and the "Completed"
   ledger (entries are dated, latest at the bottom).
3. Skim `DECISIONS.md` before changing architecture; most odd-looking
   choices are intentional and have a "Trade-off:" line spelling out
   what we accept.
4. Update this file when the open items above shrink or grow, and
   bump the "Current status" section on every release.
