# SA Prototype PRD — ssv-prom-exporter

> **SA Name:** Luc Blanc
> **Date:** 20/05/2026
> **Product:** SANsymphony (Prometheus / observability integration)
> **Region / Territory:** EMEA
> **Prototype type:** [ ] New standalone feature  [ ] Enhancement to existing  [x] Third-party integration  [ ] Sizing / tooling
> **Prototype link:** https://github.com/lblanc/ssv-prom-exporter
> **Doc version:** 1.1
> **Status:** Ready for PM review — v0.8.0 cut 2026-05-20

---

## 1. What Did You Build?

### 1.1 One-line summary

Native Prometheus exporter for DataCore SANsymphony that exposes topology, health (monitors + alerts), and performance metrics as standard Prometheus series, packaged as a Windows service, Linux systemd unit, and multi-arch OCI image.

### 1.2 Prototype description

The exporter scrapes SANsymphony's REST API (`/RestService/rest.svc/1.0/`) and re-emits every signal as Prometheus metrics on `/metrics`. Three collector tiers run on independent refresh rates: inventory (~60s, topology), health (~30s, monitors + alerts), performance (~15s, per-object IO counters + latencies). It runs anywhere reachable to the SSV management server — natively as a Windows service (single binary, self-installable via `-install`, EventLog handler), as a hardened Linux systemd unit (DynamicUser, ProtectSystem=strict, SystemCallFilter), or as a container (`ghcr.io/lblanc/ssv-prom-exporter`, multi-arch, ~34 MB final image). A bundled `docker-compose` `full` profile ships the exporter + Prometheus + Grafana with three provisioned dashboards (Overview, Servers, Storage) for an end-to-end demo. Authentication is session-based (matches DataCore's `DcsxDataProvider` SPA contract: literal `Basic <user> <pass>` on `/sessions`, then `Authorization: Token <token>` on resource calls). REST endpoint failover loops through every management node in the storage group, discovered automatically from `/servers`.

### 1.3 Prototype artifacts

| Artifact | Link / Location | Notes |
|----------|----------------|-------|
| Source code / repo | https://github.com/lblanc/ssv-prom-exporter | Public; MIT license |
| OCI image | `ghcr.io/lblanc/ssv-prom-exporter` | Multi-arch (amd64/arm64); tags `vX.Y.Z`, `X.Y`, `latest` |
| Windows MSI | GitHub Releases (per-machine, drops exe + LICENSE + `config.example.yaml`) | Built via `wixl` |
| Linux tarball | GitHub Releases (binary + hardened systemd unit + `install-linux.sh`) | |
| Operator user guide | `out/user-guide.pdf` (15 pages, screenshots embedded) | Rebuilt from `out/user-guide.md` |
| Deck | `out/deck.pptx` (15 slides + 5 live-lab dashboard screenshots) | Rebuilt by `build_deck.py` |
| Web help | `out/help.html` (DataCore-style accordion sidebar) | Linux / Docker / full-stack chapters |
| Grafana dashboards | `deploy/grafana/dashboards/` — Overview / Servers / Storage / Ports / Hosts | Provisioned automatically in the demo stack |
| Documentation | `README.md`, `PROJECT_CONTEXT.md`, `DECISIONS.md`, `CHANGELOG.md` | |

---

## 2. Customer Context

### 2.1 Originating customer / account

| Account | Industry / Segment | Context |
|---------|-------------------|---------|
| TBD | | |

### 2.2 The customer problem

