---
name: bucketstore-provision
description: Provision a managed bucket store instance (silo, S3-compatible) on fleetly and wire an app to it — mint the instance and its root access/secret key pair, inject the S3 endpoint and credentials into the app via bucketstore_refs (FLEETLY_BKT_<NAME>_ENDPOINT/ACCESS_KEY/SECRET_KEY) or secret_refs, rotate the secret key with a supervised roll. Use when an app needs an S3-compatible bucket store with zero credential handling.
---

# bucketstore-provision

Managed bucket store instances (ADR-0096) are the third managed data-service
kind: the platform runs the carrier (pinned `pgsty/silo` image serving the
S3 API on `/data`), keeps it attached to
every active network of the project, and seals one project secret per instance:

- **Credential single truth**: secret `bucketstore:NAME` holds the
  **connection material** as a `k=v` dialect string
  (`endpoint=bkt-<id>:9000;access_key=fleetly;secret_key=…`). The pair is
  minted at creation, never returned by any read surface, and rotated
  declaratively (a new secret key rides the carrier env and the instance
  rolls — stop-first, no break window).
- **Reachability**: the instance is reachable at DNS name `bkt-<bucket-id>`
  (lowercase) on port 9000 (S3 API) on the project's networks; plain HTTP
  inside the project network is the platform caliber (same as the zot
  registry).
- **Connection injection**: an app process declaring
  `bucketstore_refs: [NAME]` (compose label `fleetly.bucketstores: NAME`)
  receives `FLEETLY_BKT_<NAME>_ENDPOINT`, `_ACCESS_KEY`, `_SECRET_KEY` at
  Ensure time (NAME upper-snake). The secret key rides a plain env variable —
  this is the explicitly accepted ADR-0014 A.2 scope for this kind.
- **Templates**: engine `silo` with a single version `2026-10` (default, and
  the only registered one); the engine carries **no knobs yet** (v1) and
  **no backup face** — the engine catalog (`fleetly engines list --json`)
  lists `capabilities.backup=false`; durability is an external-sync concern.
  License disclosure: silo is AGPLv3 (the catalog `license_note` carries the
  upstream lineage) — the operating team owns the compliance ruling.
- **Deletion** keeps the volume and the credential secret; IDs are never
  reused.

## When to use

- An app needs a managed S3-compatible bucket store with platform-managed
  credentials.
- You must wire an existing app to an existing instance
  (`bucketstore_refs`).
- You need to rotate the root secret key of an instance.

## Command sequences

1. Every project is born with its `default` network; only a pre-birth project
   with zero networks needs the explicit form:

   ```bash
   fleetly networks create --project PROJECT_NAME default
   ```

2. Create the instance (flags before the NAME positional; an empty
   `--version` means the engine default):

   ```bash
   fleetly buckets create --project PROJECT_NAME --engine silo assets
   fleetly buckets create --project PROJECT_NAME --engine silo --cpu-millis 500 --memory-mb 1024 media
   ```

   The human form prints the id, the credential secret name and the
   in-network S3 endpoint; `--json` returns the row (`host`, `port`,
   `credentials_ref`, `status`).

3. Wait for the carrier to converge and inspect:

   ```bash
   fleetly buckets get --project PROJECT_NAME assets
   fleetly buckets list --project PROJECT_NAME --json
   ```

4. Wire an app. In the app spec declare the reference on the process; the
   platform injects the `FLEETLY_BKT_ASSETS_*` variables at deploy time:

   ```yaml
   processes:
     - name: worker
       bucketstore_refs: [assets]
       networks: [default]
   ```

   Compose dialect: `labels: { fleetly.bucketstores: assets }` on the
   service. To mount the raw k=v connection material as a file instead, use
   `secret_refs: [bucketstore:assets]`.

5. Adjust limits on a running instance (a supervised restart follows; updates
   require confirmation — interactive y/N by default, or the two-call
   `--preview` then `--ticket` flow, or `--yes` for scripts):

   ```bash
   fleetly buckets update --project PROJECT_NAME --memory-mb 512 --preview assets
   fleetly buckets update --project PROJECT_NAME --memory-mb 512 --ticket TICKET assets
   fleetly buckets update --project PROJECT_NAME --cpu-millis 1000 --yes assets
   ```

   The silo engine carries no knobs yet, so `--set` has no registered keys —
   only the resource axes are tunable.

6. Rotate the root secret key (the carrier rolls; apps keep the old key
   until redeployed):

   ```bash
   fleetly buckets rotate-password --project PROJECT_NAME assets
   ```

7. Delete when no longer needed:

   ```bash
   fleetly buckets delete --project PROJECT_NAME assets
   ```

## Buckets (declarative sub-resources)

Buckets inside a bucket store are declared rows converged through the
engine's admin tool; an optional hard quota refuses writes once reached.
The sub-resource group is the singular `bucket` (the plural `buckets` group
manages bucket store instances).

```bash
fleetly buckets bucket create --project PROJECT_NAME --store assets --quota-bytes 1073741824 media
fleetly buckets bucket update --project PROJECT_NAME --store assets --quota-bytes 0 --yes media
fleetly buckets bucket delete --project PROJECT_NAME --store assets --preview media
```

Deleting a bucket force-removes every object in it; the preview discloses
the live object and byte totals before the ticket is minted.

## Failure triage

- `E_INVALID_ARGUMENT engine` — only `silo` is registered in the bucket
  store kind.
- `E_QUOTA_EXCEEDED` — a project holds at most 4 bucket store instances.
- `E_CONFLICT` on update/rotate — the instance is not `running` yet; retry
  after `fleetly buckets get --project PROJECT_NAME assets` shows `running`.
- There is deliberately no `backup` verb: the engine catalog lists
  `capabilities.backup=false` (data redundancy/external sync is the
  durability story for this kind) — do not look for one.

## Boundaries

- No backup face: the kind carries no `backup`/`backups`/`verify` verbs and
  no restore source at create time — the catalog discloses
  `capabilities.backup=false` (ADR-0096 decision 6; silo v1 deferral).
- No browse face: `fleetly buckets` carries no `browse` verb (ADR-0051 keeps
  the registry fail-closed; a lightweight S3 web tool is deferred).
