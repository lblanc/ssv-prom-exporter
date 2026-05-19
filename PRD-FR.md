# PRD prototype SA — ssv-prom-exporter

> **Nom du SA :** Luc Blanc
> **Date :** 20/05/2026
> **Produit :** SANsymphony (intégration Prometheus / observabilité)
> **Région / Territoire :** EMEA
> **Type de prototype :** [ ] Nouvelle feature standalone  [ ] Amélioration d'une feature existante  [x] Intégration tierce  [ ] Outillage / sizing
> **Lien prototype :** https://github.com/lblanc/ssv-prom-exporter
> **Version du doc :** 1.0
> **Statut :** Prêt pour relecture PM

---

## 1. Qu'avez-vous construit ?

### 1.1 Synthèse en une ligne

Exporter Prometheus natif pour DataCore SANsymphony qui expose la topologie, la santé (monitors + alertes) et les métriques de performance sous forme de séries Prometheus standard, packagé en service Windows, unit systemd Linux et image OCI multi-arch.

### 1.2 Description du prototype

L'exporter scrape l'API REST de SANsymphony (`/RestService/rest.svc/1.0/`) et ré-émet chaque signal sous forme de métrique Prometheus sur `/metrics`. Trois tiers de collecteurs tournent à des cadences indépendantes : inventory (~60 s, topologie), health (~30 s, monitors + alertes), performance (~15 s, compteurs IO et latences par objet). Il tourne partout où le serveur de management SSV est joignable — nativement en service Windows (binaire unique, auto-installable via `-install`, handler EventLog), en unit systemd Linux durcie (DynamicUser, ProtectSystem=strict, SystemCallFilter), ou en container (`ghcr.io/lblanc/ssv-prom-exporter`, multi-arch, image finale ~34 MB). Un profil `docker-compose` `full` livre l'exporter + Prometheus + Grafana avec trois dashboards pré-provisionnés (Overview, Servers, Storage) pour une démo end-to-end. L'authentification est basée session (matche le contrat SPA `DcsxDataProvider` de DataCore : `Basic <user> <pass>` littéral sur `/sessions`, puis `Authorization: Token <token>` sur les appels ressources). Le failover des endpoints REST boucle sur chaque nœud de management du storage group, auto-découvert depuis `/servers`.

### 1.3 Livrables du prototype

| Livrable | Lien / Emplacement | Notes |
|----------|-------------------|-------|
| Code source / repo | https://github.com/lblanc/ssv-prom-exporter | Public ; licence MIT |
| Image OCI | `ghcr.io/lblanc/ssv-prom-exporter` | Multi-arch (amd64/arm64) ; tags `vX.Y.Z`, `X.Y`, `latest` |
| MSI Windows | GitHub Releases (per-machine, dépose exe + LICENSE + `config.example.yaml`) | Buildé via `wixl` |
| Tarball Linux | GitHub Releases (binaire + unit systemd durcie + `install-linux.sh`) | |
| Guide opérateur | `out/user-guide.pdf` (15 pages, screenshots embarqués) | Reconstruit depuis `out/user-guide.md` |
| Deck | `out/deck.pptx` (15 slides + 5 screenshots dashboards live-lab) | Reconstruit par `build_deck.py` |
| Aide web | `out/help.html` (sidebar accordéon style DataCore) | Chapitres Linux / Docker / full-stack |
| Dashboards Grafana | `deploy/grafana/dashboards/` — Overview / Servers / Storage / Ports / Hosts | Pré-provisionnés dans la stack démo |
| Documentation | `README.md`, `PROJECT_CONTEXT.md`, `DECISIONS.md`, `CHANGELOG.md` | |

---

## 2. Contexte client

### 2.1 Compte / client à l'origine

| Compte | Industrie / Segment | Contexte |
|---------|---------------------|----------|
| TBD | | |

### 2.2 Le problème client

Les clients qui exploitent SANsymphony à côté d'une stack d'observabilité moderne (Prometheus + Grafana, Mimir, VictoriaMetrics) n'ont aucun moyen first-party de consommer les signaux SSV depuis cette stack. Aujourd'hui ils se rabattent sur trois alternatives : scraper Windows perflib via WMI (ce qui colle le collecteur à un host Windows et rate les signaux santé / topologie), poller SNMP (pas de détail perf, pas de granularité par objet), ou écrire des scrapers REST custom (brittles — l'auth `Basic <user> <pass>` littérale non base64, le header `ServerHost` obligatoire, le format de date type .NET, et l'appel `/performance/{id}` par objet sont autant de gotchas à découvrir à la dure). Résultat : dashboards fragmentés, absence de visibilité latence / IO-time par vDisk / pool / host / port, et aucun contrat de support sur le collecteur lui-même. DataCore Insight couvre la télémétrie hébergée par DataCore, mais ne nourrit pas la stack Prometheus du client.

