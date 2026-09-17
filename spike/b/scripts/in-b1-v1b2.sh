#!/bin/sh
# Spike B experiment 1: V1 (health failure does not switch traffic) + B2
# (zero-cost restore via same-content spec replay, task id comparison) and
# B2b (which field changes make the task dirty). Runs INSIDE the dind.
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
psfmt='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'

wait_healthy() { # svc timeout_s -> task Running implies healthy (executor gates on health)
  svc=$1; i=0
  while [ $i -lt $2 ]; do
    st=$(docker service ps "$svc" --format '{{.CurrentState}}' 2>/dev/null | head -1)
    case "$st" in Running*) return 0;; esac
    i=$((i+1)); sleep 1
  done
  return 1
}
# NOTE: a paused service keeps crash-looping its failed task via the restart
# policy, so `service ps` head -1 can be a transient (Ready/Starting) row of
# the failing version. wait_img polls for a Running task of a SPECIFIC image.
wait_img() { # svc image timeout_s
  svc=$1; img=$2; i=0
  while [ $i -lt $3 ]; do
    if docker service ps "$svc" --format '{{.Image}} {{.CurrentState}}' 2>/dev/null | grep -q "^$img Running"; then return 0; fi
    i=$((i+1)); sleep 1
  done
  return 1
}

docker service rm app-v1 app-dirty >/dev/null 2>&1 || true
docker rm -f lbw-v1 >/dev/null 2>&1 || true
sleep 2

say "=== B1.1 create app-v1: healthcheck + start-first + failure-action=pause ==="
docker service create --name app-v1 --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  --update-parallelism 1 --update-delay 0s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-v1 30 || { say "FATAL app-v1 never healthy"; docker service ps app-v1 --no-trunc; exit 1; }
CID=$(docker ps -q --filter name=app-v1)
docker inspect --format 'container-health={{.State.Health.Status}}' $CID

say "=== B1.2 external prober starts (60s window across failing update + replay) ==="
docker run -d --name lbw-v1 --network $NET --entrypoint /probe spike-b/app:v1 \
  lbwatch -svc app-v1 -port 8080 -rate-ms 200 -dns-ms 200 -dur 60s >/dev/null
sleep 3

say "=== B1.3 ps BEFORE failing update ==="
docker service ps app-v1 --format "$psfmt" --no-trunc | tee /tmp/b1.ps.before
OLD_TASK_ID=$(docker service ps app-v1 --format '{{.ID}}' | head -1)
say "old task id=$OLD_TASK_ID"

say "=== B1.4 update to v2bad (bakes HEALTH_MODE=fail: /health always 503) ==="
docker service update --image spike-b/app:v2bad app-v1 > /tmp/b1.update.out 2>&1
say "update exit=$?"
cat /tmp/b1.update.out

sleep 4
say "=== B1.5 post-failure state ==="
docker service ps app-v1 --format "$psfmt" --no-trunc | tee /tmp/b1.ps.paused
docker service inspect --format 'UpdateStatus={{json .UpdateStatus}}' app-v1
NEW_TASK_ID=$(docker service ps app-v1 --format '{{.ID}} {{.CurrentState}}' | grep -i failed | head -1 | awk '{print $1}')
say "failed new task id=${NEW_TASK_ID:-none}"

say "=== B1.6 V1 assertions ==="
UPDSTATE=$(docker service inspect --format '{{if .UpdateStatus}}{{.UpdateStatus.State}}{{end}}' app-v1)
if [ "$UPDSTATE" = "paused" ]; then say "V1-ASSERT-1 UPDATE_PAUSED: PASS"; else say "V1-ASSERT-1 UPDATE_PAUSED: FAIL got=$UPDSTATE"; fi
if [ -n "$NEW_TASK_ID" ]; then say "V1-ASSERT-2 NEW_TASK_FAILED: PASS ($NEW_TASK_ID)"; else say "V1-ASSERT-2 NEW_TASK_FAILED: FAIL"; fi
NOWLINE=$(docker service ps app-v1 --format "$psfmt" | grep "^$OLD_TASK_ID ")
if echo "$NOWLINE" | grep -q Running; then say "V1-ASSERT-3 OLD_TASK_STILL_RUNNING: PASS"; else say "V1-ASSERT-3 OLD_TASK_STILL_RUNNING: FAIL line=[$NOWLINE]"; fi

