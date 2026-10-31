---
name: deploy-diagnose
description: Diagnose fleetly deployments that are stuck, failed, or serving errors — read the deployment state machine, event stream, app logs and build logs, then decide between re-deploy, rollback, or a spec fix. Use when a deploy did not reach succeeded, an app regression appears after a release, or workload.stopped fires on a previously healthy app.
---

# deploy-diagnose

Diagnose a deployment through the platform's own observability surfaces. The
diagnosis face answers "why is it bad right now" (read-time findings with
fault class, evidence and a prefilled fix); the deployment row is the primary
fact (state + error text); the event stream is the causal chain; logs and
builds are the evidence underneath.

## When to use

- `fleetly deploy --app APP_ID --image IMAGE --yes --wait` exited non-zero (the
  deployment reached a terminal state other than `succeeded`).
- `fleetly deployments list --app APP_ID` shows a row sitting in `preparing` / `building` /
  `releasing` / `observing` well past its windows (L1 ready wait default
  120s, observe window default 60s).
- A previously healthy app starts erroring (`workload.stopped` or
  `workload.drift_detected` in the event stream), or you want the platform's
  own explanation first: `fleetly apps diagnose APP_ID` names the failure
  class, evidence and a prefilled fix before you read raw streams.
- You are triaging a whole project ("which apps need attention?"):
  `fleetly findings list --project PROJECT_ID` aggregates the diagnosis
  across all of the project's apps.
- You need evidence before choosing between cancel, re-deploy,
  `fleetly rollback --app APP_ID --yes`, or fixing the spec.

## Command sequences

1. Ask the platform's diagnosis face first — it is the cheap first probe and
   often names the cause outright (crash loop, OOM, health check, image pull,
   stopped-as-declared) with evidence (state, exit code, restart count, recent
   event) and a structured fix (verb + prefilled params, or a console
   deep-link when the fix is a declaration change). `unreadable_faces` in the
   response is the honesty face — facts the diagnosis cannot read yet, not
   facts that are fine:

   ```bash
   fleetly apps diagnose --json APP_ID
   fleetly findings list --project PROJECT_ID --json
   fleetly findings list --project PROJECT_ID --after-app-seq 12 --limit 50 --json
   ```

   `findings list` is the project-wide aggregation (apps with findings only —
   healthy apps advance the cursor but occupy no row); paginate with
   `--after-app-seq N` (cursor = the last app's `app_seq` of the previous
   page) and `--limit N` (default 50, max 200) until a short page. Severity is
   two-level (`warning` = broken now, `advice` = explained steady state such
   as declared-stopped); `fault_class` routes the fix: `app` → fix the app
   and deploy again, `declaration` → fix the spec, `platform` → managed
   components (operator territory), `external` → upstream (registry, DNS).
   Findings whose fix carries a verb (`restart`/`scale`) map one-to-one onto
   the recovery verbs in step 9 — do not improvise a different one.

2. Read the deployment rows — state, `error` text, `generation`, and (while
   pre-deploy jobs are in flight) `pre_deploy_task_id`:

   ```bash
   fleetly deployments list --app APP_ID --json
   ```

3. Read the causal chain. Event names to look for: `deployment.*` (state
   transitions, `deployment.pre_deploy_job`), `build.*`, `run.*` / `task.*`
   (deploy-time jobs), `workload.drift_detected`, `workload.converged`,
   `workload.converge_failed`, `workload.stopped`:

   ```bash
   fleetly events list --limit 50 --json
   ```

   Bounded replay of the retention window, then keep following live while you
   reproduce the problem:

   ```bash
   fleetly events follow --replay --json
   ```

   Drift semantics (converge-by-default): on an idle app (no deployment in
   flight) the engine reconverges drifted or missing carriers to the last
   succeeded revision on its own — a `workload.drift_detected` is usually
   followed by `workload.converged` within a scan interval. A drift that
   persists, or a `workload.converge_failed`, means convergence is failing or
   cooling down (see the triage table). To hand-edit carriers on purpose, set
   a change freeze first — otherwise the engine will converge your fix away.
   Replica-count drift is declarative: express it with
   `fleetly apps scale APP_ID PROCESS 2` (scale-only overlay on the effective
   spec, zero carrier replacement) instead of scaling carriers by hand — the
   engine converges hand-scaled replicas back to the declared count.