### 2.3 Ampleur du problème

| Combien de comptes l'ont remonté ? | Bloque un deal ou un renew ? | Remonté aussi par les partenaires ? |
|------------------------------------|------------------------------|-------------------------------------|
| # comptes : ____ | [ ] Oui — nom du deal : ____ | [ ] Oui |
| Segment(s) : ____ | [ ] Non | [ ] Non — Partenaire : ____ |

### 2.4 Cas d'usage couverts

- UC-01 : Client avec stack observabilité Prometheus + Grafana existante qui veut les métriques SANsymphony à côté de son autre infra (hyperviseurs, switches, applis), sans écrire de collecteur custom.
- UC-02 : TAM / Support qui a besoin de visibilité latence et IO-time par objet sur un cluster client pendant une mission de perf-triage.
- UC-03 : SE / SA qui démontre l'observabilité DataCore en POC en levant la stack `docker-compose --profile full` bundled contre le lab du prospect.

---

## 3. Problem statement

### 3.1 Problème

SANsymphony expose un riche set de signaux via son API REST — topologie, monitors, alertes, compteurs de performance par objet incluant latence et IO-time — mais DataCore ne livre aucune surface Prometheus first-party par-dessus. Les clients sur stack d'observabilité moderne (Prometheus + Grafana, Mimir, Thanos, VictoriaMetrics) se rabattent sur trois workarounds, tous partiels :

