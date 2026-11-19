---
name: functions-deploy
description: Deploy and operate fleetly Functions — request-driven managed execution units with a single code entry and a URL. Covers create-with-first-deploy, the three independent planes (revision/content, capacity, exposure), rollback-as-replay, function logs, and the exposure security model. Use when deploying a function image, flipping private/public reachability, scaling a function, or diagnosing a function that never becomes healthy.
---

# functions-deploy

Functions are the fourth first-class execution product (ADR-0077): write one
code entry, the platform gives it an address, scales it, and keeps it
observable. A function is a **managed aggregate** (Database precedent — no App
row, no topology): the row is the desired state, a convergence loop drives the
carrier. The call semantics (auth, payload, idempotency) belong to the
function itself; the platform semantics (URL, scaling, exposure, observability)
belong to fleetly.

Key semantics the commands assume:

- **Create is the first deploy**: there is no empty function row; `create`
  freezes revision r1 and the convergence loop materializes the carrier.
- **Three independent planes**: content (revisions), capacity
  (`replicas` within `[min, max]`), and exposure (`private` | `public`).
  Scaling or flipping exposure never redeploys — a scale is a row write and a
  route republish, zero carrier churn. Changing code (deploy) never touches
  capacity or exposure.
- **Revision model**: every deploy freezes an immutable revision
  (`current_revision_seq` in responses). `rollback` re-tracks to the previous
  frozen row — **no new revision is created** (replay reuses the row). The
  retention window keeps the most recent 100 revisions per function; outside
  the window a rollback target is gone for good.
- **URLs**: every function has a permanent in-network DNS name `fn-<id>`
  (`http://fn-<id>:<port>`) reachable by project network members — private by
  default, and it survives exposure flips. Public exposure adds a managed
  synthetic route at the chosen host (no Route row to clean up); the control
  plane never proxies invocations.
- **Probe**: the platform health-checks the declared port with a TCP probe by
  default (it does not assume `/healthz` — call semantics belong to the
  function). Images without a shell (distroless/scratch) must declare an exec
  probe via the API healthcheck field, or the default probe cannot run.
- **Quotas**: per-project caps (50 active functions, 64 replicas summed); a
  quota rejection names the numbers.
- **v1 boundary**: image-only source, `min_replicas >= 1` (scale-to-zero is
  Phase 2), no per-invocation metrics on private functions (the platform is
  not on the private data path).

## When to use

- Deploy a container image as a request-driven function and get a URL.
- Scale a function's replica count without redeploying.
- Flip a function between private (project-network-only) and public
  (internet-reachable managed route) — and understand what changed.
- Replace the code (full replace: image/command/env/secrets/port) or roll
  back to the previous frozen revision.
- A function is stuck `pending`, shows `degraded`, or its URL does not answer.

## Command sequences

Create (first deploy included) and watch it converge, then read the URLs:

```bash
fleetly functions create --project PROJECT_NAME --image IMAGE --port 8080 --min-replicas 1 --max-replicas 4 --wait FUNCTION_NAME
fleetly functions list --project PROJECT_NAME --json
fleetly functions get --project PROJECT_NAME --json FUNCTION_NAME
fleetly functions url --project PROJECT_NAME FUNCTION_NAME
```

Entity references accept id, name, or project/name; names resolve within the
team/project scope.

Replace the code (full replace — omitting a field resets it, capacity and
exposure stay untouched), then roll back by replaying the previous frozen row:

```bash
fleetly functions deploy --project PROJECT_NAME --image IMAGE2 --port 8080 --wait FUNCTION_NAME
fleetly functions revisions --project PROJECT_NAME FUNCTION_NAME
fleetly functions rollback --project PROJECT_NAME FUNCTION_NAME
fleetly functions rollback --project PROJECT_NAME --to-revision 2 FUNCTION_NAME
```

Capacity plane (row write, zero redeploy; the window is `[min, max]` from
create):

```bash
fleetly functions scale --project PROJECT_NAME FUNCTION_NAME 3
```

Exposure plane (route face republishes immediately, carriers untouched).
Going public requires a host — derive one or pass it explicitly; going private
clears the host and the public URL disappears while `fn-<id>` stays:

```bash
fleetly functions exposure --project PROJECT_NAME --host HOSTNAME FUNCTION_NAME public
fleetly functions invoke --project PROJECT_NAME --data @payload.json FUNCTION_NAME
fleetly functions exposure --project PROJECT_NAME FUNCTION_NAME private
```

Logs and removal (revisions are cleared with the aggregate; the `fn-<id>`
name dies with the row; a second delete is a clean 404):

```bash
fleetly functions logs --project PROJECT_NAME --follow FUNCTION_NAME
fleetly functions delete --project PROJECT_NAME FUNCTION_NAME
```

## Failure triage

| Symptom (event / state / errcode) | Probe next | Action |
| --- | --- | --- |
| Stuck `pending` after create/deploy | `fleetly functions get --project PROJECT_NAME --json FUNCTION_NAME` (status), `fleetly functions logs --project PROJECT_NAME FUNCTION_NAME` | Carrier is not passing its health probe. Most common: the declared `--port` is not what the image listens on, or a shell-less image cannot run the default TCP probe — fix the port or declare an exec probe via the API healthcheck field and deploy again. |
| `degraded` after being `running` | `fleetly functions logs --project PROJECT_NAME --follow FUNCTION_NAME` | The carrier is unhealthy (probe failures / restart loop). Read the function's own stdout; the platform restarts it — fix the code or the probe expectation and deploy. |
| In-network callers cannot reach `fn-<id>` | `fleetly functions get --project PROJECT_NAME --json FUNCTION_NAME` (private_url), then call from inside the project network | The name is reachable only to project network members. Verify the caller shares an active project network (`fleetly networks list --project PROJECT_NAME --json`); cross-project needs a network peer. |
| Public URL 404s after `exposure public` | `fleetly functions url --project PROJECT_NAME FUNCTION_NAME` (public row), re-run with the host you configured | Route existence derives from the function row and republishes immediately; a 404 means the host you dialed is not the configured one, the flip was to `private` (redline by design), or the backend is unresolved (function not healthy). |
| `E_QUOTA_EXCEEDED` on create/scale | `fleetly functions list --project PROJECT_NAME --json` (count rows and replica sum) | Bring the project under the caps (50 functions / 64 summed replicas) — delete unused functions or scale windows down before retrying. |
| Scale rejected `E_INVALID_ARGUMENT` outside window | `fleetly functions get --project PROJECT_NAME --json FUNCTION_NAME` (min/max) | The window was fixed at create. Recreate the function with a wider `--min-replicas`/`--max-replicas`, or scale within the window. |
| Rollback `E_NOT_FOUND` with retention suggestion | `fleetly functions revisions --project PROJECT_NAME FUNCTION_NAME` (window contents) | The target fell outside the 100-revision retention window (ADR-0101). Redeploy the desired spec instead of rolling back. |
| `E_ALREADY_EXISTS` on create | `fleetly functions list --project PROJECT_NAME --json` | An active function with that name exists in the project. Reuse it, or delete first — the name frees as soon as the row is tombstoned. |

## Boundaries

- Functions deploy images only (`--image`); build sources are an app surface
  (Phase 2 adds upload+build for functions).
- No platform-side invocation proxying: `functions invoke` is a client-side
  direct HTTP call and refuses private functions with an honest error — call
  private functions from inside the project network at `fn-<id>`.
- Private is not authenticated transport: project network membership is the
  reachability boundary; authentication belongs to the function.
- No invocation metrics on private functions (the platform is not on that
  data path); public request counting arrives with Phase 2.
