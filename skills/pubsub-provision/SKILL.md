---
name: pubsub-provision
description: Provision a managed pubsub instance (nats with JetStream) on fleetly and wire an app to it — mint the instance and its credential secret, inject the connection URL into the app via pubsub_refs (FLEETLY_PUB_<NAME>_URL/HOST/PORT/USER/PASSWORD) or secret_refs, take per-stream backups and restore them into a fresh instance. Use when an app needs a message bus with zero credential handling.
---

# pubsub-provision

Managed pubsub instances (ADR-0096) are the second managed data-service
kind after databases: the platform runs the carrier (pinned `nats:<ver>-alpine`
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
  the explicitly accepted ADR-0014 A.2 scope for this kind.
- **Templates**: engines `nats` (versions `2.10`/`2.11`, default `2.11`) and
  the kafka-protocol dialect family `kafka` (versions `4.3`/`4.4`, default
  `4.4`) and `redpanda` (versions `26.1`/`26.2`, default `26.2`; BSL 1.1
  license note in the catalog); the knob schema and capabilities come from the
  engine catalog (`fleetly engines list --json`). Engine and version are fixed
  at create time. Kafka-family engines speak SASL SCRAM-SHA-256 and inject
  `FLEETLY_PUB_<NAME>_BOOTSTRAP`/`_MECHANISM` instead of `_URL`; they carry
  `backup: false` (no data backup face yet — the TriggerBackup verb is
  refused).
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
   follows; updates require confirmation — interactive y/N by default, or the
   two-call `--preview` then `--ticket` flow, or `--yes` for scripts):

   ```bash
   fleetly pubsubs update --project PROJECT_NAME --set jetstream_max_memory=256mb --preview events
   fleetly pubsubs update --project PROJECT_NAME --set jetstream_max_memory=256mb --ticket TICKET events
   fleetly pubsubs update --project PROJECT_NAME --unset jetstream_max_memory --cpu-millis 1000 --yes events
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

## Topics and streams (declarative sub-resources)

The engine decides the face: kafka and redpanda instances carry **topics**,
nats instances carry **streams**. Rows are the desired state; the platform
converges them through the engine's own admin tool (status goes
`pending` → `ready`, or `error` with the reason on the row).

```bash
fleetly pubsubs topics create --project PROJECT_NAME --pubsub kbus --partitions 3 --retention-ms 3600000 orders
fleetly pubsubs topics update --project PROJECT_NAME --pubsub kbus --partitions 6 --yes orders
fleetly pubsubs streams create --project PROJECT_NAME --pubsub events --subjects "orders.*" --storage file ORDERS
fleetly pubsubs streams update --project PROJECT_NAME --pubsub events --subjects "orders.*,bills.*" --subjects-set --yes ORDERS
```

Deleting a topic or stream destroys its data; read the deletion disclosure
first, then confirm with the ticket it prints:

```bash
fleetly pubsubs topics delete --project PROJECT_NAME --pubsub kbus --preview orders
fleetly pubsubs topics delete --project PROJECT_NAME --pubsub kbus --ticket TICKET orders
```

Topic partitions only increase (the replication factor is fixed at 1 on the
single broker); stream storage and retention are immutable after create.

## Failure triage

- `E_INVALID_ARGUMENT engine` — the pubsub kind registers `nats`, `kafka`
  and `redpanda`; anything else is refused (engine names are implementation
  names — the dialect field in the catalog shows family compatibility).
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
