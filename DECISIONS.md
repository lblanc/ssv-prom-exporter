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
install command prints a warning when `-pass` was used without
`-config`, and the YAML config (below) is the recommended way to keep
credentials out of `sc qc`.

### SSV perf timers are milliseconds; exported as Prometheus seconds
Decision: every `*Time` counter pulled from `/performance/{id}`
(`Total*Time`, `Max*Time`, `*MaxIOTime`) is treated as milliseconds and
multiplied by `timeScale = 1e-3` before emission, so all latency
metrics expose seconds — Prometheus convention.
Rationale: verified empirically against PSP 20 by sampling
`/performance/{server-id}` twice ~6 s apart and computing
`Δ TotalOperationsTime / Δ Operations` for several pipeline classes.
Results landed in the 0.6–2.8 range, matching SSD/cache latencies in
ms; the same numbers in μs (ns × 100) would imply RAM-speed IO,
which is impossible for a network-fronted SDS. `MaxIOTime` peaks at
15 in the lab also fit ms (15 ms peak = healthy).
Trade-off: the unit isn't stamped in the API response, so a future
SSV release that switched timers to μs or 100ns ticks would silently
break the conversion. Mitigation: the conversion lives in one
constant (`internal/collectors/performance.go::timeScale`); a sanity
test against a known-active lab catches it.

### Retry/backoff layered on top of failover, not per-endpoint
Decision: `GetRaw` retries up to `Retries` additional times (default 2)
when *every* configured endpoint has failed transiently in one pass.
Backoff is exponential (`RetryBaseDelay`, default 200ms) doubled each
attempt, capped at `RetryMaxDelay` (2s), with up to 50% jitter. Ctx
cancellation aborts the sleep immediately. Non-transient errors (4xx,
decode) short-circuit the loop.
Rationale: per-endpoint retry would defeat the failover advantage —
the existing loop already tries all endpoints once, so retry should
only kick in when *all* endpoints failed transiently (typical case:
a brief network blip or a global mgmt-server hiccup). Bounded backoff
keeps a scrape inside Prometheus's 30s collector budget. Jitter
avoids thundering-herd retries when several scrapes hit a flapping
mgmt server at the same instant.
Trade-off: a worst-case `2*15s + 200ms + 400ms` per call when every
endpoint hangs to its `Timeout`. Acceptable: that path means a real
outage and the scrape will be marked `ssv_up=0` regardless.

### YAML config with strict precedence and unknown-field rejection
Decision: a `-config <path>` flag loads a YAML file whose schema lives
in `internal/config`. Values fall through this precedence order:
explicit command-line flag > matching env var (which acts as the
flag's default) > YAML > built-in default. Unknown YAML keys fail the
load (`yaml.Decoder.KnownFields(true)`).
Rationale: lets operators put credentials in an ACL'ed file (out of
`sc qc`) while still allowing per-environment flag overrides at
runtime. Strict unknown-field handling catches typos that would
otherwise silently leave a setting at its default.
Trade-off: env-var-set flag values are treated as "explicit" by the
merge (since they pre-populate the flag default), so YAML cannot
override them. Acceptable: the user controls precedence by deciding
where to put each value. Booleans need a `*bool` in the YAML schema
to distinguish "absent" from "false".

