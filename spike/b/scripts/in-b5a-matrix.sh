#!/bin/sh
# Spike B experiment 5a: failure matrix part 1 - crash-on-start and
# health-never-pass, each x (start-first, stop-first). Records: task states,
# service UpdateStatus, old task fate, and the restore (same-content replay)
# behavior + duration. Raw material for the release state machine's error
# classification (E_TASK_START_FAILED / E_HEALTH_TIMEOUT).
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
psfmt='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'

wait_healthy() { # svc timeout_s
  svc=$1; i=0
  while [ $i -lt $2 ]; do
    st=$(docker service ps "$svc" --format '{{.CurrentState}}' 2>/dev/null | head -1)
    case "$st" in Running*) return 0;; esac
    i=$((i+1)); sleep 1
  done
  return 1
}

run_combo() { # name order failimage
  svc="app-fm-$1"
  docker service rm "$svc" >/dev/null 2>&1 || true
  sleep 2
  say "===== combo $1: order=$2 failimage=$3 ====="
  docker service create --name "$svc" --network $NET \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 2 --health-start-period 2s \
    --update-failure-action pause --update-order "$2" --update-monitor 5s \
    spike-b/app:v1 >/dev/null || { say "FATAL create $svc"; return 1; }
  wait_healthy "$svc" 30 || { say "FATAL $svc baseline"; return 1; }
  OLD=$(docker service ps "$svc" --format '{{.ID}}' | head -1)
  say "old task=$OLD"
  docker service update --image "spike-b/app:$3" "$svc" > /tmp/fm-$1.out 2>&1
  say "failing-update exit=$?"
  head -3 /tmp/fm-$1.out
  US=""; i=0
  while [ $i -lt 40 ]; do
    US=$(docker service inspect --format '{{if .UpdateStatus}}{{.UpdateStatus.State}}{{end}}' "$svc")
    [ -n "$US" ] && break
    i=$((i+1)); sleep 1
  done
  say "UpdateStatus=$US (after ${i}s)"
  docker service inspect --format 'UpdateStatus-msg={{if .UpdateStatus}}{{.UpdateStatus.Message}}{{end}}' "$svc"
  docker service ps "$svc" --format "$psfmt" --no-trunc
  say "old task line now: [$(docker service ps "$svc" --format "$psfmt" | grep "^$OLD ")]"
  say "prober verdict during failure window: $(docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 client -url http://$svc:8080/ -count 3 -every-ms 300 -dur 0s 2>&1 | grep '"ev":"client-summary"')"
  R0=$(date +%s)
  docker service update --image spike-b/app:v1 "$svc" > /tmp/fm-$1-restore.out 2>&1
  say "restore exit=$?"
  wait_healthy "$svc" 45 || say "$svc restore FAILED (task never healthy)"
  R1=$(date +%s)
  say "restore seconds=$((R1-R0))"
  docker service ps "$svc" --format "$psfmt"
  docker service rm "$svc" >/dev/null 2>&1 || true
  sleep 2
}

run_combo crash-sf start-first crash
run_combo crash-xf stop-first crash
run_combo bad-sf   start-first v2bad
run_combo bad-xf   stop-first v2bad
say "B5A-MATRIX-DONE"
