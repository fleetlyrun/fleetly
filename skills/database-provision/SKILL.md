---
name: database-provision
description: Provision a managed database (postgres/pgvector/redis) on fleetly and wire an app to it — create the project network precondition, mint the database and its credential secret, inject the connection URL into the app via secret_refs, and verify connectivity through db-<id> DNS on the project network. Use when an app needs a database with zero credential handling.
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
  installed on first boot), `redis`; versions are pinned by the platform.
- **Deletion** keeps the volume and the credential secret (backup-retention
  semantics); IDs are never reused — recreating the same name mints a fresh
  credential.

## When to use

- An app needs postgres/redis with platform-managed credentials.
- You must wire an existing app to an existing database (secret injection).
- Verifying a database is up and app↔database connectivity works.

## Command sequences

1. Every project is born with its `default` network (created in the same
   transaction as the project — projects from fleetly builds since 2026-10-03
   carry it automatically). Only a pre-birth project with zero networks needs
   the explicit form (it fails with `E_ALREADY_EXISTS` when the row exists):

   ```bash
   fleetly networks create --project PROJECT_ID default
   ```

2. Create the database (engine value domain is exactly `postgres`,
   `pgvector`, `redis`; creation is idempotent-key eligible; flags come
   before the NAME positional):

   ```bash
   fleetly databases create --project PROJECT_ID --engine postgres NAME
   fleetly databases create --project PROJECT_ID --engine postgres --idempotency-key KEY NAME
   ```

3. Read back the reachability facts (status, `host` = `db-<id>`, port; the
   credential value is never returned — only the secret name + fingerprint):

   ```bash
   fleetly databases list --project PROJECT_ID --json
   fleetly databases get DATABASE_ID --json
   ```

4. Wire the app. In the compose file, inject the URL by secret reference and
   join the project network (the file lands at
   `/run/secrets/database:NAME` at runtime):

   ```yaml
   services:
     web:
       image: IMAGE
       networks:
         - default
       secrets:
         - database:NAME
   ```

   Then deploy it:

   ```bash
   fleetly deploy --app APP_ID --compose-file compose.yaml --wait
   ```

5. Verify the chain end to end — database `running`, app `succeeded`, and
   the app reading the URL from the injected file:

   ```bash
   fleetly databases get DATABASE_ID --json
   fleetly deployments list --app APP_ID --json
   fleetly logs --app APP_ID --process web --tail 50
   ```

## Failure triage

| Symptom (errcode / state) | Probe next | Action |
| --- | --- | --- |
| `E_CONFLICT` ... `has no networks` on create | `fleetly networks list --project PROJECT_ID --json` | Create a network first (step 1); the check is fail-closed by design — a database would otherwise run unreachable. |
| `E_INVALID_ARGUMENT` on `--engine` | (error text lists the legal engines) | Engine values are exactly `postgres`, `pgvector`, `redis`; custom versions are not accepted yet. |
| Database stuck in `pending` | `fleetly databases get DATABASE_ID --json`, `fleetly events list --limit 20 --json` | First boot runs `initdb`/AOF setup (generous start period); if it never turns `running`, check node capacity (`fleetly nodes list --json`) and events. |
| App cannot reach `db-<id>` | `fleetly deployments list --app APP_ID --json` — does the running revision's compose declare the same network as the database? | Add the project network under the service's `networks:` and redeploy; cross-project reach needs the peer declare/approve flow. |
| App reads no credential file | `fleetly secrets list --project PROJECT_ID --json` (is `database:NAME` present?) | The secret is minted at database creation. If it is missing the database was deleted/recreated — redeploy the app so the injection re-resolves; never write the URL into the spec. |
| Rejected when writing secret `database:...` | (error text names the reserved prefix) | By design: the credential URL is the single truth and user writes would break it. In-place rotation is not exposed yet — recreate the database (new id, new credential) and redeploy the app. |
| Delete then recreate with the same name behaves "like a new database" | `fleetly databases get DATABASE_ID --json` (new id) | Expected: IDs are never reused and a fresh credential is minted; the old volume and secret were retained per deletion semantics. |

## Boundaries

- Backups land with F2 (local object store target is registered already);
  restore/verify surfaces do not exist yet — do not promise them.
- mysql/mongodb templates and version matrices are F2.
- One schema/database per managed instance (`fleetly` database); multiple app
  schemas over one instance is an app-level concern.