### Session-based auth, with reauth on 401 and per-endpoint tokens
Decision: every REST call rides on a session opened via `POST
/sessions` (literal `Basic <user> <pass>`, NOT base64); resource calls
authenticate with `Authorization: Token <token>` (NOT `Bearer`). The
token is cached per `(baseURL, ServerHost)` endpoint, transparently
re-issued once on HTTP 401, revoked on shutdown via `Client.Close()`,
and preserved across `SetBackups` rebuilds when an endpoint survives.
Rationale: matches the official SPA contract documented in DataCore's
`DcsxDataProvider` (Self-Service Portal). The non-standard literal
forms are intentional — RFC 7617 / OAuth `Bearer` clients are rejected
on `/sessions`. Reusing one token across a scrape avoids one Windows
auth roundtrip per request (~80 calls / 15 s before, one OpenSession
per process lifetime now). Per-endpoint scoping mirrors how the IIS
bridge keys session state on (REST host, ServerHost) pairs, so
failover swaps both endpoint AND token together.
Trade-off: more state in the client (mutex+token per endpoint), and a
one-time pause on the first scrape per endpoint while the session
opens. The previous per-call `SetBasicAuth` did work on PSP 20 (SSV
accepts both forms on resource endpoints), but burned auth on every
request and could not survive a future hardening to "session-only".

### JSON-fault parsing in HTTPError
Decision: HTTP 4xx/5xx responses have their body decoded as the SSV
JSON fault shape (`{"ErrorCode": int, "ErrorMessage": string}`); the
parsed values land on `HTTPError.Code` / `Message` while `Body` keeps
the raw payload. `Error()` formats the structured form when present.
Rationale: SSV uses WCF's `JsonFault` behavior for every error response
(e.g. ErrorCode 9 = "No ServerHost header was passed."). Surfacing the
code makes operator triage faster and lets future logic switch on
specific error codes without string-matching the body.
Trade-off: silent fallback when the body isn't JSON (some error
intermediaries — IIS itself, a reverse proxy — may emit plain text or
HTML). The raw body remains available, so nothing is lost.

### Reauth triggered by either 401 OR 400 + stale-token marker
Decision: the auth-retry helper (`isStaleSession`, renamed from
`isUnauthorized`) considers a response "stale" if either (a) HTTP 401,
or (b) HTTP 400 with body containing `"Passed token is not valid for
this connection."`. The 400-with-marker case triggers exactly one
silent reopen + retry, the same path the 401 case has always taken.
Rationale: empirical — SANsymphony's REST surface does return HTTP 400
(NOT 401) when a previously valid token is invalidated server-side,
contrary to what RFC 7235 / the documented 401 expiry path suggest.
This was first surfaced by `ssa-collector` during a 30 h lab run on
2026-05-17 (fixed there in `bac55f9`) and then hit `ssv-prom-exporter`
on 2026-05-20 in both lab groups simultaneously after ~26 h of uptime.
With only-401 matching, the exporter never reauthed, kept presenting
the dead token, and got `ssv_up=0` forever on every collector.
The marker (`staleTokenMarker = "Passed token is not valid for this
connection"`) is matched as a substring of `HTTPError.Message` first,
falling back to `HTTPError.Body` if the JSON fault did not decode
cleanly.
Trade-off: the throttle response (`"Too many requests with wrong
credentials/token. You must wait N seconds…"`) is also a 400 — if we
ever broaden the matcher beyond the stale-token marker, we risk
retrying into the throttle window and escalating it. Guarded by an
explicit regression test (`TestSession_Throttle400DoesNotReopen`) that
asserts a single OpenSession on that body.

### NullCounterMap honored explicitly in Performance()
Decision: `Performance()` reads SSV's `NullCounterMap` field on each
`/performance/{id}` snapshot and skips counters listed there, instead
of relying on JSON `null` failing to decode as `int64`.
Rationale: documented in the SANsymphony REST contract — the bitmap
exists precisely so clients distinguish "counter unavailable" from
"counter is zero". Two shapes have been observed across PSP releases
(object `{name: bool}` and array `[name, ...]`); both are accepted
defensively. The "skip non-int64" fallback remains as belt-and-braces:
if SSV ever emits `0` instead of `null` for an unavailable counter
without listing it in `NullCounterMap`, only the bitmap saves us.
Trade-off: a counter genuinely set to `null` AND missing from
`NullCounterMap` is silently dropped. That's strictly better than
emitting a fake zero; consumers re-running rate() see the absence.

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
