# Architectural decisions for ssv-prom-exporter

This file records the WHY behind non-obvious choices in this project.
Read before suggesting changes — most odd-looking decisions are intentional.

## Format
Each decision: 3-6 lines.
- Decision: what was chosen.
- Rationale: why.
- Trade-off: what we accept.

## Decisions

### REST API as the single data source (no PowerShell, no Windows perflib)
Decision: collect all signals via SSV's REST API at
`/RestService/rest.svc/1.0/`.
Rationale: REST exposes topology, state, health AND performance counters
(`/performance/{instanceId}`). Going through PowerShell cmdlets or Windows
performance counters would duplicate data already exposed via REST and tie
the exporter to running on the SSV server. REST lets it run anywhere on
the network reachable to the mgmt server.
Trade-off: extra HTTP round-trips vs. a local perflib read; cushioned by
SSV's 30s server-side cache and the exporter's own cache.
Note: an initial conclusion that perf was unavailable via REST was wrong —
it came from probing alone. The official doc surfaces
`/performance/{instanceId}` as the right pattern.

### Windows service packaging via `golang.org/x/sys/windows/svc`, not NSSM
Decision: single binary that detects whether it runs as a service and
self-installs via `/mgr`; service-mode logs go to EventLog, console-mode
logs to stderr.
Rationale: avoids shipping a wrapper process; install/uninstall via the
exporter binary itself; idiomatic Go on Windows.
Trade-off: more Windows-specific code paths to maintain than a "linux-only
binary + NSSM wrapper".

### Three collector tiers with independent refresh rates
Decision: inventory (slow, ~60s), health (~30s), performance (~15s).
Rationale: topology rarely changes; perf is the time-series of interest.
Splitting refresh rates avoids hammering SSV when only perf changes between
scrapes.
Trade-off: more state inside the exporter (per-tier caches), and cross-tier
joins (e.g. labelling a perf metric with a vdisk's caption from the
inventory cache) need a small lookup layer.

### Per-object `/performance` call, parallelised with a worker pool
Decision: one `GET /performance/{id}` per object known from inventory,
fanned out via a bounded worker pool (default 8 concurrent).
Rationale: the SSV perf endpoint takes a single instance ID; there is no
batch form. Parallel calls keep total scrape time bounded as cluster size
grows.
Trade-off: more concurrent connections to the mgmt server; mitigated by
the bounded pool and SSV's server-side perf cache.

### Secrets stay strictly outside the repo
Decision: credentials passed via env vars (`SSV_URL`/`SSV_USER`/`SSV_PASS`)
for v0; later via a YAML config that is gitignored. Only an example file
(`config.example.yaml`) with placeholder values is committed.
Rationale: lab credentials must never enter the repository, even
accidentally; `.gitignore` covers the local config files explicitly.
Trade-off: one more piece of operational paperwork (deploying the config
file or env to the target host).

### .NET-style date parsing in the REST client
Decision: custom `UnmarshalJSON` for SSV's `/Date(epoch_ms+tz)/` format,
applied to a typed `Time` wrapper.
Rationale: SSV's API uses .NET's WCF date format. Off-the-shelf JSON
parsing produces a string we'd parse ad hoc everywhere. Centralising it
once keeps the rest of the code idiomatic.
Trade-off: one more type alias to keep in mind when writing collectors.

### TLS verification disabled by default
Decision: `-insecure` defaults to `true`; the exporter does not verify the
SSV TLS certificate.
Rationale: SSV management servers typically ship with a self-signed cert.
Operationally, requiring a valid CA-signed cert is unrealistic for most
deployments.
Trade-off: a MITM on the mgmt-server-to-exporter path could read
credentials. Deployments that care can supply a CA via a future flag and
flip `-insecure` off.

### REST endpoint failover via discovered server IPs
Decision: the client carries an ordered list of `(baseURL, ServerHost)`
pairs — primary first, backups appended after each successful `/servers`
fetch. `Get` loops on transient errors (net errors, HTTP 5xx); 4xx is
non-transient and short-circuits.
Rationale: SSV groups have multiple management nodes. If one is down,
the exporter should keep scraping via another. Discovering IPs from the
API itself avoids static config drift.
Trade-off: bootstrap requires the primary on first scrape; the `-bases`
flag pre-seeds the list to cover that. `ServerHost` must match the IP we
hit (verified empirically — hostnames return HTTP 400), so each entry is
fully self-contained.