say "=== B1.7 B2: replay same-content spec (image back to v1), no --force ==="
docker service update --image spike-b/app:v1 app-v1 > /tmp/b1.replay.out 2>&1
say "replay exit=$?"
cat /tmp/b1.replay.out
wait_img app-v1 spike-b/app:v1 30 || { say "FATAL app-v1 not healthy after replay"; docker service ps app-v1 --no-trunc; exit 1; }
docker service ps app-v1 --format "$psfmt" --no-trunc | tee /tmp/b1.ps.restored
RUNNING_IDS=$(docker service ps app-v1 --format '{{.ID}} {{.CurrentState}}' | grep ' Running' | awk '{print $1}' | tr '\n' ' ')
say "running task ids after replay=[$RUNNING_IDS]"
if echo "$RUNNING_IDS" | grep -q "$OLD_TASK_ID"; then say "B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY: PASS"; else say "B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY: FAIL"; fi
NRUNNING=$(echo "$RUNNING_IDS" | wc -w)
if [ "$NRUNNING" = "1" ]; then say "B2-ASSERT-2 NO_EXTRA_TASK_CREATED: PASS"; else say "B2-ASSERT-2 NO_EXTRA_TASK_CREATED: FAIL n=$NRUNNING"; fi
docker service inspect --format 'UpdateStatus-after-replay={{json .UpdateStatus}}' app-v1

say "=== B1.8 prober verdict (V1-ASSERT-4: zero external failures, all v1) ==="
i=0
while [ $i -lt 75 ]; do
  R=$(docker inspect -f '{{.State.Running}}' lbw-v1 2>/dev/null)
  [ "$R" = "false" ] && break
  i=$((i+1)); sleep 1
done
docker logs lbw-v1 2>&1 | tail -8
FAILS=$(docker logs lbw-v1 2>&1 | grep '"ev":"req"' | grep -c '"fail"' || true)
VERS=$(docker logs lbw-v1 2>&1 | grep -o '"ver":"[a-z0-9]*"' | sort | uniq -c | tr '\n' ' ')
say "prober fails=$FAILS version-histogram: $VERS"
if [ "$FAILS" = "0" ]; then say "V1-ASSERT-4 EXTERNAL_PROBE_ZERO_FAILURE: PASS"; else say "V1-ASSERT-4 EXTERNAL_PROBE_ZERO_FAILURE: FAIL"; fi
docker rm -f lbw-v1 >/dev/null 2>&1 || true

say "=== B1.9 B2b: field-dirty matrix on app-dirty (no healthcheck, fast) ==="
docker service create --name app-dirty --network $NET \
  --label spike=b2 --container-label cl=1 \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-dirty 20 || { say "FATAL app-dirty baseline"; exit 1; }
BASE=$(docker service ps app-dirty --format '{{.ID}}' | head -1)
say "baseline task id=$BASE"
b2b_step() { # name docker-update-args...
  nm=$1; shift
  docker service update "$@" app-dirty >/dev/null 2>&1
  rc=$?
  sleep 3
  CUR=$(docker service ps app-dirty --format '{{.ID}}' | head -1)
  if [ "$CUR" = "$BASE" ]; then r=UNCHANGED; else r=REPLACED; BASE=$CUR; fi
  say "B2b[$nm] rc=$rc -> $r"
}
b2b_step replay-identical-image  --image spike-b/app:v1
b2b_step svc-label-add           --label-add foo=bar
b2b_step container-label-add     --container-label-add cl2=x
b2b_step update-config-parallel  --update-parallelism 2
b2b_step env-add                 --env-add E1=1
b2b_step restart-policy          --restart-condition on-failure
b2b_step force-no-content-change --force
docker service ps app-dirty --format "$psfmt" --no-trunc | head -5
docker service rm app-dirty >/dev/null 2>&1 || true
say "B1-EXPERIMENT-DONE"
