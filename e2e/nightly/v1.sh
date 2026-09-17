#!/bin/sh
# e2e/nightly V1 — health failure must not switch traffic.
# Runs INSIDE a docker:29.8.1-dind prepared by infra-b.sh.
#
# Derived from spike/b/scripts/in-b1-v1b2.sh (Spike B B1/B2; findings in
# spike/b/README.md section 1/2). Asserts:
#   V1-ASSERT-1 UPDATE_PAUSED              failing update ends in paused
#   V1-ASSERT-2 NEW_TASK_FAILED            new (v2bad) task failed
#   V1-ASSERT-3 OLD_TASK_STILL_RUNNING     old v1 task kept serving
#   V1-ASSERT-4 EXTERNAL_PROBE_ZERO_FAILURE  200ms VIP prober, zero fails,
#                                          all responses v1
# plus the B2 piggyback from the same spike script:
#   B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY (same-content replay)
#   B2-ASSERT-2 NO_EXTRA_TASK_CREATED
set -u
. /tmp/lib.sh

NET=nightly-b-net
PSFMT='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'

docker service rm app-v1 >/dev/null 2>&1 || true
docker rm -f lbw-v1 >/dev/null 2>&1 || true
sleep 2

nl "=== V1.1 create app-v1: healthcheck + start-first + failure-action=pause ==="
docker service create --name app-v1 --network "$NET" \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
    --update-failure-action pause --update-order start-first --update-monitor 5s \
    --update-parallelism 1 --update-delay 0s \
    spike-b/app:v1 >/dev/null || fatal "create app-v1"
# task Running implies healthy: the executor only reports Running after the
# healthcheck passes (source-level gate; spike B1 relied on the same).
wait_run app-v1 40 || {
    docker service ps app-v1 --no-trunc
    fatal "app-v1 baseline never healthy"
}
CID=$(docker ps -q --filter name=app-v1 | head -1)
nl "baseline container health: $(docker inspect --format '{{.State.Health.Status}}' "$CID")"

nl "=== V1.2 external prober starts (60s window across failing update + replay) ==="
docker run -d --name lbw-v1 --network "$NET" --entrypoint /probe spike-b/app:v1 \
    lbwatch -svc app-v1 -port 8080 -rate-ms 200 -dns-ms 200 -dur 60s >/dev/null || fatal "prober"
sleep 3

nl "=== V1.3 failing update v1 -> v2bad (HEALTH_MODE=fail: /health always 503) ==="
OLD_TASK_ID=$(docker service ps app-v1 --format '{{.ID}}' | head -1)
nl "old task id=$OLD_TASK_ID"
docker service update --image spike-b/app:v2bad app-v1 >/tmp/v1-update.out 2>&1
nl "update exit=$?"
cat /tmp/v1-update.out

sleep 4
nl "=== V1.4 post-failure state ==="
docker service ps app-v1 --format "$PSFMT" --no-trunc
docker service inspect --format 'UpdateStatus={{json .UpdateStatus}}' app-v1
NEW_TASK_ID=$(docker service ps app-v1 --format '{{.ID}} {{.CurrentState}}' | grep -i failed | head -1 | awk '{print $1}')
nl "failed new task id=${NEW_TASK_ID:-none}"

UPDSTATE=$(docker service inspect --format '{{if .UpdateStatus}}{{.UpdateStatus.State}}{{end}}' app-v1)
[ "$UPDSTATE" = "paused" ]
assert "V1-ASSERT-1 UPDATE_PAUSED" $? "got=$UPDSTATE"

[ -n "$NEW_TASK_ID" ]
assert "V1-ASSERT-2 NEW_TASK_FAILED" $? "no Failed row found"

NOWLINE=$(docker service ps app-v1 --format "$PSFMT" | grep "^$OLD_TASK_ID ")
echo "$NOWLINE" | grep -q Running
assert "V1-ASSERT-3 OLD_TASK_STILL_RUNNING" $? "line=[$NOWLINE]"

nl "=== V1.5 B2: replay same-content spec (image back to v1), no --force ==="
docker service update --image spike-b/app:v1 app-v1 >/tmp/v1-replay.out 2>&1
nl "replay exit=$?"
cat /tmp/v1-replay.out
wait_img app-v1 spike-b/app:v1 40 || {
    docker service ps app-v1 --no-trunc
    fatal "app-v1 not healthy after replay"
}
RUNNING_IDS=$(docker service ps app-v1 --format '{{.ID}} {{.CurrentState}}' | grep ' Running' | awk '{print $1}' | tr '\n' ' ')
nl "running task ids after replay=[$RUNNING_IDS]"
echo "$RUNNING_IDS" | grep -q "$OLD_TASK_ID"
assert "B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY" $?
NRUNNING=$(echo "$RUNNING_IDS" | wc -w)
[ "$NRUNNING" = "1" ]
assert "B2-ASSERT-2 NO_EXTRA_TASK_CREATED" $? "n=$NRUNNING"

nl "=== V1.6 prober verdict (zero external failures, all v1) ==="
wait_container_gone lbw-v1 90 || nl "WARN prober still running"
docker logs lbw-v1 2>&1 | tail -6
PROB_FAILS=$(docker logs lbw-v1 2>&1 | grep '"ev":"req"' | grep -c '"fail"')
PROB_VERS=$(docker logs lbw-v1 2>&1 | grep -o '"ver":"[a-z0-9]*"' | sort | uniq -c | tr '\n' ' ')
nl "prober fails=$PROB_FAILS version-histogram: $PROB_VERS"
[ "$PROB_FAILS" = "0" ]
assert "V1-ASSERT-4 EXTERNAL_PROBE_ZERO_FAILURE" $?
docker rm -f lbw-v1 >/dev/null 2>&1 || true

docker service rm app-v1 >/dev/null 2>&1 || true
finish
