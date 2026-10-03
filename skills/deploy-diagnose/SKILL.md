---
name: deploy-diagnose
description: Diagnose fleetly deployments that are stuck, failed, or serving errors — read the deployment state machine, event stream, app logs and build logs, then decide between re-deploy, rollback, or a spec fix. Use when a deploy did not reach succeeded, an app regression appears after a release, or workload.stopped fires on a previously healthy app.
---

# deploy-diagnose

Diagnose a deployment through the platform's own observability surfaces. The
deployment row is the primary fact (state + error text); the event stream is
the causal chain; logs and builds are the evidence underneath.

## When to use

- `fleetly deploy --app APP_ID --image IMAGE --wait` exited non-zero (the
  deployment reached a terminal state other than `succeeded`).
- `fleetly deployments list --app APP_ID` shows a row sitting in `preparing` / `building` /
  `releasing` / `observing` well past its windows (L1 ready wait default
  120s, observe window default 60s).
- A previously healthy app starts erroring (`workload.stopped` or
  `workload.drift_detected` in the event stream).
- You need evidence before choosing between cancel, re-deploy,
  `fleetly rollback --app APP_ID`, or fixing the spec.

## Command sequences

1. Read the deployment rows — state, `error` text, `generation`, and (while
   first-boot jobs are in flight) `first_boot_task_id`:

   ```bash
   fleetly deployments list --app APP_ID --json
   ```

2. Read the causal chain. Event names to look for: `deployment.*` (state
   transitions, `deployment.first_boot_job`), `build.*`, `run.*` / `task.*`
   (deploy-time jobs), `workload.drift_detected`, `workload.stopped`:

   ```bash
   fleetly events list --limit 50 --json
   ```

   Bounded replay of the retention window, then keep following live while you
   reproduce the problem:

   ```bash
   fleetly events follow --replay --json
   ```

3. Probe workload reality under the app (per process; live tail + recent
   buffer only — persisted search lands with N2):

   ```bash
   fleetly logs --app APP_ID --process PROCESS --tail 100
   ```

4. Build-source deployments (git / upload): check build rows, then stream the
   build log of the failing build:

   ```bash
   fleetly builds list --app APP_ID --json
   fleetly builds logs --build BUILD_ID
   ```

5. See exactly what changed between the running revision and the target
   (seqs come from `fleetly revisions list --app APP_ID`; numeric flags take real numbers):

   ```bash
   fleetly revisions list --app APP_ID --json
   fleetly revisions diff --app APP_ID --from 3 --to 4
   ```

6. Recovery verbs — pick per the triage table, do not stack them blindly.
   Cancel clears a queued or in-flight deployment first (terminal states
   refuse with E_NOT_CANCELLABLE); rollback and re-deploy submit the
   replacement. To attach to a deployment or build already in flight
   (webhook-triggered chains submit outside your session), use the standalone
   wait verbs — they stream state transitions and exit non-zero unless the
   row ends `succeeded`:

   ```bash
   fleetly deployments cancel DEPLOYMENT_ID
   fleetly deployments wait --deployment DEPLOYMENT_ID
   fleetly builds wait --build BUILD_ID
   fleetly rollback --app APP_ID --wait
   fleetly deploy --app APP_ID --image IMAGE --wait
   fleetly deploy --app APP_ID --from-dir DIR --wait
   fleetly deploy --app APP_ID --from-dir DIR --builder railpack --railpack-version RAILPACK_VERSION --wait
   fleetly deploy --app APP_ID --from-dir DIR --builder static --output-dir OUTPUT_DIR --wait
   ```

## Failure triage

| Symptom (state / event / text) | Probe next | Action |
| --- | --- | --- |
| `failed` + `health gate L1 timed out waiting for workloads to become ready` | `fleetly logs --app APP_ID --process PROCESS --tail 100` (crash loop? app listening on another port?) | Fix the app or the declared probe/port, then deploy again. If the previous revision was healthy and traffic matters, `fleetly rollback --app APP_ID --wait` first. |
| `failed` + `first boot job "NAME" ...` (exit code, `exceeded its ttl`, or `did not reach a terminal state within its wait window`) | `fleetly tasks list --project PROJECT_ID --json` to find the job task (unnamed, one-shot), then `fleetly runs get --run RUN_ID --json` for exit code and stop reason | The job's exit code and the deployment error text carry the diagnosis. Job stdout is not yet retrievable through `fleetly logs --app APP_ID` (app-scoped log face; run-scoped logs are a known gap) — make the migration command print its own diagnostics. Fix the job (or its ttl), then deploy again. Rollback never re-runs first-boot jobs. |
| `failed` + `build BUILD_ID: failed (...)` | `fleetly builds logs --build BUILD_ID` | Fix the build (Dockerfile / source), deploy again. Builds are revision-scoped: the same revision rebuilds from cache. |
| `failed` + `railpack: prepare rejected the source` | `fleetly builds logs --build BUILD_ID` (the railpack prepare log names the detection failure) | Fix what railpack detected (lockfile / start command / config), then deploy again. A static site with no runnable app belongs to `--builder static`, not railpack. |
| `failed` + `railpack: pinned version mismatch` or `railpack binary ... is not runnable` | The error text names the platform's pinned version and the `--railpack-version` form | Redeploy with the exact pinned version; a missing/mismatched binary is an operator task on the control-plane node (install.sh pins it). |
| `failed` + `cross-project network reference ... is not approved` | `fleetly networks peers --project PROJECT_ID --json` | Have the receiving project approve the peer declaration, or drop the reference from the spec. |
| `releasing` / `observing` for minutes, no failure | `fleetly events follow --replay --json` (are `workload` observations arriving at all?) | If observations stalled, suspect the runtime/provider; if L1 legitimately waits, the window (default 120s) will fail the deployment on its own — do not preempt it. |
| `workload.stopped` on a `succeeded` app (steady state) | `fleetly logs --app APP_ID --process PROCESS --tail 100`, `fleetly events list --limit 20 --json` | The watchdog only reports. If the process exits are a crash loop, fix and deploy; a stopped row with no deployment in flight may need a no-op redeploy to reconverge. |
| `E_QUEUE_FULL` on submit | `fleetly deployments list --app APP_ID --json` (queued rows pile-up) | Wait for the in-flight deployment to drain; do not retry-loop the submit. |
| `E_CHANGE_FROZEN` on any change verb | `fleetly freeze list --json` | A change freeze is active for the team/resource. Lift it (`fleetly freeze lift FREEZE_ID`) only if the change is intended. |

## Boundaries

- `fleetly logs --app APP_ID` reads the recent buffer + live tail only; there is no
  persisted search before N2, and no run-scoped log retrieval yet.
- Rollback replays the last successful revision's carriers; it never re-runs
  first-boot jobs (they already ran for that revision).
