# fleetly

**English** | [简体中文](README_ZH.md)

fleetly is a lightweight, self-hosted PaaS. It turns a container image or a source tree into workloads on a pluggable runtime, then wires traffic entry, managed data services, and observability around them. Humans and AI agents are both first-class users: every surface — CLI, API, Console, docs — is built to be driven by a person at a terminal or by an autonomous agent with the same fidelity.

## Highlights

- **One host binary, zero external dependencies.** The control plane (`fleetlyd`) embeds its own SQLite storage (WAL) and self-hosts everything it needs: an OCI image registry (zot), a proxy (traefik), a log store (VictoriaLogs), a metrics store (VictoriaMetrics), and a per-node collector (cAdvisor). A fresh Linux box goes from zero to a publicly reachable HTTPS app with one command.
- **Declarative at the core.** Intake normalizes every source — compose files, image references, specs — into an immutable Revision of a Spec. The engine converges reality toward the declaration, detects drift, and can replay the last good state; rollback is just a Replay of a previous Revision.
- **Deploy-class changes get previews and tickets.** These verbs render a change preview first (spec diff, per-Workload consequences, warnings); interactive callers confirm with y/N, non-interactive calls pass `--yes` or use the two-call `--preview` / `--ticket` flow.
- **A machine contract on every verb.** `--json` everywhere (protojson, snake_case), stable exit codes (`0`/`1`/`2`/`64`), an error envelope carrying errcode + remediation hint + docs link, and self-describing surfaces (`fleetly schema`, `fleetly explain`). `skills/` ships ready-made agent skills.
- **Architecture carried by tests.** A frozen vocabulary ([CONTEXT.md](CONTEXT.md)) — renaming counts as a design flaw — decisions recorded as ADRs, and contract tests that keep layering, wording, and registries honest.

## Features

### Ship and run

- Deploy from an image reference, a source tree (`dockerfile`, `railpack`, `static` builders), an uploaded artifact, or a git push (GitHub App integration for private repositories, or plain webhooks).
- Compose intake speaks official compose-spec syntax plus `x-fleetly-*` extension keys, parsed and schema-checked by compose-go; the normalized result is frozen verbatim into the Revision.
- Deployment strategies per Process: rolling (health-gated, the default) and blue-green.
- Variables synthesize across three levels (Project-shared, source-declared, App-level) and freeze into the Revision; Secrets stay encrypted at rest and never echo back — only fingerprints do.
- Tasks run one-shot or as resident instance pools with owner leases and dedicated network groups; Schedules are time-zone-aware crons that mint Runs.
- exec sessions (`fleetly exec`, `fleetly shell`, web terminal) ride an outbound node relay — joined nodes need zero inbound ports.

### Managed data services

- **Database** — postgres, pgvector, mysql, mongo, redis.
- **PubSub** — nats, kafka, redpanda (kafka-dialect compatibility is expressed in the catalog), with Topic and Stream sub-resources.
- **Bucket Store** — silo, an S3-compatible store with per-project access keys.
- An Engine Catalog self-describes engines, pinned versions, defaults, deprecations, and knob schemas; `databases upgrade` walks major-version upgrades through a resumable wizard.
- Two config lanes per instance: static knobs roll the engine, dynamic knobs take effect live without a restart.
- Backups verify and restore-drill per engine; Platform Backup (restic) protects the control plane itself.

### Traffic and networking

- Routes map `host/path → Process port` with http / h2c / tcp protocols and TLS per route (ACME-managed or uploaded certificates).
- Project networks with optional cross-project peering and `egress:none` isolation (NetworkPolicy-enforced on k3s).

### Cluster

- Pluggable runtimes behind one capability port: Docker Swarm is the default, k3s the second runtime.
- Nodes join via enrollment materials (`fleetly nodes enroll`) and can be drained or cordoned; nodes carry no platform installation — the node view is an observation cache.
- Volumes pin to a node by default; placement constraints are explicit.

### Observe and govern