### Sticky preferred endpoint with 5-minute TTL
Decision: after a successful call, the working endpoint index is sticky;
subsequent calls start from it. The TTL (`preferredTTL = 5 min`) makes
the next call retry the primary first, so recovery is detected.
Rationale: without stickiness every call retries the primary, wasting
`N_dead × dialTimeout` during an outage. Sticky means only the first
call after the outage pays that cost.
Trade-off: up to 5 minutes of lag before the exporter notices the
primary recovered. Acceptable for a Prometheus exporter scraping every
15-30 s.

### Backup CIDR filter, default = primary's /24
Decision: discovered IPs are filtered through an allowlist of CIDRs; the
default, when the primary URL is an IPv4, is that IP's `/24`.
Rationale: SSV's `/servers` returns every IP bound on each node (mgmt,
iSCSI, mirror, IPv6 link-local). Most aren't valid REST targets;
attempting them on failover blows the scrape budget (3 s dial timeout
× N dead backups). The `/24` default matches the typical "all mgmt IPs
on one VLAN" deployment.
Trade-off: multi-subnet management deployments must override via
`-backup-cidrs`. To disable filtering, pass `0.0.0.0/0`.

### IPv4-only failover
Decision: discovered IPv6 addresses (link-local and public) are skipped.
Rationale: SSV's REST service often binds IPv4 only in IIS; link-local
IPv6 (`fe80::/10`) is never useful as a backup target. Keeping the
failover list IPv4-only avoids dial timeouts on never-going-to-work
backups.
Trade-off: IPv6-only deployments would need code changes. None observed
on SSV.

### Single binary for console + Windows service mode
Decision: the same exe runs interactively (current console flow) or
under the SCM, picked at startup via `svc.IsWindowsService()`. Service
mode is implemented in `internal/svc` behind build tags
(`svc_windows.go` for the real impl, `svc_other.go` for stubs) so the
project still builds and tests on Linux.
Rationale: avoids shipping NSSM or a wrapper `.bat`, and keeps `-ping`
useful from a Windows console for diagnostics. Build-tagged stubs let
us cross-compile from Linux without breaking either platform.
Trade-off: the Windows service code path is exercised only on the
target OS — Linux CI catches package-level mistakes (vet, build) but
not runtime service semantics.

### EventLog as the slog destination in service mode
Decision: when launched by the SCM, the slog handler is replaced with
one that writes to the service's Event Log source (registered by
`-install`). Levels map to the three EventLog severities
(Error / Warning / Information).
Rationale: services have no console; stderr goes to nowhere useful.
Event Viewer is what Windows ops teams already watch.
Trade-off: no structured logs in service mode (Event Log entries are
flattened to a single string). Acceptable for an exporter — Prometheus
itself is the structured-data sink.

### Service args baked into the SCM ImagePath
Decision: `-install` copies the explicitly-set runtime flags (other
than `-install` / `-uninstall` / `-ping` / `-version`) into the
service's command line via `mgr.CreateService(..., args...)`.
Rationale: simplest install workflow — one command, no second config
file to deploy.
Trade-off: ImagePath is readable by any user with
`SeQueryServiceConfigPrivilege` (and shown by `sc qc`). Anything
sensitive (notably `-pass`) is therefore exposed to local admins. The
install command prints a warning, and a future YAML-config option will
let operators move credentials to a file with restricted ACLs.

## What to watch out for
- The `ServerHost` HTTP header is mandatory on every REST call. Without
  it, the API returns 400 with `ErrorCode 9` (`"No ServerHost header was
  passed."`).
- Some SSV IDs contain colons and curly braces (notably pool IDs of the
  form `<server-uuid>:{<pool-uuid>}`). They must be path-escaped before
  being interpolated into URLs.
- `/performance/{id}` returns an array containing one snapshot, never a
  bare object — code must unwrap `[0]`.
- Perf responses include `NullCounterMap`; counters mapped as null in
  that bitmap should be skipped, not reported as zero.
- `/alerts` and `/tasks` return `[]` on a healthy idle system — don't
  infer "endpoint broken".
- `/performanceCounters` (plural) does NOT exist. The right path is
  `/performance/{instanceId}`, singular. `/performanceByType/{type}`
  exists but consistently returns `[]` and is not used.
- SSV's REST cache is 30s by default (`RequestExpirationTime` in
  `Web.config`). Faster scrapes than that won't see new data.
- Self-signed TLS on the mgmt server requires `InsecureSkipVerify` in the
  Go HTTP client (or a configured custom CA pool, planned later).
- The `ServerHost` header value must match the IP we are hitting. The
  hostnames published in `/servers[].HostName` (e.g. `SDS1-LAB-PVE`) are
  rejected with HTTP 400, even when reaching the right physical host.
  This is why each failover endpoint stores its own `ServerHost = IP`.