4. Probe workload reality under the app (per process; live tail + recent
   buffer only — persisted search lands with N2):

   ```bash
   fleetly logs --app APP_ID --process PROCESS --tail 100
   ```

5. Build-source deployments (git / upload): check build rows, then stream the
   build log of the failing build:

   ```bash
   fleetly builds list --app APP_ID --json
   fleetly builds logs --build BUILD_ID
   ```

6. See exactly what changed between the running revision and the target
   (seqs come from `fleetly revisions list --app APP_ID`; numeric flags take real numbers):

   ```bash
   fleetly revisions list --app APP_ID --json
   fleetly revisions diff --app APP_ID --from 3 --to 4
   ```

7. Read the frozen spec of one revision in full (what that deployment actually
   shipped — the row anchor carries the digest and the material source kind),
   or render the workload view: the projected spec replay (what the platform
   would issue now — default is the current effective revision) and the live
   in-service observation (what the cluster is actually running, with the real
   per-carrier generation; compare the two to spot drift):

   ```bash
   fleetly revisions get --app APP_ID --seq 3
   fleetly apps workloads APP_ID
   fleetly apps workloads --seq 3 APP_ID
   fleetly apps workloads --live APP_ID
   ```

8. Git-hook apps have split spec ownership (ADR-0072): the repo owns the
   material half (source, build — pushes refresh it and keep console-declared
   runtime settings), spec_file deploys are runtime overlays, and material
   sources (image/compose/upload) are refused with E_APP_HOOK_OWNED unless you
   pass `--override-hook` (one-shot takeover; the regular path is removing the
   hook). Env lives in app variables — the top merge layer, re-synthesized on
   every freeze, so a push never wipes it; changing one requires a redeploy to
   take effect:

   ```bash
   fleetly apps vars list --app APP_ID
   fleetly apps vars set --app APP_ID --value production TIER
   fleetly apps vars unset --app APP_ID TIER
   fleetly deploy --app APP_ID --image IMAGE --override-hook --yes
   ```

9. Recovery verbs — pick per the triage table, do not stack them blindly.
   Cancel clears a queued or in-flight deployment first (terminal states
   refuse with E_NOT_CANCELLABLE); rollback and re-deploy submit the
   replacement. Restart has two honest faces (ADR-0083): the default is the
   declarative face — it bumps `restart_seq` on every process of the latest
   frozen revision and rides the normal deploy pipeline (audited lineage,
   pre-deploy jobs/migrations do not re-run, content-equivalence rolls the
   carriers); use it when the intent is "re-run these processes as a desired
   state change". `--runtime` is the escape hatch (ADR-0069): it
   force-reschedules one process's carriers in place — no revision, no
   deployment, brief per-replica downtime; use it to un-wedge a single
   process whose spec is healthy. Hook-owned apps restart declaratively too
   (the material half stays frozen to the hook; a plain material deploy on a
   hook-owned app fails with E_APP_HOOK_OWNED — `--runtime` is that escape).
   Replica-count changes go through `apps scale` (scale-only overlay on the
   effective spec; zero carrier replacement, no confirmation ticket — the
   Restart/Scale exemption family). To attach to a deployment or build
   already in flight (webhook-triggered chains submit outside your session),
   use the standalone wait verbs — they stream state transitions and exit
   non-zero unless the row ends `succeeded`:

   ```bash
   fleetly deployments cancel DEPLOYMENT_ID
   fleetly deployments wait --deployment DEPLOYMENT_ID
   fleetly builds wait --build BUILD_ID
   fleetly rollback --app APP_ID --yes --wait
   fleetly apps restart --app APP_ID
   fleetly apps restart --app APP_ID --runtime --process PROCESS
   fleetly apps scale --wait APP_ID PROCESS 2
   fleetly deploy --app APP_ID --image IMAGE --yes --wait
   fleetly deploy --app APP_ID --from-dir DIR --yes --wait
   fleetly deploy --app APP_ID --from-dir DIR --builder railpack --railpack-version RAILPACK_VERSION --yes --wait
   fleetly deploy --app APP_ID --from-dir DIR --builder static --output-dir OUTPUT_DIR --yes --wait
   ```

   Confirmation forms (B1): deploy and rollback are in the mandatory
   confirmation set. Non-interactive sessions (agents, CI) pass `--yes` as
   shown above — it skips the preview confirmation and is audited as such.
   To inspect a change before committing to it, run the two-call flow
   instead: `fleetly deploy --app APP_ID --image IMAGE --preview` prints the
   spec diff, per-workload rollout badges and warnings plus a single-use
   ticket (600s TTL), then `fleetly deploy --app APP_ID --image IMAGE --ticket TICKET`
   confirms exactly that payload; a stale or mismatched ticket refuses with
   E_CONFIRMATION_STALE / E_CONFIRMATION_MISMATCH and the fix is always to
   re-run `--preview`. `apps delete` follows the same shape with a teardown
   preview (cascade + kept lists). Rollback currently has no preview face —
   `--yes` is its scripted channel.