- Logs: live streams plus persisted search; build logs stream with secret redaction at a single egress point.
- Metrics queried with PromQL; per-App alert rules; notification channels (webhook, telegram).
- Events carry monotonic sequence numbers (SSE follow) alongside an append-only audit ledger.
- A read-time diagnosis engine turns observations into Findings with fault classes that say where to look — a platform fault is never narrated as yours. Platform incidents spell out the blast radius.
- Identity: Teams, Roles, Scopes (`resource:action`, write implies read), revocable Tokens, one-time invitations; Change Freeze windows can halt changes globally or per Team.

### Surfaces

- **Console** — a React SPA embedded in the daemon: deployments, logs, a web terminal, a quickstart wizard, templates, on-demand database browse sessions (pgweb / redis-commander / Adminer / Mongoku), audit and settings.
- **CLI** — `fleetly`, a thin client over the same API; every verb renders for humans and for machines.
- **API & SDK** — gRPC on `:9080` and REST on `:9081` from one proto contract; a typed Go SDK lives in `sdk/go`.

## Architecture at a glance

```
fleetly CLI · Go SDK · AI agents · Console (web)
        │   gRPC :9080 · REST :9081 — one proto contract
        ▼
fleetlyd — single control-plane binary (systemd preferred)
  ├─ embedded SQLite (WAL) + goose migrations
  ├─ engine: single writer, convergence loops as pluggable application modules
  ├─ five self-hosted managed providers:
  │    zot registry :5000 · traefik proxy · VictoriaLogs :9428
  │    VictoriaMetrics :8428 · cAdvisor :8080 (per node)
  ▼  Runtime capability — provider model (Docker Swarm default · k3s)
nodes — zero platform install, joined via enrollment, exec over an outbound relay
```

- Seven capability ports (Runtime, Builder, Registry, Proxy, Logging, Metrics, ObjectStore); each runs exactly one registered provider, chosen by config.
- Clean four-ring layering — `domain` → `application` → `infrastructure` → `interfaces`, plus the `assembly` composition root — with dependency directions enforced by contract tests, not convention.
- Deep dive: [docs/design/2026-09-30-architecture.md](docs/design/2026-09-30-architecture.md).

## Quick start

Linux, amd64 or arm64:

```sh
# 1) One command: OS/arch detection → binaries → Docker check/install → swarm init
#    → fleetlyd under systemd → wait for health
curl -fsSL https://fleetly.sh/install.sh | sudo sh

# 2) Consume the bootstrap token to initialize the first admin
cat /var/lib/fleetly/bootstrap-token
fleetly init --token <token> admin

# 3) Sample app end to end: project/app → image deploy → sslip.io Route
#    → wait for succeeded → prints the URL (zero DNS setup; --tls auto uses ACME)
fleetly quickstart
```

Distribution runs through GitHub Releases; until the first tag is published, that channel fails honestly with a 404 — build from source until then. `FLEETLY_BIN_DIR=... sh install.sh` installs local binaries instead (the same shape the e2e smoke uses); data root, advertise address, and managed-provider endpoints are env-tunable — see the header of [install.sh](install.sh).

Everyday deploys:

```sh
fleetly projects create shop
fleetly apps create --project <project-id> demo
fleetly deploy --app demo --image nginx:1.29               # image, straight in
fleetly deploy --app demo --from-dir . --builder railpack  # source build
fleetly deployments wait --deployment <deployment-id>
fleetly rollback --app demo --yes                          # replay the last succeeded Revision
```

## CLI map

Every group lists its sub-verbs when run bare (`fleetly <group>`):

| Context | Commands |
| --- | --- |
| Identity & access | `init` `login` `whoami` `users` `teams` `roles` `tokens` `audit` |
| Structure | `projects` `apps` `secrets` `configs` `vars` `shared-variables` `volumes` `networks` |
| Delivery | `deploy` `deployments` `revisions` `rollback` `builds` `uploads` `templates` `hooks` `github-apps` `registry` |
| Managed data services | `databases` `pubsubs` `buckets` `engines` `topics` `streams` `bucket` |
| Automation | `tasks` `runs` `schedules` `exec` `shell` |
| Traffic & cluster | `routes` `nodes` |
| Observability | `logs` (`--text` for persisted search) `metrics` `alerts` `rules` `channels` `events` `findings` |
| Platform | `platform` `components` `freeze` `doctor` `status` `schema` `explain` `version` |

