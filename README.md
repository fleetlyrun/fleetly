# fleetly

[![ci](https://github.com/fleetlyrun/fleetly/actions/workflows/pr.yml/badge.svg)](https://github.com/fleetlyrun/fleetly/actions/workflows/pr.yml) [![nightly](https://github.com/fleetlyrun/fleetly/actions/workflows/nightly.yml/badge.svg)](https://github.com/fleetlyrun/fleetly/actions/workflows/nightly.yml)

[English](README.md) | [简体中文](README_ZH.md)

> Dokku's footprint, Railway's API, AI-Agent-first operations.

fleetly is an ultra-lightweight open-source PaaS for small teams. Deploy `compose.yaml` apps to a cluster of 1–10 servers with zero-downtime releases, snapshot-based rollback, drift detection, and an API surface designed for both humans and AI agents — no Kubernetes required.

**Status: early development.** Design is frozen and reviewed; the T0 foundation (repo, CI, proto contract chain, error-code registry, dind E2E skeleton) has landed. v0.1 is not released yet — see the [roadmap](#roadmap). Formerly known as *edgesets* and *edgefleet*.

## Install

One command on a clean Linux VPS (amd64/arm64, root) installs a running platform — engine gate (Docker ≥ 29.8.1, iptables backend), implicit `docker swarm init`, systemd autostart, and an install report with the port-exposure surface:

```sh
curl -fsSL https://fleetly.dev/install.sh | sudo sh -            # latest stable
curl -fsSL https://fleetly.dev/install.sh | sudo sh - --version v0.1.0
sudo sh install.sh --bin-dir ./dist                              # offline / dev form
```

The first start writes the bootstrap admin token **once** to `<data-root>/bootstrap-token` (0600, never logged; remove the file after the first successful login). Uninstall keeps application data (`--purge` removes it). Upgrading the control plane is one command with a pre-upgrade snapshot and automatic rollback (`sudo sh upgrade.sh --version vX.Y.Z`); Engine/host upgrades are a separate cold-backup procedure — see [`docs/runbooks/upgrade.md`](docs/runbooks/upgrade.md). Forms, gate list, port table, and dind verification: [`deploy/README.md`](deploy/README.md). (Release artifacts land with the release pipeline — until then the offline `--bin-dir` form is the working path.)

## Why fleetly

- **Built for teams without ops.** ≤5 developers, no dedicated ops, 1–3 servers to start, a maintenance budget of 1–2 hours *per week*. Everything automatable (TLS, backups, upgrades, inspection) is automated and verifiable.
- **Compose is the only app model.** No proprietary spec. A controlled subset of the Compose Specification with a minimal `fleetly.*` label convention; anything outside the subset is rejected with a structured error, never silently ignored.
- **Docker Swarm as the substrate.** Membership, scheduling, and health-gated updates come from the engine itself — no self-built distributed core. Single-node v0.1 is already a (transparent) single-node Swarm, so adding the second server is a `docker swarm join`, not a re-architecture.
- **API-first, proto as the contract.** gRPC + REST (grpc-gateway) derived from a single protobuf source; CLI, Console, and (in v0.2) MCP are all consumers of the same contract. No feature ships without an API.
- **Trust is the floor.** Atomic self-upgrades (pre-pulled image + snapshot + auto-rollback), backups with read-back verification, error messages as a product (stable error codes + context + fix suggestions) — for humans and AI agents alike.

## Feature map (planned)

| Area | Behavior | Version |
|---|---|---|
| Deploy | git push / webhook / API → Railpack or Dockerfile build → zero-downtime rollout → observation window | v0.1 |
| Release safety | Swarm `failure-action=pause` + platform snapshot replay (last 5 verified revisions); never Swarm-native rollback | v0.1 |
| Routing / TLS | Per-node Traefik with routes and certs pushed by the control plane; central ACME (HTTP-01), multi-SAN domain lists | v0.1 |
| State | SQLite control-plane state, three-layer model (authoritative / observed cache / live read) | v0.1 |
| Drift detection | Desired-state hash vs. reality; detection on by default, auto-converge opt-in per app | v0.1 |
| Multi-node | `docker swarm join`, image registry (zot), stateful pinning, honest HA boundaries | v0.2 |
| AI agents | MCP server with a curated toolset (≤30 tools), scoped tokens, two-step destructive confirmation | v0.2 |
| Data services | Managed Postgres/Redis templates + per-engine backup adapters + connection-string injection | v0.2 |
| Extras | Cron (Swarm jobs), metrics (VictoriaMetrics), Web terminal (exec relay, D19) | v0.2+ |

## Honest boundaries

We say what we don't do: no cross-node shared storage (volumes are local; stateful services are pinned to a node and never auto-migrated — moving data goes through backup/restore); **two nodes ≠ full HA** (you get stateless process HA, not management-plane or stateful HA — the installer says so explicitly); no CI engine (your Git host runs CI; fleetly gates deploys on webhook status); no Kubernetes backend (k3s is reserved as an exit plan, not a feature).

## Architecture

```
CLI (fleetly) / Console / MCP (v0.2) / gRPC / REST / git push (SSH) / Webhook
                 │
   fleetlyd — single Go binary on the Swarm manager
     API: gRPC + grpc-gateway (proto = single contract source)
     release state machine · reconciler · build pipeline (Railpack/BuildKit)
     state: SQLite (WAL) · secrets: envelope encryption (age) · TLS: central ACME
                 │  Docker API (local socket manages the whole cluster)
   Docker Engine (Swarm mode) — services · overlay networks · scheduling
   Traefik (global, per node) — routes & certs pushed by the control plane
```

Foundation stack: [lynx](https://github.com/lynx-go/lynx) + [google/wire](https://github.com/google/wire) (D20), buf + [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway/v2) (D21). Domain code stays free of framework types — the core/adapter boundary is a hard rule (D13).

## Repository layout

```
cmd/fleetlyd/   control-plane daemon
cmd/fleetly/    CLI
proto/            API contracts (fleetly.{server,client,console,shared}.v1)
genproto/         generated code + OpenAPI (openapiv2) — committed
sdk/go/           Go SDK (gRPC client)
internal/         errcode / eventcode registries, app error envelope
e2e/              dind smoke harness (reused by CI and Spikes)
docs/             design docs, research reports, implementation plan
console/          Console frontend (React + Vite + shadcn/ui, lands with T2.21)
deploy/           installer & systemd units (lands with T2.1)
```

## CLI

The CLI talks to the daemon over gRPC only — no direct database or Docker access. Every verb that touches the platform takes `--addr` (default `127.0.0.1:8421`, env `FLEETLY_ADDR`) and `--token` (env `FLEETLY_TOKEN`); the bootstrap admin token is written **once** to `<data-root>/bootstrap-token` on first start (never logged; delete after first login), further tokens come from `fleetly tokens create`. Every verb supports `--json`; exit codes are `0` success/no changes, `1` error, `2` changes detected (`plan`/`diff` only), `64` usage error (unknown verb, bad flags/arguments — `EX_USAGE`). Flags must precede positional arguments (Go std `flag` semantics). Unary RPCs carry a default 30s deadline; Ctrl-C on streaming verbs (`logs follow`, `events watch`) and wait verbs (`deploy`, `build`, `rollback`) exits cleanly with code 0.

```bash
fleetlyd &                                  # control plane (gRPC :8421, HTTP :8420, git SSH :8424)
export FLEETLY_ADDR=127.0.0.1:8421
export FLEETLY_TOKEN=<bootstrap admin token>

fleetly validate compose.yaml               # controlled-subset validation (local)
fleetly plan compose.yaml                   # diff vs latest revision via API; exit 2 = changes
fleetly deploy compose.yaml                 # enqueue and wait for the terminal state
fleetly apps list && fleetly deployments list my-api
fleetly logs follow --service web my-api    # live stream (--json for JSONL)
fleetly env set my-api KEY value            # pending until next deploy
fleetly rollback my-api                     # snapshot replay (last 5 revisions)
fleetly drift show my-api                   # desired vs. live
fleetly tokens create --scopes deploy --note CI   # plaintext shown once
```

### Deploying via `git push` (SSH)

The daemon runs an embedded SSH git endpoint (default `127.0.0.1:8424` — loopback by default; expose it on a VPS by setting `git.addr` and firewalling accordingly). Register your public key, then push to the app's bare repo; `compose.yaml`/`compose.yml` at the repo root is the deploy unit, and pushes to the app's configured branch (default `main`) trigger a deployment.

```bash
fleetly git keys add --note laptop ~/.ssh/id_ed25519.pub   # admin scope; fingerprints at rest
git remote add fleetly ssh://git@127.0.0.1:8424/my-api.git
git push fleetly main                                      # → build → zero-downtime rollout
fleetly git keys list && fleetly git keys rm <id>
```

### Deploying via webhook (GitHub / Gitea)

Configure the per-app signing secret (never echoed again), point the webhook at the control plane (`POST /v1/apps/<app>/webhooks/github` or `/gitea`, JSON body), and the daemon verifies the HMAC-SHA256 signature, rejects replayed delivery IDs (15-min TTL), dedups by commit, fetches the source, and enqueues the deploy.

```bash
fleetly apps webhook set-secret my-api <secret>            # ≥16 chars; admin scope
fleetly apps webhook set-source --branch main --auth-kind none \
    my-api https://github.com/acme/web.git                  # or https_token / ssh_key
fleetly apps webhook show my-api                           # no sensitive projection
```

See `fleetly help <verb>` for the full flag list.

### Console (web UI)

A React SPA (Vite + Tailwind + shadcn/ui) that consumes only the authenticated REST API. Build it and point the daemon at the output to get it served at `/ui/` (static assets are unauthenticated; all data still goes through the Bearer-authenticated `/v1` API):

```bash
cd console && pnpm install && pnpm build      # → console/dist
fleetlyd -c config.yaml                       # with console.static_dir: "./console/dist"
# open http://127.0.0.1:8420/ui/  → paste an API token to sign in
```

The console covers app list/detail (derived-state badges), deploys with live terminal-state tracking, rollback, streaming logs (NDJSON follow + history search), env management (pending changes grouped as "takes effect on next deploy"), domains with verify, system health, and the platform event stream. See [console/README.md](console/README.md).

## Documentation

All docs live in [`docs/`](docs/README.md) (Chinese, design-first workflow):

- [Architecture](docs/design/2026-09-17-architecture.md) — positioning, stack, 21 key decisions (D1–D21), roadmap
- Design specials: [release semantics](docs/design/2026-09-17-release-semantics.md) · [stateful placement](docs/design/2026-09-17-stateful-placement.md) · [control-plane state model](docs/design/2026-09-17-state-model.md) · [delivery pipeline](docs/design/2026-09-17-delivery-pipeline.md)
- Research: [competitive landscape](docs/research/2026-09-17-competitive-landscape.md) · [Swarm substrate assessment](docs/research/2026-09-17-swarm-substrate-assessment.md)
- Implementation: [task breakdown](docs/plan/2026-09-17-task-breakdown.md) · [v0.1 scope freeze](docs/plan/2026-09-17-v0.1-scope-freeze.md)

## Roadmap

| Stage | Scope | Status |
|---|---|---|
| T0 foundation | repo, CI gates, proto contract chain, error/event registries, dind E2E skeleton | ✅ done |
| Spike A/B/C | build, release+routing, substrate risk validation (V1–V7) | next |
| v0.1 | single-node GA of the 8-item scope (deploy loop, TLS, rollback, trust drill) | in development |
| v0.2 | multi-node, MCP, S3 endpoints, managed databases, cron, metrics, Web terminal | planned |
| v0.3 | preview environments, template catalog, RBAC, Compose subset expansion, tunnel access | planned |

## Development

Prerequisites: Go ≥ 1.26.6 (`GOTOOLCHAIN=auto` works), buf CLI, Docker (for e2e).

```bash
go build ./...
go test ./... ./sdk/go/... -race
buf lint && buf generate          # generated artifacts are committed; must not drift
golangci-lint run
```

Smoke E2E (runs fleetlyd inside `docker:29.8.1-dind`): see [`e2e/README.md`](e2e/README.md).

Contribution discipline: this project is design-first — behavior changes start as doc changes (review rounds), then land as vertical slices tracked in the task breakdown. Error codes and events are append-only registries. Remediation acceptance must include a write-back check of related docs/comments: grep the changed keyword across `docs/`, `deploy/`, and code comments to confirm runbooks, scripts, and help text no longer describe the pre-fix behavior (drift is a defect, not a style issue).

## License

Apache-2.0 — see [LICENSE](LICENSE). The default distribution contains no AGPL/DSAL components.