Customers operating SANsymphony alongside a modern observability stack (Prometheus + Grafana, Mimir, VictoriaMetrics) have no first-party way to consume SSV signals from that stack. Today they either: scrape Windows perflib over WMI (ties the collector to a Windows host and misses health/topology), poll SNMP (no perf detail, no per-object granularity), or write custom REST scrapers (brittle — SSV's non-standard `Basic <user> <pass>` literal auth, mandatory `ServerHost` header, .NET-style date format, and per-object `/performance/{id}` calls are all gotchas to discover the hard way). The result is fragmented dashboards, missing latency / IO-time visibility on vDisks / pools / hosts / ports, and no support contract on the collector itself. DataCore Insight covers vendor-hosted telemetry, but does not feed the customer's own Prometheus tier.

### 2.3 How widespread is this problem?

| How many accounts have raised this? | Is it blocking a deal or renewal? | Have you heard this from partners? |
|-------------------------------------|----------------------------------|-----------------------------------|
| # accounts: ____ | [ ] Yes - deal name: ____ | [ ] Yes |
| Segment(s): ____ | [ ] No | [ ] No - Partner: ____ |

### 2.4 Use cases covered

- UC-01: Customer with an existing Prometheus + Grafana observability stack wants SANsymphony metrics alongside their other infra (hypervisors, switches, apps), without writing a custom collector.
- UC-02: TAM / Support needs per-object latency and IO-time visibility on a customer cluster during a perf-triage engagement.
- UC-03: SE / SA demonstrates DataCore observability in a POC by spinning up the bundled `docker-compose --profile full` stack against the prospect's lab.

---

## 3. Problem Statement

### 3.1 Problem

SANsymphony exposes a rich set of signals through its REST API — topology, monitors, alerts, per-object performance counters including latency and IO-time — but DataCore ships no first-party Prometheus surface on top of them. Customers running modern observability stacks (Prometheus + Grafana, Mimir, Thanos, VictoriaMetrics) fall back to three workarounds, all partial:

- **SNMP / Windows perflib / WMI**: ties the collector to the Windows host running SSV, exposes a fraction of the counters, and has no per-object granularity (no per-pool latency, no per-port error counters, no per-host bandwidth).
- **Telegraf / custom scrapers**: must re-implement SSV's non-standard contract (`Basic <user> <pass>` literal auth, mandatory `ServerHost` header that must match the IP being hit, .NET-style `/Date(ms+tz)/` parsing, per-object `/performance/{id}` fan-out, NullCounterMap filtering). Each deployment re-discovers the same gotchas.
- **Insight / cloud telemetry**: vendor-hosted, does not feed the customer's own Prometheus.

The gap is also visible on the dashboard side: cross-cutting views (vDisk latency by pool, alerts by host, port errors over time) are unavailable outside the SSV GUI, so Day-2 ops on a multi-system stack mean tab-switching.

### 3.2 Why now?

- **Competitive parity.** Every modern storage vendor now ships a first-party Prometheus exporter: Pure Storage (Pure FlashArray exporter + Pure-published Grafana dashboards), NetApp (Harvest 2, officially maintained), Dell PowerStore (native metrics endpoint with Prometheus-compatible scrape). DataCore is the outlier.
- **Recurring field ask.** Partners and SEs running Prometheus stacks have repeatedly built throwaway SSV scrapers for individual engagements — the same gotchas (auth, ServerHost header, .NET dates) are re-discovered each time.
- **Grafana ecosystem expectation.** Customers expect a vendor-published exporter plus a set of Grafana dashboards (on `grafana.com/dashboards`) as a baseline. Its absence is read as "DataCore = closed observability".
- **Operational pull from inside DataCore.** TAM / Support need per-object latency and IO-time over time during perf triage; the SSV console alone forces real-time observation only.

### 3.3 Urgency and business signal

| Urgency Signal | Active deal impacted? | Competitive pressure? | Customer accounts affected |
|---------------|----------------------|----------------------|--------------------------|
| [ ] Deal at risk | [ ] Yes | [ ] Yes | # accounts: ____ |
| [ ] Competitive gap | Deal name: ____ | Competitor: ____ | Segment: ____ |
| [ ] Recurring ask | | | |
| [ ] Nice to have | | | |

---

## 4. Proposed Solution

### 4.1 Solution summary

A single Go binary that runs as a Windows service, Linux systemd unit, or container, and exposes SANsymphony metrics on a standard Prometheus `/metrics` endpoint. Three independent collector tiers (inventory, health, performance) keep refresh rates aligned with each signal's volatility. Three production-grade packagings (MSI, Linux tarball, OCI image) match how customers actually deploy infrastructure components today. Five Grafana dashboards (Overview / Servers / Storage / Ports / Hosts) are bundled and provisioned automatically in a `docker-compose --profile full` stack, so a POC takes one `docker compose up`. The exporter has been validated end-to-end against a PSP 20 lab (~470 ms perf scrape, ~470 series across the three collectors). **v0.8.0 was cut on 2026-05-20**, after a late stale-session reauth fix surfaced by a 26 h cross-lab uptime test: SANsymphony returns HTTP 400 (not 401) when a cached session token is invalidated server-side, and the client now matches both shapes.

### 4.2 User stories

**Story 1**
As a customer ops engineer running a Prometheus stack
I want a DataCore-provided Prometheus exporter for SANsymphony
So that I get topology, health, and latency in Grafana without writing custom scrapers.

**Story 2**
As a TAM running a performance-triage engagement
I want per-object IO-time and latency metrics with rate() over time
So that I can pinpoint a degraded vdisk / pool / host without sitting in front of the SSV GUI.

**Story 3**
As an SA running a POC
I want a one-command demo stack (`docker compose --profile full up -d`)
So that the prospect sees DataCore Prometheus + Grafana in 5 minutes.

### 4.3 Acceptance criteria (from the SA perspective)

- [x] AC1: Single binary, runs as Windows service / Linux systemd / OCI container — same exe.
- [x] AC2: Self-installable on Windows (`-install` / `-uninstall`); EventLog handler in service mode.
- [x] AC3: Hardened Linux systemd unit (DynamicUser, ProtectSystem=strict, SystemCallFilter=@system-service).
- [x] AC4: Three collector tiers (inventory / health / performance) with independent refresh rates.
- [x] AC5: REST endpoint failover (discovered from `/servers`) with sticky preferred endpoint + 5 min TTL.
- [x] AC6: Session-based auth matching the official SSV SPA contract (`/sessions` Basic + `Token <token>`, 401 reauth).
- [x] AC7: Bundled Prometheus + Grafana demo stack with three provisioned dashboards.
- [x] AC8: Multi-arch OCI image (~34 MB) published to GHCR by the release workflow.
- [x] AC9: Stale-session reauth fix — client matches both HTTP 401 and HTTP 400 + `"Passed token is not valid for this connection."` (PSP 20 contract). Two regression tests; surfaced by a 26 h cross-lab uptime test on 2026-05-20.
- [ ] AC10: PSP 21 / 22 validation — confirm session-auth flow and perf endpoint shape on newer releases.
- [ ] AC11: Custom CA pool flag (operator can flip `-insecure` off in sites with internal PKI).

### 4.4 Out of scope

- No write paths. The exporter consumes the SSV REST API read-only and exposes Prometheus metrics; it does not act on the cluster.
- No alerting logic inside the exporter. Alerting is left to Prometheus / Alertmanager on top of the exposed metrics.
- No vendor-enum mapping for state metrics in v0.8 (`ssv_server_state`, `ssv_pool_status`, `ssv_host_state`, …). Dashboards still translate via Grafana value mappings — moved into the v0.9 roadmap.
- No long-term metrics storage. Prometheus is the storage tier; the exporter is stateless.
- No IPv6 failover. Discovered IPv6 IPs (link-local and public) are skipped — SSV's REST service typically binds IPv4 only in IIS.
- No `/performanceCounters` plural endpoint (it does not exist); no `/performanceByType/{type}` (consistently returns `[]`).

---

## 5. Technical Notes for Engineering

### 5.1 Technology stack used

| Component | Technology / Version | Notes |
|-----------|---------------------|-------|
| Language | Go 1.26 | CGO_ENABLED=0; cross-compiles cleanly Linux→Windows |
| Metrics | `github.com/prometheus/client_golang` | Standard Prometheus exposition format |
| Windows service | `golang.org/x/sys/windows/svc` (+ `/mgr` install, `/eventlog` log handler) | Self-installable single binary |
| Config | env vars (v0) + YAML config (`gopkg.in/yaml.v3`, KnownFields strict) | Flag > env > YAML > default precedence |
| HTTP client | stdlib `net/http` | Session auth, ServerHost header, InsecureSkipVerify by default |
| Container base | `alpine:3` multi-stage, nonroot uid 65532, tini, wget HEALTHCHECK | Final image ~34 MB |
| Linux packaging | hardened systemd unit + `install-linux.sh` (idempotent) | DynamicUser, ProtectSystem=strict, NoNewPrivileges, SystemCallFilter |
| Windows packaging | WiX MSI built via `wixl` (Debian) | Per-machine install; service registration left to operator |
| Release pipeline | GitHub Actions (`.github/workflows/release.yml`) | Triggers on `v*` tags; publishes MSI + Linux tarball + multi-arch image |
| CI | GitHub Actions (`.github/workflows/ci.yml`) | go vet, build, test, windows/amd64 cross-compile, docker build (no push) |

### 5.2 Integration points

| System / API | How it is used | Known limitations |
|-------------|---------------|------------------|
| SSV REST (`/RestService/rest.svc/1.0/`) | Topology, health, performance; session auth (`/sessions` Basic + `Token <token>`) | `ServerHost` header is mandatory (HTTP 400 + `ErrorCode 9` without). Hostnames in `/servers[].HostName` are rejected — `ServerHost` must match the IP. Some pool IDs contain colons + curly braces and must be path-escaped. |
| SSV `/performance/{instanceId}` | One call per object (servers, pools, vdisks, physical disks, ports, hosts), bounded worker pool (default 8 concurrent) | No batch form. Response is always a one-element array (must unwrap `[0]`). `NullCounterMap` lists counters to skip. Timers are milliseconds — multiplied by `timeScale = 1e-3` for Prometheus seconds convention (verified empirically on PSP 20: Δ-time / Δ-ops in the 0.6–2.8 ms range). |
| SSV `/servers` for failover discovery | Backups appended after each successful fetch; CIDR-filtered (default = primary's `/24`); IPv4-only | Bootstrap requires the primary on first scrape; `-bases` pre-seeds backups. Multi-subnet management deployments must override `-backup-cidrs`. |
| Prometheus | Scrapes `/metrics` (default port `:9876`) | 30s server-side SSV cache (`RequestExpirationTime`) means faster Prometheus scrapes won't see new data. |
| Grafana | Five provisioned dashboards | Anonymous Viewer enabled in the demo stack for fast read-only access. |

### 5.3 Known limitations and shortcuts

> Engineering needs to know what corners were cut to assess real build effort.

- **`-insecure` defaults to `true`** — SSV management servers ship with self-signed certs in most deployments. A custom CA pool flag is planned (already on the roadmap) so operators with an internal PKI can flip verification on.
- **Stale-session reauth path is empirical, not documented.** SANsymphony returns HTTP 400 (not 401) with body `"Passed token is not valid for this connection."` when a previously valid token is invalidated server-side. The client matches both shapes; if a future PSP changes the message text, the marker (`staleTokenMarker` in `internal/ssv/client.go`) needs to be extended. The throttle response (`"Too many requests with wrong credentials/token. You must wait N seconds…"`) is also a 400 but deliberately NOT matched to avoid escalating the throttle window — covered by `TestSession_Throttle400DoesNotReopen`.
- **PSP 21 / 22 not yet validated**. The current implementation runs against PSP 20. Session-auth flow, ServerHost header, and `/performance/{id}` shape are expected to be stable, but a sanity check on each newer release is still pending.
- **No vendor-enum mapping for state metrics in v0.8**. State values surface as raw numeric codes (`ssv_server_state`, `ssv_pool_status`, `ssv_host_state`, …); dashboards translate via Grafana value mappings today. Engineering may decide whether to ship enum tables or expose them as `_info` metrics.
- **Pool extras not yet exposed**: `EstimatedDepletionTime`, `MaxTierNumber`, `TierReservedPct`, `InSharedMode`. Surfaced by the inspiration Grafana boards but missing from v0.8.
- **`PercentAllocated` on vdisks not yet exposed** if the REST shape includes it (InfluxDB inspiration uses it).
- **Service ImagePath secrecy**: anything passed via `-pass` on `-install` lands in `sc qc` output (readable by any user with `SeQueryServiceConfigPrivilege`). The install command warns when `-pass` is used without `-config`. YAML config (recommended) keeps credentials out of `sc qc`.
- **EventLog in service mode flattens structured logs** to a single string — Prometheus is the structured-data sink anyway, so this is an accepted trade-off, not a defect to fix.
- **NTLM not implemented**: the exporter speaks plain HTTPS Basic + session auth. SSV doesn't require NTLM on the REST endpoint; if a future hardening did, the client would need a different transport.
- **IPv6 failover skipped on purpose**. SSV's REST service typically binds IPv4 only in IIS; link-local IPv6 (`fe80::/10`) is never a useful backup. Code change required for an IPv6-only deployment.

### 5.4 Suggested productization path _(optional)_

The exporter is shaped to land in DataCore's product portfolio as a supported component: single binary, three packagings, MIT-licensed open repo (or re-license if needed). Engineering ownership is mostly the SSV REST contract — the rest is standard Prometheus exposition. The five Grafana dashboards are also valuable as official artifacts and could be hosted on `grafana.com/dashboards`. Pairing the exporter with DataCore Insight is worth a design discussion — they target different audiences (Insight = DataCore-hosted, exporter = customer-hosted) but the metric shape could converge.

---

## 6. Business Case

### 6.1 Estimated ARR impact

| Scenario | Accounts / Deals | Estimated ARR impact |
|----------|-----------------|---------------------|
| Immediate (deals at risk) | TBD | $____k |
| Short-term (6-12 months) | TBD | $____k |
| Long-term (12+ months) | TBD | $____k |

### 6.2 Competitive context

| Competitor | Their capability | Our gap |
|-----------|----------------|---------|
| Pure Storage | Native Prometheus exporter + Pure-published Grafana dashboards | DataCore has no first-party exporter |
| NetApp (Harvest) | NetApp Harvest 2 (open-source Prometheus exporter, official) | DataCore has no equivalent |
| Dell PowerStore | Native metrics endpoint + Prometheus-compatible scrape | DataCore has no equivalent |

### 6.3 What happens if we do not ship this?

- **Customers keep building one-off scrapers.** Each customer site re-discovers the same SSV REST gotchas, ships a Python/Telegraf hack to a single dashboard, and has no support contract on it. Failures look like "DataCore is broken".
- **SE / SA engagements lose hours per POC**. Every POC against a Prometheus-shop prospect needs custom plumbing to surface SSV metrics; the absence of a one-command demo stack pushes "wait, where do I see vDisk latency?" to the end of the engagement.
- **Competitive perception drifts toward "closed observability".** Pure, NetApp Harvest, PowerStore land on prospect shortlists with native Prometheus support out of the box; DataCore is asked the question on every RFP and has no clean answer.
- **TAM / Support keep flying blind on time series.** Without an exporter, post-incident perf review depends on SSV's GUI snapshot rather than rate() over Δt. The case-resolution loop is longer than it needs to be.

---

## 7. Attachments and References

| Type | Link / Description | Notes |
|------|--------------------|-------|
| Customer conversation notes | TBD | |
| Competitive analysis | TBD | |
| Prototype demo recording | TBD | |
| Related feature requests | TBD | |
| Relevant support tickets | TBD | |
| DataCore skill (REST) | `sansymphony-rest` Claude skill | Documents the non-standard SSV auth flow used by the exporter |
| Lab validation | PSP 20 lab `10.12.110.11:9876` | `ssv_up=1` on all three collectors |

---

## 8. Revision History

| Version | Date | Author | Changes |
|---------|------|--------|---------|
| 0.1 | 20/05/2026 | Luc Blanc | Initial draft, pre-filled sections 1, 4, 5 from project context + DECISIONS.md; sections 2, 3, 6 pending field input |
| 1.0 | 20/05/2026 | Luc Blanc | Filled in prose sections 2.2, 3.1, 3.2, 6.3 from project context; only field-specific numerics (account name, ARR, deal name) left as placeholders |
| 1.1 | 20/05/2026 | Luc Blanc | v0.8.0 cut; AC9 added (stale-session reauth fix on 400 + marker); §5.3 mentions the empirical reauth contract; AC10 / AC11 reshuffled from previous AC9 / AC10 |

---

_SA Prototype PRD Template v1.0 — DataCore Software — Internal use only_