Clients connect with `FLEETLY_ADDR` + `FLEETLY_TOKEN` (or a credentials file) — the CLI is meant to run wherever you are, not on the server. The SDK dials the same addresses and additionally understands `grpc://` / `grpcs://` schemes.

## Agent face

The machine contract is identical on every verb, so an agent can drive the whole platform:

- `--json` on every command — protojson, snake_case — pinned by golden tests in both renderings.
- Stable exit codes: `0` success / no change, `1` error, `2` changes found (diff-shaped verbs), `64` usage error.
- One error envelope — errcode, remediation hint, docs link — the same stderr for humans and agents.
- Self-describing platform: `fleetly schema` enumerates resources and fields; `fleetly explain` narrates any resource on the spot.
- `skills/` carries in-repo agent skills (deploy diagnosis, database / PubSub / Bucket Store provisioning, task pools) exercising the same API surface as the CLI; the prose inside them sits under the same contract tests as code.

## Development

```sh
mise install               # pinned toolchain (go/buf/protoc/protoc-gen-go/golangci-lint/node/pnpm)
mise run generate:all      # baseline generation: pinned local plugins → buf → config.pb.go → wire
mise run generate:verify   # regenerate + zero-drift assertion
mise run test              # go test -race across all three modules
mise run lint              # go vet/gofmt + golangci + buf lint + buf breaking (vs origin/main)
mise run dev:server        # start fleetlyd
go run ./cmd/fleetly version
```

Console (React 19 + Vite + Tailwind 4; Node 24 / pnpm pinned): `console:install` · `console:gen` (openapi-typescript from the genproto swagger) · `console:build` (artifacts are committed under `internal/interfaces/console/dist`) · `console:verify`.

End-to-end suites run Docker-in-Docker: `e2e:dind` (single-node smoke) · `e2e:h2c` (managed proxy Route) · `e2e:twonode` (enroll + volume pinning) · `e2e:upgrade` (zero-disturbance upgrade matrix) · `e2e:backup` (restore drills across engines).

Engineering conventions — generated-code discipline, layering, the guard-test culture, commit hygiene — live in [AGENTS.md](AGENTS.md) and bind humans and agents alike.

## Repository layout

```
cmd/fleetlyd, cmd/fleetly    daemon and CLI entrypoints
proto/                       the single contract source (buf lint + breaking gates)
genproto/                    generated protobuf module — never hand-edited
sdk/go                       typed Go client module
internal/domain              domain layer: spec IR, models, capability ports, aggregates
internal/application         engine kernel + application modules, authn, compose intake
internal/infrastructure      SQLite repositories; providers (swarm, k3s, traefik, zot, …)
internal/interfaces          gRPC handlers, admin, embedded console
internal/assembly, config    composition root
console/                     web console sources
skills/                      agent-facing skills
docs/                        design, ADRs, plans, runbooks
e2e/                         DinD end-to-end suites
```

## Documentation

- Vocabulary — the single source, frozen: [CONTEXT.md](CONTEXT.md)
- Architecture, domain model, tech choices: [docs/design/](docs/design/)
- Decision records: [docs/adr/](docs/adr/)
- v1 feature checklist and batches: [docs/plan/2026-09-30-feature-checklist.md](docs/plan/2026-09-30-feature-checklist.md)
- Staging operations logbook: [docs/runbooks/staging-fleetly.md](docs/runbooks/staging-fleetly.md)

## Status

Pre-release — no tag published yet. The v1 feature checklist is essentially complete (single-node heart, agent face + dogfooding, data & observability trust, full Console experience, the k3s second runtime, and the managed data services redesign), and the platform runs for real on a two-node staging cluster carrying dogfooded workloads. Open items are tracked in the checklist: the backup manifest form, the GitHub App staging anchor; GitLab/Gitea webhooks wait for demand.
