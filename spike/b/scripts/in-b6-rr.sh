#!/bin/sh
# Spike B experiment 6: snapshot-replay rollback timing (design acceptance:
# rollback completes in <60s and the previous version serves again).
# healthy v1 -> update v2 (env... here image-tag switch) -> replay v1 spec.
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
probe_ver() { # svc -> summary line of a 3-sample probe
  docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 \
    client -url "http://$1:8080/" -count 3 -every-ms 300 -dur 0s 2>&1 | grep '"ev":"client-summary"'
}

docker service rm app-rr >/dev/null 2>&1 || true
sleep 2

say "=== B6.1 healthy v1 ==="
docker service create --name app-rr --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-rr 30 || { say "FATAL baseline"; exit 1; }
T1=$(docker service ps app-rr --format '{{.ID}}' | head -1)
say "v1 task=$T1 ; $(probe_ver app-rr)"

say "=== B6.2 upgrade v1 -> v2 ==="
U0=$(date +%s)
docker service update --image spike-b/app:v2 app-rr >/dev/null 2>&1
say "upgrade exit=$?"
wait_healthy app-rr 30 || { say "FATAL v2 never healthy"; exit 1; }
U1=$(date +%s)
say "upgrade seconds=$((U1-U0)) ; $(probe_ver app-rr)"

say "=== B6.3 snapshot replay back to v1 spec ==="
R0=$(date +%s)
docker service update --image spike-b/app:v1 app-rr > /tmp/b6.replay.out 2>&1
say "replay exit=$?"
# completion = task healthy AND probe sees v1 content
i=0
while [ $i -lt 60 ]; do
  st=$(docker service ps app-rr --format '{{.CurrentState}}' 2>/dev/null | head -1)
  case "$st" in Running*)
    VER=$(probe_ver app-rr | grep -o '"per_version":{[^}]*}')
    echo "$VER" | grep -q '"v1"' && break
    ;;
  esac
  i=$((i+1)); sleep 1
done
R1=$(date +%s)
say "replay seconds=$((R1-R0)) (completion poll $i s)"
docker service ps app-rr --format "$psfmt" --no-trunc
say "final probe: $(probe_ver app-rr)"
if [ $((R1-R0)) -lt 60 ]; then say "B6-ASSERT ROLLBACK_UNDER_60S: PASS ($((R1-R0))s)"; else say "B6-ASSERT ROLLBACK_UNDER_60S: FAIL ($((R1-R0))s)"; fi
docker service rm app-rr >/dev/null 2>&1 || true
say "B6-RR-EXPERIMENT-DONE"
