#!/bin/sh
# Spike B experiment 3d: V4 round 3 - POST (non-replayable) requests vs the
# in-flight task kill. GET proved invisible (backend transport transparently
# retries idempotent requests); POST is where the kill must surface as 502.
#   A) abrupt-exit backend  -> expect visible failures at kill
#   C) graceful-drain backend (GRACEFUL=1, stop-grace 15s) -> expect zero
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
CFG=/tmp/spike-b-cfg/config.json
CTX=/work-src/dockerctx

wait_healthy() { # svc image timeout_s
  svc=$1; img=$2; i=0
  while [ $i -lt $3 ]; do
    if docker service ps "$svc" --format '{{.Image}} {{.CurrentState}}' 2>/dev/null | grep -q "^spike-b/app:$img Running"; then return 0; fi
    i=$((i+1)); sleep 1
  done
  return 1
}
wait_gone() { # container timeout_s
  i=0
  while [ $i -lt $2 ]; do
    R=$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)
    [ "$R" = "false" ] && return 0
    i=$((i+1)); sleep 1
  done
  say "WARN $1 still running"
}

say "=== rebuild v1/v2 fixtures with -method-capable client ==="
rb() {
  docker build --provenance=false --sbom=false \
    --build-arg APP_VERSION="$2" --build-arg HEALTH_MODE="$3" \
    --build-arg LISTEN_DELAY_S="$4" --build-arg CRASH_ON_START="$5" \
    -f "$CTX/Dockerfile.fixture" -t "spike-b/app:$1" "$CTX" > "/tmp/rb-$1.log" 2>&1 \
    || { say "FATAL rebuild $1"; cat "/tmp/rb-$1.log"; exit 1; }
  say "rebuilt spike-b/app:$1"
}
rb v1 v1 ok 0 0
rb v2 v2 ok 0 0

cat > "$CFG" <<'EOF'
{"http":{"routers":{
"v4a":{"rule":"Host(`v4a.local`)","service":"v4sa","entryPoints":["web"]},
"v4c":{"rule":"Host(`v4c.local`)","service":"v4sc","entryPoints":["web"]}},
"services":{
"v4sa":{"loadBalancer":{"servers":[{"url":"http://app-v4a:8080"}]}},
"v4sc":{"loadBalancer":{"servers":[{"url":"http://app-v4c:8080"}]}}}}}
EOF

docker service rm app-v4a app-v4c >/dev/null 2>&1 || true
docker rm -f post-a post-c >/dev/null 2>&1 || true
sleep 2
docker events > /tmp/v4p.events 2>&1 &
EV_PID=$!
sleep 1

docker service create --name app-v4a --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  --env SLOW_MS=2000 spike-b/app:v1 >/dev/null || exit 1
docker service create --name app-v4c --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  --env SLOW_MS=2000 --env GRACEFUL=1 --stop-grace-period 15s spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-v4a v1 40 || { say "FATAL v4a baseline"; exit 1; }
wait_healthy app-v4c v1 40 || { say "FATAL v4c baseline"; exit 1; }
sleep 8

phase() { # tag host svc
  tag=$1; host=$2; svc=$3
  say "##### V4-POST-$tag: client up (continuous in-flight), update in 4s #####"
  docker run -d --name "post-$tag" --network $NET --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host "$host" -method POST -every-ms 0 -dur 55s >/dev/null
  sleep 4
  docker service update --image spike-b/app:v2 "$svc" > /tmp/v4p-$tag.out 2>&1
  say "V4-POST-$tag update exit=$?"
  wait_healthy "$svc" v2 40 || say "WARN $svc v2 not healthy"
  sleep 3
  say "V4-POST-$tag second kill (--force)"
  docker service update --force "$svc" > /tmp/v4p-$tag-f.out 2>&1
  say "V4-POST-$tag force exit=$?"
  wait_healthy "$svc" v2 40 || say "WARN $svc v2 not healthy after force"
  wait_gone "post-$tag" 60
  say "V4-POST-$tag summary: $(docker logs post-$tag 2>&1 | grep client-summary)"
  say "V4-POST-$tag bad samples:"
  docker logs "post-$tag" 2>&1 | grep -E '"fail"|"bad"' | head -8 || echo "(none)"
  docker rm -f "post-$tag" >/dev/null 2>&1 || true
  sleep 2
}

phase a v4a.local app-v4a
say "(C backend graceful: GRACEFUL=1 stop-grace 15s)"
phase c v4c.local app-v4c

say "=== post-mortem: killed task containers (graceful markers) ==="
for c in $(docker ps -a --format '{{.Names}}' | grep -E 'app-v4[ac]'); do
  say "--- container $c: $(docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}} finished={{.State.FinishedAt}}' $c)"
  docker logs "$c" 2>&1 | grep -E 'graceful|server-listening' | head -3
done
say "=== events: container die + health for v4a/v4c ==="
grep -E 'container (die|start)' /tmp/v4p.events | grep -E 'app-v4[ac]' | head -12
kill $EV_PID 2>/dev/null
wait $EV_PID 2>/dev/null
say "--- Traefik log 5xx excerpts:"
docker service logs edge --tail 400 2>&1 | grep -E ' 502 | 503 ' | tail -10 || echo "(none)"
say "B3D-V4POST-EXPERIMENT-DONE"
