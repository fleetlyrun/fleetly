---
name: pubsub-provision
description: Provision a managed pubsub instance (nats with JetStream) on fleetly and wire an app to it — mint the instance and its credential secret, inject the connection URL into the app via pubsub_refs (FLEETLY_PUB_<NAME>_URL/HOST/PORT/USER/PASSWORD) or secret_refs, take per-stream backups and restore them into a fresh instance. Use when an app needs a message bus with zero credential handling.
---

# pubsub-provision

Managed pubsub instances (ADR-0096) are the second managed data-service
family after databases: the platform runs the carrier (pinned `nats:<ver>-alpine`
image with JetStream on `/data`), keeps it attached to every active network of
the project, and seals one project secret per instance:

- **Credential single truth**: secret `pubsub:NAME` holds the **connection
  material** in the nats URL dialect (`nats://fleetly:<pw>@pub-<id>:4222`).
  The value is minted at creation, never returned by any read surface, and
  rotated declaratively (the server config is rewritten and the carrier rolls).
- **Reachability**: the instance is reachable at DNS name `pub-<pubsub-id>`
  (lowercase) on port 4222 on the project's networks.
- **Connection injection**: an app process declaring `pubsub_refs: [NAME]`
  (compose label `fleetly.pubsubs: NAME`) receives
  `FLEETLY_PUB_<NAME>_URL`, `_HOST`, `_PORT`, `_USER`, `_PASSWORD` at Ensure
  time (NAME upper-snake). Credentials ride a plain env variable — this is
  the explicitly accepted ADR-0014 A.2 scope for this family.
- **Templates**: engine `nats` with versions `2.10` and `2.11` (default
  `2.11`); the knob schema (`jetstream_max_file`, `jetstream_max_memory`, both
  `size`/rolling) and capabilities come from the engine catalog
  (`fleetly engines list --json`). Engine and version are fixed at create time.
- **Backups** are per-stream object sets: every JetStream stream (KV buckets
  included) becomes one member; the manifest is written last. Memory-storage
  streams cannot be snapshotted — a backup that meets one is refused and
  names the streams.
- **Deletion** keeps the volume and the credential secret; IDs are never
  reused.

## When to use

- An app needs a managed nats/JetStream instance with platform-managed
  credentials.
- You must wire an existing app to an existing instance (`pubsub_refs`).
- You need a backup of every stream, or a fresh instance restored from one.

## Command sequences

1. Every project is born with its `default` network; only a pre-birth project
   with zero networks needs the explicit form:

   ```bash
   fleetly networks create --project PROJECT_NAME default
   ```

2. Create the instance (flags before the NAME positional; an empty
   `--version` means the engine default):

   ```bash
   fleetly pubsubs create --project PROJECT_NAME --engine nats events
   fleetly pubsubs create --project PROJECT_NAME --engine nats --version 2.11 --set jetstream_max_file=4gb --memory-mb 512 events
   ```

   The human form prints the id, the credential secret name and the
   in-network endpoint; `--json` returns the row (`host`, `port`,
   `credentials_ref`, `status`).

3. Wait for the carrier to converge and inspect:

   ```bash
   fleetly pubsubs get --project PROJECT_NAME events
   fleetly pubsubs list --project PROJECT_NAME --json
   ```

4. Wire an app. In the app spec declare the reference on the process; the
   platform injects the `FLEETLY_PUB_EVENTS_*` variables at deploy time:

   ```yaml
   processes:
     - name: worker
       pubsub_refs: [events]
       networks: [default]
   ```

   Compose dialect: `labels: { fleetly.pubsubs: events }` on the service.
   To mount the raw URL as a file instead, use `secret_refs: [pubsub:events]`.

5. Tune rolling knobs or limits on a running instance (a supervised restart
   follows):

   ```bash
   fleetly pubsubs update --project PROJECT_NAME --set jetstream_max_memory=256mb events
   fleetly pubsubs update --project PROJECT_NAME --unset jetstream_max_memory --cpu-millis 1000 events
   ```

6. Back up and restore:

   ```bash
   fleetly pubsubs backup --project PROJECT_NAME events
   fleetly pubsubs backups --project PROJECT_NAME events
   fleetly pubsubs verify --pubsub events --project PROJECT_NAME BACKUP_ID
   fleetly pubsubs create --project PROJECT_NAME --engine nats --restore-from-backup BACKUP_ID events-restored
   ```

   Restore is stream-by-stream into the new instance once it is running
   (empty-stream precondition: a fresh instance). Completion lands in
   `database.backup_succeeded` / `database.restored` events on the `pubsub`
   aggregate.

7. Rotate the credential (apps keep the old value until redeployed):

   ```bash
   fleetly pubsubs rotate-password --project PROJECT_NAME events
   ```

8. Delete when no longer needed:

   ```bash
   fleetly pubsubs delete --project PROJECT_NAME events
   ```

## Failure triage

- `E_INVALID_ARGUMENT engine` — only `nats` is registered in the pubsub
  family (kafka/redpanda land with a later batch).
- `E_QUOTA_EXCEEDED` — a project holds at most 8 pubsub instances.
- `E_CONFLICT` on update/rotate — the instance is not `running` yet; retry
  after `fleetly pubsubs get --project PROJECT_NAME events` shows `running`.
- A backup failing with `memory-storage streams cannot be snapshotted` —
  recreate those streams with file storage (JetStream default) and retry.

## Boundaries

- No browse face: nats has no lightweight web tool, so `fleetly pubsubs`
  carries no `browse` verb (ADR-0051 keeps the registry fail-closed).
- Backups are per-stream object sets; a backup of an instance that holds a
  memory-storage stream is refused (not partially taken).
- Rotation is declarative: the server config is rewritten and the carrier
  rolls; apps keep the previous value until redeployed.