## Failure triage

| Symptom (state / event / text) | Probe next | Action |
| --- | --- | --- |
| App erroring / unhealthy, cause not yet named (any of the rows below can start here) | `fleetly apps diagnose --json APP_ID` first (read-time findings: severity, fault_class, evidence, prefilled fix); project-wide sweep with `fleetly findings list --project PROJECT_ID --json` | Let the finding route you: fault_class `app` → the fix-the-app rows below; `declaration` → spec fix + deploy (fix verb absent, console deep-link); `platform` → managed components, wait for the operator; `external` → upstream (image registry, DNS). A fix verb (`restart`/`scale`) is the recovery action itself — run it prefilled (step 9). `F_DIAGNOSIS_UNEXPLAINED` means evidence exists but no rule attributes it yet: fall through to events/logs. Empty findings + `unreadable_faces` means healthy within what the diagnosis can read — not a guarantee about the rest. |
| `failed` + `health gate L1 timed out waiting for workloads to become ready` | `fleetly logs --app APP_ID --process PROCESS --tail 100` (crash loop? app listening on another port?) | Fix the app or the declared probe/port, then deploy again. If the previous revision was healthy and traffic matters, `fleetly rollback --app APP_ID --yes --wait` first. |
| `failed` + `pre-deploy job "NAME" ...` (exit code, `exceeded its ttl`, or `did not reach a terminal state within its wait window`) | `fleetly tasks list --project PROJECT_ID --json` to find the job task (unnamed, one-shot), then `fleetly runs get --run RUN_ID --json` for exit code and stop reason | The job's exit code and the deployment error text carry the diagnosis. Job stdout is not yet retrievable through `fleetly logs --app APP_ID` (app-scoped log face; run-scoped logs are a known gap) — make the migration command print its own diagnostics. Fix the job (or its ttl), then deploy again. Rollback never re-runs pre-deploy jobs. |
| `failed` + `build BUILD_ID: failed (...)` | `fleetly builds logs --build BUILD_ID` | Fix the build (Dockerfile / source), deploy again. Builds are revision-scoped: the same revision rebuilds from cache. |
| `failed` + `railpack: prepare rejected the source` | `fleetly builds logs --build BUILD_ID` (the railpack prepare log names the detection failure) | Fix what railpack detected (lockfile / start command / config), then deploy again. A static site with no runnable app belongs to `--builder static`, not railpack. |
| `failed` + `railpack: pinned version mismatch` or `railpack binary ... is not runnable` | The error text names the platform's pinned version and the `--railpack-version` form | Redeploy with the exact pinned version; a missing/mismatched binary is an operator task on the control-plane node (install.sh pins it). |
| `failed` + `cross-project network reference ... is not approved` | `fleetly networks peers --project PROJECT_ID --json` | Have the receiving project approve the peer declaration, or drop the reference from the spec. |
| `releasing` / `observing` for minutes, no failure | `fleetly events follow --replay --json` (are `workload` observations arriving at all?) | If observations stalled, suspect the runtime/provider; if L1 legitimately waits, the window (default 120s) will fail the deployment on its own — do not preempt it. |
| `workload.stopped` on a `succeeded` app (steady state) | `fleetly apps diagnose --json APP_ID` (expect the `F_WORKLOAD_STOPPED_EXPLAINED` advice finding with a `scale` fix), `fleetly logs --app APP_ID --process PROCESS --tail 100`, `fleetly events list --limit 20 --json` | The watchdog only reports (spec matches, so convergence stands down — the runtime restarts tasks on its own). If the process exits are a crash loop, fix and deploy; declared-stopped is advice, not a failure — to run it, `fleetly apps scale APP_ID PROCESS 1` (the finding's prefilled fix). A stopped row with no deployment in flight reconverges with `fleetly apps restart --app APP_ID --runtime --process PROCESS` (in-place force-reschedule for one wedged process) or `fleetly apps restart --app APP_ID` (declarative: bumps restart_seq through the deploy pipeline; a redeploy of unchanged content does not roll carriers). |
| `workload.drift_detected` on an idle app (no deployment in flight) | `fleetly events list --limit 20 --json` (did `workload.converged` follow?), `fleetly doctor` | Converge-by-default restores the succeeded baseline within a scan interval — confirm convergence instead of acting. If drift persists without `workload.converged`, suspect a change freeze (convergence suspends; `fleetly freeze list --json`) or a cooling window (next row). |
| `workload.converge_failed` / doctor shows a convergence cooldown | `fleetly events list --limit 50 --json` (the failure tails), `fleetly apps workloads --live APP_ID` | The engine retries after the cooldown window, or sooner on a new drift signature. Fix the underlying cause (carrier stuck, image unresolvable, runtime unhealthy) rather than retrying manually. For a deliberate manual hot fix, set a change freeze first (`fleetly freeze set --reason manual-hot-fix`), edit, then either write the change into the spec or lift the freeze. |
| `E_QUEUE_FULL` on submit | `fleetly deployments list --app APP_ID --json` (queued rows pile-up) | Wait for the in-flight deployment to drain; do not retry-loop the submit. |
| `E_CHANGE_FROZEN` on any change verb | `fleetly freeze list --json` | A change freeze is active for the team/resource. Lift it (`fleetly freeze lift FREEZE_ID`) only if the change is intended. |
| `git clone` failure mentioning `Authentication failed` (build never starts) | `fleetly github-apps list --project PROJECT_ID --json` (is there a row whose owner matches the repo's `owner/` path?) | Private repository with no access material: for github.com hosts configure a GitHub App — `fleetly github-apps put --project PROJECT_ID --app-id 182345 --installation-id 51790921 --private-key-file key.pem` (owner resolved automatically; `--global` for a platform-wide row); for other hosts put a `git:<host>` project secret via `fleetly secrets put --project PROJECT_ID --value '{"username":"ci","secret":"s3cret"}' git:HOST` (flags before the NAME positional; value is a JSON pair). If a row exists but the failure persists, `fleetly github-apps repos --id ID` re-verifies the installation live (suspended or uninstalled rows fail precise with `E_GITHUB_UPSTREAM`). |

## Boundaries

- `fleetly logs --app APP_ID` reads the recent buffer + live tail only; there is no
  persisted search before N2, and no run-scoped log retrieval yet.
- The diagnosis face (B2a) explains steady-state failures ("why is it bad
  now"); deploy-time failures stay explained by the deployment row's `error`
  text and build logs — do not expect a Finding for a failed deployment. Its
  first rule set covers crash loop, OOM, health check, image pull and
  declared-stopped; anything else falls to `F_DIAGNOSIS_UNEXPLAINED` with
  evidence attached.
- Rollback replays the last successful revision's carriers; it never re-runs
  pre-deploy jobs (they already ran for that revision).
