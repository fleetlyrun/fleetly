---
name: database-provision
description: Provision a managed database (postgres/pgvector/redis/mysql/mongo) on fleetly and wire an app to it — create the project network precondition, mint the database and its credential secret, inject the connection URL into the app via secret_refs, and verify connectivity through db-<id> DNS on the project network. Use when an app needs a database with zero credential handling.
---

# database-provision

Managed databases (ADR-0029) are a third deployment track: the platform runs
the carrier (pinned template image), keeps it attached to every active
network of the project, and seals one project secret per database:

- **Credential single truth**: secret `database:NAME` holds the **full
  connection URL** (e.g. `postgresql://fleetly:<pw>@db-<id>:5432/fleetly`).
  The value is minted at creation, never returned by any read surface, and
  rotated by overwriting the secret (carriers roll automatically).
- **Reachability**: the database is reachable at DNS name `db-<database-id>`
  (lowercase) on the project's networks — an app process must declare the
  same network to connect.
- **Templates**: engines are `postgres`, `pgvector` (vector extension
  installed on first boot), `redis`, `mysql`, `mongo`. Registered versions
  (one default per engine, deprecated ones flagged), the knob schema and the
  capabilities all come from the engine catalog — `fleetly engines list --json`
  for every engine, `fleetly databases config describe ENGINE --json` for one
  engine's knobs. Engine and version are fixed at create time.
- **Knob classes**: `instant` knobs (catalog class `dynamic`) take effect live
  without a restart; `rolling` knobs (class `static`) and resource limits take
  effect through a supervised rolling restart of the carrier.
- **Deletion** keeps the volume and the credential secret (backup-retention
  semantics); IDs are never reused — recreating the same name mints a fresh
  credential.

## When to use

- An app needs a managed database (postgres/pgvector/redis/mysql/mongo) with
  platform-managed credentials.
- You must wire an existing app to an existing database (secret injection).
- You need a specific engine version, engine knobs (`--set`) or resource
  limits at create time, or must tune knobs/limits on a running database.
- Verifying a database is up and app↔database connectivity works.

## Command sequences

1. Every project is born with its `default` network (created in the same
   transaction as the project — projects from fleetly builds since 2026-10-03
   carry it automatically). Only a pre-birth project with zero networks needs
   the explicit form (it fails with `E_ALREADY_EXISTS` when the row exists):

   ```bash
   fleetly networks create --project PROJECT_NAME default
   ```

   Entity references accept id, name, or project/name; names resolve within
   the team/project scope.

2. Create the database (engine and version values come from the engine
   catalog; an empty `--version` means the engine default; creation is
   idempotent-key eligible; flags come before the NAME positional). Read the
   knob schema first when you need knobs — keys, kinds, ranges, defaults and
   the instant/rolling class:

   ```bash
   fleetly engines list --json
   fleetly databases config describe postgres
   fleetly databases config describe --version 17 postgres --json
   fleetly databases create --project PROJECT_NAME --engine postgres NAME
   fleetly databases create --project PROJECT_NAME --engine postgres --version 17 --set max_connections=200 --set work_mem=16mb NAME
   fleetly databases create --project PROJECT_NAME --engine redis --set maxmemory=512mb --memory-mb 1024 NAME
   fleetly databases create --project PROJECT_NAME --engine postgres --idempotency-key KEY NAME
   ```

   Resource limits are optional (`--cpu-millis`, `--memory-mb`; omitted =
   unlimited). A hard memory limit is your call: engine shared and
   per-connection memory can push past it.

3. Read back the reachability facts (status, `host` = `db-<id>`, port; the
   credential value is never returned — only the secret name + fingerprint):

   ```bash
   fleetly databases list --project PROJECT_NAME --json
   fleetly databases get --project PROJECT_NAME DATABASE_NAME --json
   ```

4. Wire the app. In the compose file, inject the URL by secret reference and
   join the project network (the file lands at
   `/run/secrets/database:NAME` at runtime). Secret and network entities are
   referenced via official `external` declarations — the database secret name
   carries a colon, so it rides the `name` override (ADR-0074):

   ```yaml
   services:
     web:
       image: IMAGE
       networks:
         - default
       secrets:
         - dbcred
   networks:
     default:
       external: true
   secrets:
     dbcred:
       external: true
       name: "database:NAME"
   ```

   Then deploy it:

   ```bash
   fleetly deploy --app PROJECT_NAME/APP_NAME --compose-file compose.yaml --yes --wait
   ```

