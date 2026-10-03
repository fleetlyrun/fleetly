---
name: task-pool
description: Operate fleetly programmatic workloads — one-shot jobs, resident instance pools with owner leases, and timezone-aware cron schedules. Covers create/scale/stop/renew semantics, drain vs force-stop, TTL bounds, revoked-owner handling, and pool diagnosis through tasks/runs/schedules verbs. Use when managing dispatcher-style pools, batch jobs, or scheduled fires.
---

# task-pool

Tasks are programmatic workloads in two forms (ADR-0012): **one-shot** (a
single execution; the task mirrors its run's outcome) and **resident** (a pool
kept at a desired concurrency; each run is a carrier). Schedules fire one-shot
tasks from a frozen template. Owner leases keep resident pools claimed: a
resident task without a renewal heartbeart drains after grace.

Key semantics the commands assume:

- **Replenish**: a resident pool replaces failed/stopped runs up to
  `desired_concurrency`; a one-shot task always wants exactly one run.
- **TTL**: an absolute wall-clock deadline per run (`--ttl-seconds`, max
  86400); expiry stops the run with `stop_reason=ttl_expired`.
- **Stop modes**: `tasks stop` without `--force` stops replenishment and lets
  in-flight runs finish (or run out their TTL); with `--force` they are
  grace-stopped (SIGTERM, then force-kill after the stop grace).
- **Lease**: `tasks renew` advances an absolute deadline; past deadline +
  grace the pool drains (`lease.expired`). A revoked owner token drains the
  task's runs the same way. `--owner-token-id TOKEN_ID` pins an owner other
  than the calling token (e.g. a dedicated pipeline token whose revocation
  drains the pool).
- **DNS**: every task has a stable pool name `task-<id>` (round-robins across
  live runs) and every run a stable `run-<id>` name, inside its task network
  group (`taskgrp-<group>`).
- **Quotas**: per-project caps apply (100 active tasks, 200 desired
  concurrency); a quota rejection names the numbers.
- **Command**: each `--command` occurrence is one argv element of the
  entrypoint override (repeat the flag per element; the default is the image
  entrypoint).

## When to use

- Create a one-shot job or a resident pool, scale it, or drain it.
- A pool is losing runs (`run.failed`, `lease.expired`, `task.draining` in the
  event stream) and needs diagnosis or revival.
- A dispatcher lost its heartbeat and you must decide: renew, drain, or let
  TTL run out.
- Move periodic work onto schedules (timezone-aware cron) instead of cron-in-a-container.

## Command sequences

Create and operate a resident pool:

```bash
fleetly tasks create --project PROJECT_ID --name POOL_NAME --form resident --image IMAGE --concurrency 4 --network-group GROUP --command CMD
fleetly tasks list --project PROJECT_ID --json
fleetly tasks get --task TASK_ID --json
fleetly tasks scale --task TASK_ID --concurrency 8
fleetly tasks stop --task TASK_ID
fleetly tasks stop --task TASK_ID --force
fleetly tasks delete --task TASK_ID
```

One-shot execution with a bounded wait (exit code mirrors the run outcome;
`--command` repeats once per argv element):

```bash
fleetly tasks create --project PROJECT_ID --name JOB_NAME --form one-shot --image IMAGE --command CMD --command ARG --ttl-seconds 600 --wait
fleetly runs wait --run RUN_ID
fleetly runs get --run RUN_ID --json
```

Owner lease keep-alive (resident pools; step is the lease interval, default
30s; missing past deadline + grace = drain):

```bash
fleetly tasks renew --task TASK_ID
```

Inspect and control runs:

```bash
fleetly runs list --task TASK_ID --limit 50 --json
fleetly runs stop --run RUN_ID
```

Schedules (fires a one-shot task per occurrence; manual trigger does not move
the cron rhythm; an overlapping occurrence is skipped with
`schedule.skipped`):

```bash
fleetly schedules create --project PROJECT_ID --name SCHEDULE_NAME --cron CRON_EXPR --timezone TIMEZONE --image IMAGE --command CMD
fleetly schedules list --project PROJECT_ID --json
fleetly schedules trigger --schedule SCHEDULE_ID
fleetly schedules delete --schedule SCHEDULE_ID
```

## Failure triage

| Symptom (event / state / errcode) | Probe next | Action |
| --- | --- | --- |
| `lease.expired` then `task.draining` on a healthy dispatcher | `fleetly tasks get --task TASK_ID --json` (lease deadline) | If the owner is alive, resume `tasks renew` heartbeats and the pool replenishes after revival; otherwise let it drain or delete it. |
| `task.draining` with `owner_revoked` | `fleetly tokens list --json` (is the owner token revoked?) | Intentional revocation drains by design (grace-stop, or run-to-TTL per platform config). Re-point the workload at a fresh token by recreating the task with `--owner-token-id TOKEN_ID`. |
| Runs stuck `pending`, never `running` | `fleetly runs list --task TASK_ID --json`, `fleetly nodes list --json` | Unschedulable placement (no matching node) or image pull failure. Check the image reference and cluster capacity; stop and recreate if the spec is wrong. |
| `run.failed` with non-zero exit, pool size holds | `fleetly runs get --run RUN_ID --json` (exit_code) | The pool already replaced the slot. Fix the workload; run-scoped log retrieval is a known gap (log face is app-scoped) — make the entrypoint print diagnostics. |
| `schedule.skipped` with `reason=overlap` | `fleetly tasks list --project PROJECT_ID --json` (previous fire still live) | Expected under the default skip policy: the previous occurrence's run has not finished. Shorten the run (TTL) or move to the fire policy if parallel fires are wanted. |
| `E_QUOTA_EXCEEDED` on create/scale/spawn | `fleetly tasks list --project PROJECT_ID --json` (count active) | Bring the project under the task/concurrency caps (drain or delete stale tasks) before retrying. |
| `E_RATE_LIMITED` on create | (response carries `Retry-After`) | Back off and retry; the per-token create budget is 120/min by default. Retried identical bodies under an idempotency key do not double-count. |

## Boundaries

- Tasks deploy images only (`--image`); build sources are an app surface.
- Deploy-time init jobs (firstBootJobs) appear here as unnamed one-shot tasks;
  they are owned by the deployment chain — stop them only when abandoning
  that deployment (cancel/supersede already force-stops them).