- **SNMP / Windows perflib / WMI** : colle le collecteur à l'hôte Windows qui héberge SSV, n'expose qu'une fraction des compteurs, et n'a pas de granularité par objet (pas de latence par pool, pas de compteurs d'erreurs par port, pas de bande passante par host).
- **Telegraf / scrapers custom** : doivent ré-implémenter le contrat non-standard SSV (auth `Basic <user> <pass>` littérale, header `ServerHost` obligatoire devant matcher l'IP appelée, parsing `/Date(ms+tz)/`, fan-out par objet sur `/performance/{id}`, filtrage du `NullCounterMap`). Chaque déploiement re-découvre les mêmes gotchas.
- **Insight / télémétrie cloud** : hébergée par le vendor, ne nourrit pas le Prometheus du client.

Le gap est aussi visible côté dashboards : les vues cross-cutting (latence vDisk par pool, alertes par host, erreurs port dans le temps) sont indisponibles hors de la GUI SSV, donc le Day-2 ops sur une stack multi-systèmes devient du tab-switching.

### 3.2 Pourquoi maintenant ?

- **Parité concurrentielle.** Tous les vendors storage modernes livrent désormais un exporter Prometheus first-party : Pure Storage (Pure FlashArray exporter + dashboards Grafana publiés par Pure), NetApp (Harvest 2, officiellement maintenu), Dell PowerStore (endpoint de métriques natif compatible scrape Prometheus). DataCore est l'outlier.
- **Demande récurrente terrain.** Partenaires et SEs sur stack Prometheus ont à plusieurs reprises construit des scrapers SSV jetables pour des missions individuelles — les mêmes gotchas (auth, ServerHost header, .NET dates) sont re-découverts à chaque fois.
- **Attente écosystème Grafana.** Les clients attendent un exporter publié par le vendor + un set de dashboards Grafana (sur `grafana.com/dashboards`) comme baseline. Son absence est lue comme "DataCore = observabilité fermée".
- **Pull opérationnel interne DataCore.** TAM / Support ont besoin de latence et IO-time par objet dans le temps pendant les missions de perf-triage ; la console SSV seule force une observation temps réel uniquement.

### 3.3 Urgence et signal business

| Signal d'urgence | Deal actif impacté ? | Pression concurrentielle ? | Comptes clients affectés |
|------------------|---------------------|----------------------------|--------------------------|
| [ ] Deal at risk | [ ] Oui | [ ] Oui | # comptes : ____ |
| [ ] Gap concurrentiel | Nom du deal : ____ | Concurrent : ____ | Segment : ____ |
| [ ] Demande récurrente | | | |
| [ ] Nice to have | | | |

---

## 4. Solution proposée

### 4.1 Synthèse de la solution

Un binaire Go unique qui tourne en service Windows, unit systemd Linux ou container, et expose les métriques SANsymphony sur un endpoint Prometheus standard `/metrics`. Trois tiers de collecteurs indépendants (inventory, health, performance) alignent les cadences de refresh sur la volatilité de chaque signal. Trois packagings production-grade (MSI, tarball Linux, image OCI) matchent comment les clients déploient effectivement leurs composants d'infra aujourd'hui. Cinq dashboards Grafana (Overview / Servers / Storage / Ports / Hosts) sont bundled et provisionnés automatiquement dans une stack `docker-compose --profile full`, donc un POC se fait en un seul `docker compose up`. L'exporter a été validé end-to-end contre un lab PSP 20 (~470 ms de scrape perf, ~470 séries au total sur les trois collecteurs). La v0.8 est feature-complete sur `main` ; le tag v0.8.0 est la prochaine étape de release.

### 4.2 User stories

**Story 1**
En tant qu'ingénieur ops client qui exploite une stack Prometheus
Je veux un exporter Prometheus fourni par DataCore pour SANsymphony
Afin d'obtenir topologie, santé et latence dans Grafana sans écrire de scrapers custom.

**Story 2**
En tant que TAM en mission de perf-triage
Je veux des métriques IO-time et latence par objet avec rate() dans le temps
Afin d'identifier un vdisk / pool / host dégradé sans avoir à m'asseoir devant la GUI SSV.

**Story 3**
En tant que SA en POC
Je veux une stack démo en une commande (`docker compose --profile full up -d`)
Afin que le prospect voie DataCore Prometheus + Grafana en 5 minutes.

### 4.3 Critères d'acceptation (vue SA)

- [x] AC1 : Binaire unique, tourne en service Windows / systemd Linux / container OCI — même exe.
- [x] AC2 : Auto-installable sur Windows (`-install` / `-uninstall`) ; handler EventLog en mode service.
- [x] AC3 : Unit systemd Linux durcie (DynamicUser, ProtectSystem=strict, SystemCallFilter=@system-service).
- [x] AC4 : Trois tiers de collecteurs (inventory / health / performance) à cadences de refresh indépendantes.
- [x] AC5 : Failover des endpoints REST (découverts depuis `/servers`) avec endpoint préféré sticky + TTL 5 min.
- [x] AC6 : Auth par session matchant le contrat SPA officiel SSV (`/sessions` Basic + `Token <token>`, réauth sur 401).
- [x] AC7 : Stack démo Prometheus + Grafana bundled avec trois dashboards pré-provisionnés.
- [x] AC8 : Image OCI multi-arch (~34 MB) publiée sur GHCR par le workflow de release.
- [ ] AC9 : Validation PSP 21 / 22 — confirmer le flow d'auth session et la shape de l'endpoint perf sur les releases plus récentes.
- [ ] AC10 : Flag de CA custom (l'opérateur peut flipper `-insecure` off sur les sites avec PKI interne).

### 4.4 Hors scope

- Aucun write path. L'exporter consomme l'API REST SSV en read-only et expose des métriques Prometheus ; il n'agit pas sur le cluster.
- Pas de logique d'alerting dans l'exporter. L'alerting est laissé à Prometheus / Alertmanager par-dessus les métriques exposées.
- Pas de mapping enum vendor pour les métriques d'état en v0.8 (`ssv_server_state`, `ssv_pool_status`, `ssv_host_state`, …). Les dashboards traduisent encore via Grafana value mappings — déplacé sur la roadmap v0.9.
- Pas de stockage long terme des métriques. Prometheus est le tier de stockage ; l'exporter est stateless.
- Pas de failover IPv6. Les IPs IPv6 découvertes (link-local et publiques) sont skippées — le service REST SSV bind typiquement IPv4 only dans IIS.
- Pas d'endpoint `/performanceCounters` pluriel (il n'existe pas) ; pas de `/performanceByType/{type}` (renvoie systématiquement `[]`).

---

## 5. Notes techniques pour l'équipe Engineering

### 5.1 Stack technique utilisée

| Composant | Technologie / Version | Notes |
|-----------|----------------------|-------|
| Langage | Go 1.26 | CGO_ENABLED=0 ; cross-compile cleanly Linux→Windows |
| Métriques | `github.com/prometheus/client_golang` | Format d'exposition Prometheus standard |
| Service Windows | `golang.org/x/sys/windows/svc` (+ `/mgr` install, `/eventlog` log handler) | Binaire unique auto-installable |
| Config | env vars (v0) + config YAML (`gopkg.in/yaml.v3`, KnownFields strict) | Précédence flag > env > YAML > défaut |
| HTTP client | stdlib `net/http` | Auth session, header ServerHost, InsecureSkipVerify par défaut |
| Base container | `alpine:3` multi-stage, nonroot uid 65532, tini, HEALTHCHECK wget | Image finale ~34 MB |
| Packaging Linux | unit systemd durcie + `install-linux.sh` (idempotent) | DynamicUser, ProtectSystem=strict, NoNewPrivileges, SystemCallFilter |
| Packaging Windows | MSI WiX buildé via `wixl` (Debian) | Install per-machine ; enregistrement du service laissé à l'opérateur |
| Pipeline release | GitHub Actions (`.github/workflows/release.yml`) | Trigger sur tags `v*` ; publie MSI + tarball Linux + image multi-arch |
| CI | GitHub Actions (`.github/workflows/ci.yml`) | go vet, build, test, cross-compile windows/amd64, docker build (no push) |

### 5.2 Points d'intégration

| Système / API | Comment c'est utilisé | Limites connues |
|---------------|------------------------|-----------------|
| SSV REST (`/RestService/rest.svc/1.0/`) | Topologie, santé, performance ; auth session (`/sessions` Basic + `Token <token>`) | Le header `ServerHost` est obligatoire (HTTP 400 + `ErrorCode 9` sans). Les hostnames de `/servers[].HostName` sont rejetés — `ServerHost` doit matcher l'IP. Certains IDs de pool contiennent `:` et `{` et doivent être path-escapés. |
| SSV `/performance/{instanceId}` | Un appel par objet (servers, pools, vdisks, physical disks, ports, hosts), worker pool borné (8 par défaut concurrent) | Pas de forme batch. La réponse est toujours un array d'un élément (`[0]` à dépaqueter). `NullCounterMap` liste les compteurs à skipper. Les timers sont en millisecondes — multipliés par `timeScale = 1e-3` pour respecter la convention Prometheus en secondes (validé empiriquement sur PSP 20 : Δ-time / Δ-ops dans la fourchette 0,6–2,8 ms). |
| SSV `/servers` pour la découverte du failover | Backups appendés après chaque fetch réussi ; filtrés CIDR (défaut = `/24` du primary) ; IPv4 only | Bootstrap impose le primary au premier scrape ; `-bases` pré-seed les backups. Les déploiements multi-subnet doivent override `-backup-cidrs`. |
| Prometheus | Scrape `/metrics` (port défaut `:9876`) | Cache server-side SSV de 30 s (`RequestExpirationTime`) — un scrape Prometheus plus rapide ne verra pas de nouvelle donnée. |
| Grafana | Cinq dashboards pré-provisionnés | Anonymous Viewer activé dans la stack démo pour un accès read-only rapide. |

### 5.3 Limites connues et raccourcis pris

> Engineering doit savoir ce qui a été coupé pour évaluer le vrai coût d'industrialisation.

- **`-insecure` défaut à `true`** — les serveurs de management SSV sont livrés avec des certs auto-signés dans la majorité des déploiements. Un flag de CA custom est planifié (déjà en roadmap) pour que les opérateurs avec PKI interne puissent flipper la vérification on.
- **PSP 21 / 22 pas encore validés**. L'implémentation actuelle tourne contre PSP 20. Le flow d'auth session, le header ServerHost et la shape de `/performance/{id}` sont attendus stables, mais un sanity check sur chaque release plus récente reste à faire.
- **Pas de mapping enum vendor pour les métriques d'état en v0.8**. Les valeurs d'état sortent en codes numériques bruts (`ssv_server_state`, `ssv_pool_status`, `ssv_host_state`, …) ; les dashboards traduisent via Grafana value mappings aujourd'hui. Engineering peut décider de livrer les tables d'enum ou de les exposer en métriques `_info`.
- **Extras pool non encore exposés** : `EstimatedDepletionTime`, `MaxTierNumber`, `TierReservedPct`, `InSharedMode`. Surfacés par les Grafana boards d'inspiration mais manquants en v0.8.
- **`PercentAllocated` sur les vdisks non encore exposé** si la shape REST l'inclut (l'inspiration InfluxDB l'utilise).
- **Secret de l'ImagePath du service** : tout ce qui est passé via `-pass` sur `-install` atterrit dans la sortie de `sc qc` (lisible par tout user avec `SeQueryServiceConfigPrivilege`). La commande d'install warn si `-pass` est utilisé sans `-config`. La config YAML (recommandée) garde les credentials hors de `sc qc`.
- **L'EventLog en mode service aplatit les logs structurés** en chaîne unique — Prometheus est le sink data structurée de toute façon, donc c'est un trade-off accepté, pas un défaut à corriger.
- **NTLM non implémenté** : l'exporter parle HTTPS Basic + auth session simple. SSV n'impose pas NTLM sur l'endpoint REST ; si un futur durcissement le faisait, le client aurait besoin d'un transport différent.
- **Failover IPv6 skippé volontairement**. Le service REST SSV bind typiquement IPv4 only dans IIS ; l'IPv6 link-local (`fe80::/10`) n'est jamais un backup utile. Un changement de code est requis pour un déploiement IPv6 only.

### 5.4 Chemin de productisation suggéré _(optionnel)_

L'exporter est shapé pour atterrir dans le portfolio produit DataCore comme composant supporté : binaire unique, trois packagings, repo open MIT (ou re-licence si nécessaire). L'ownership engineering est essentiellement le contrat REST SSV — le reste est de l'exposition Prometheus standard. Les cinq dashboards Grafana sont aussi des artifacts officiels valorisables et pourraient être hébergés sur `grafana.com/dashboards`. L'articulation de l'exporter avec DataCore Insight mérite une design discussion — ils visent des audiences différentes (Insight = hosted DataCore, exporter = hosted client) mais la shape des métriques pourrait converger.

---

## 6. Business case

### 6.1 Estimation d'impact ARR

| Scénario | Comptes / Deals | Impact ARR estimé |
|----------|-----------------|-------------------|
| Immédiat (deals at risk) | TBD | $____k |
| Court terme (6-12 mois) | TBD | $____k |
| Long terme (12+ mois) | TBD | $____k |

### 6.2 Contexte concurrentiel

| Concurrent | Leur capacité | Notre gap |
|------------|---------------|-----------|
| Pure Storage | Exporter Prometheus natif + dashboards Grafana publiés par Pure | DataCore n'a pas d'exporter first-party |
| NetApp (Harvest) | NetApp Harvest 2 (exporter Prometheus open-source, officiel) | DataCore n'a pas d'équivalent |
| Dell PowerStore | Endpoint de métriques natif + scrape Prometheus compatible | DataCore n'a pas d'équivalent |

### 6.3 Que se passe-t-il si on ne livre pas ce produit ?

- **Les clients continuent à construire des scrapers one-off.** Chaque site re-découvre les mêmes gotchas REST SSV, livre un hack Python / Telegraf vers un seul dashboard, et n'a pas de contrat de support dessus. Les pannes ressemblent à "DataCore est cassé".
- **Les missions SE / SA perdent des heures par POC.** Chaque POC contre un prospect Prometheus-shop demande de la plomberie custom pour faire émerger les métriques SSV ; l'absence d'une stack démo en une commande pousse le "et la latence vdisk, on la voit où ?" à la toute fin de la mission.
- **La perception concurrentielle dérive vers "observabilité fermée".** Pure, NetApp Harvest, PowerStore atterrissent sur les shortlists prospects avec un support Prometheus natif out-of-the-box ; DataCore se prend la question sur chaque RFP sans réponse propre.
- **TAM / Support continuent à voler à l'aveugle en time-series.** Sans exporter, la revue perf post-incident dépend du snapshot GUI SSV plutôt que d'un rate() sur Δt. La boucle de résolution case est plus longue qu'elle ne devrait l'être.

---

## 7. Pièces jointes et références

| Type | Lien / Description | Notes |
|------|--------------------|-------|
| Notes de conversation client | TBD | |
| Analyse concurrentielle | TBD | |
| Enregistrement démo prototype | TBD | |
| Demandes de feature liées | TBD | |
| Tickets support pertinents | TBD | |
| Skill DataCore (REST) | Skill Claude `sansymphony-rest` | Documente le flow d'auth non-standard SSV utilisé par l'exporter |
| Validation lab | Lab PSP 20 `10.12.110.11:9876` | `ssv_up=1` sur les trois collecteurs |

---

## 8. Historique des révisions

| Version | Date | Auteur | Changements |
|---------|------|--------|-------------|
| 1.0 | 20/05/2026 | Luc Blanc | Version FR initiale, mirror de la version EN v1.0 |

---

_Template PRD Prototype SA v1.0 — DataCore Software — Usage interne uniquement_