5. Verify the chain end to end — database `running`, app `succeeded`, and
   the app reading the URL from the injected file:

   ```bash
   fleetly databases get --project PROJECT_NAME DATABASE_NAME --json
   fleetly deployments list --app PROJECT_NAME/APP_NAME --json
   fleetly logs --app PROJECT_NAME/APP_NAME --process web --tail 50
   ```

6. Tune knobs or resource limits on a running database. `--set` is a patch
   (knobs you do not name keep their values), `--unset KEY` returns a knob
   to the engine default, and `--cpu-millis`/`--memory-mb` change one axis
   each (0 = unlimited). Updates require confirmation: preview first (diff,
   instant/rolling lanes, rollout `zero_touch` vs `replace`, plus a
   single-use ticket), then send the same flags with `--ticket`; `--yes` is
   the scripted escape hatch:

   ```bash
   fleetly databases update --project PROJECT_NAME --set work_mem=64mb --preview DATABASE_NAME
   fleetly databases update --project PROJECT_NAME --set work_mem=64mb --ticket TICKET DATABASE_NAME
   fleetly databases update --project PROJECT_NAME --set shared_buffers=256mb --unset max_connections --memory-mb 2048 --yes DATABASE_NAME
   ```

   Rollout `replace` restarts the carrier once per update — batch rolling
   changes into one update instead of several.

## Failure triage

| Symptom (errcode / state) | Probe next | Action |
| --- | --- | --- |
| `E_CONFLICT` ... `has no networks` on create | `fleetly networks list --project PROJECT_NAME --json` | Create a network first (step 1); the check is fail-closed by design — a database would otherwise run unreachable. |
| `E_INVALID_ARGUMENT` on `--engine`, `--version` or `--set` | `fleetly engines list --json`, `fleetly databases config describe ENGINE --json` (the error text lists the legal engines, versions or knob keys) | Use catalog values only. A deprecated version refuses brand-new databases (a same-version restore from a backup is still accepted); knob values must match the kind/range/enum shown in the schema. |
| `E_CONFLICT` ... `requires a running database` on update | `fleetly databases get --project PROJECT_NAME DATABASE_NAME --json` (status) | Updates are refused while a roll is in progress or the database is not ready — retry once status is `running`. |
| `E_SETTINGS_APPLY_FAILED` on update | (error text carries the engine's own reply) | The engine refused the live (instant) change and the database row is unchanged; fix the value against `fleetly databases config describe ENGINE` and run the update again. |
| `E_CONFIRMATION_MISMATCH` / `E_CONFIRMATION_STALE` on `update --ticket` | `fleetly databases update --project PROJECT_NAME --set KEY=VALUE --preview DATABASE_NAME` | The ticket is single-use and bound to the previewed desired state: the database changed after the preview, the flags differ, or the ticket expired (600s). Preview again with the same flags and confirm the fresh ticket. |
| Engine or version change rejected on update (`immutable`) | (error text points at the upgrade path) | Engine and version are fixed at create: create a new database on the target version restoring from a backup, then repoint apps. |
| Database stuck in `pending` | `fleetly databases get --project PROJECT_NAME DATABASE_NAME --json`, `fleetly events list --limit 20 --json` | First boot runs `initdb`/AOF setup (generous start period); if it never turns `running`, check node capacity (`fleetly nodes list --json`) and events. |
| App cannot reach `db-<id>` | `fleetly deployments list --app PROJECT_NAME/APP_NAME --json` — does the running revision's compose declare the same network as the database? | Add the project network under the service's `networks:` and redeploy; cross-project reach needs the peer declare/approve flow. |
| App reads no credential file | `fleetly secrets list --project PROJECT_NAME --json` (is `database:NAME` present?) | The secret is minted at database creation. If it is missing the database was deleted/recreated — redeploy the app so the injection re-resolves; never write the URL into the spec. |
| Rejected when writing secret `database:...` | (error text names the reserved prefix) | By design: the credential URL is the single truth and user writes would break it. In-place rotation is not exposed yet — recreate the database (new id, new credential) and redeploy the app. |
| Delete then recreate with the same name behaves "like a new database" | `fleetly databases get --project PROJECT_NAME DATABASE_NAME --json` (new id) | Expected: IDs are never reused and a fresh credential is minted; the old volume and secret were retained per deletion semantics. |

## Boundaries

- Backups land with F2 (local object store target is registered already);
  restore/verify surfaces do not exist yet — do not promise them.
- Engine and version are immutable for a database's lifetime (`databases
  update` changes knobs and resource limits only); moving to a new major is a
  new database on the target version restored from a backup. Cross-node
  volume migration is a runbook operation.
- One schema/database per managed instance (`fleetly` database); multiple app
  schemas over one instance is an app-level concern.
