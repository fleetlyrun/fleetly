#!/bin/sh
# Spike B experiment 3c: V4 round 2 - the in-flight request kill (the real
# Dokploy #5281 severity) in three configurations:
#   A) abrupt-exit backend, default Traefik transport  -> expect 502s (repro)
#   B) abrupt-exit backend + serversTransport(idleConnTimeout=1s,
#      maxIdleConnsPerHost=1) -> does transport tuning help in-flight? (no)
#   C) graceful-drain backend (GRACEFUL=1, stop-grace 15s) -> expect 0 fails
# Also rebuilds fixtures first (probe binary gained SLOW_MS / GRACEFUL and
# 5xx-counting); the new binary arrives via the bind mount.
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

say "=== rebuild fixtures with upgraded probe binary ==="
rb() {
  docker build --provenance=false --sbom=false \
    --build-arg APP_VERSION="$2" --build-arg HEALTH_MODE="$3" \
    --build-arg LISTEN_DELAY_S="$4" --build-arg CRASH_ON_START="$5" \
    -f "$CTX/Dockerfile.fixture" -t "spike-b/app:$1" "$CTX" > "/tmp/rb-$1.log" 2>&1 \
    || { say "FATAL rebuild $1"; cat "/tmp/rb-$1.log"; exit 1; }
  say "rebuilt spike-b/app:$1"
}
rb v1     v1     ok   0 0
rb v2     v2     ok   0 0
rb v2bad  v2bad  fail 0 0
rb v2slow v2     ok   6 0
rb crash  crash  ok   0 1

say "=== one config, three routers: v4a plain / v4b serversTransport / v4c plain ==="
cat > "$CFG" <<'EOF'
{"http":{"routers":{
"v4a":{"rule":"Host(`v4a.local`)","service":"v4sa","entryPoints":["web"]},
"v4b":{"rule":"Host(`v4b.local`)","service":"v4sb","entryPoints":["web"]},
"v4c":{"rule":"Host(`v4c.local`)","service":"v4sc","entryPoints":["web"]}},
"services":{
"v4sa":{"loadBalancer":{"servers":[{"url":"http://app-v4a:8080"}]}},
"v4sb":{"loadBalancer":{"servers":[{"url":"http://app-v4b:8080"}],"serversTransport":"short@http"}},
"v4sc":{"loadBalancer":{"servers":[{"url":"http://app-v4c:8080"}]}}},
"serversTransports":{"short":{"maxIdleConnsPerHost":1,"forwardingTimeouts":{"idleConnTimeout":"1s","dialTimeout":"5s"}}}}}
EOF

docker service rm app-v4a app-v4b app-v4c >/dev/null 2>&1 || true
docker rm -f ka-a ctl-a ka-b ctl-b ka-c ctl-c >/dev/null 2>&1 || true
sleep 2

mk() { # svc image extra-args...
  svc=$1; img=$2; shift 2
  docker service create --name "$svc" --network $NET \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
    --update-failure-action pause --update-order start-first --update-monitor 5s \
    --env SLOW_MS=2000 \
    "$@" "spike-b/app:$img" >/dev/null || { say "FATAL create $svc"; exit 1; }
}
mk app-v4a v1
mk app-v4b v1
mk app-v4c v1 --env GRACEFUL=1 --stop-grace-period 15s
wait_healthy app-v4a v1 40 || { say "FATAL v4a baseline"; exit 1; }
wait_healthy app-v4b v1 40 || { say "FATAL v4b baseline"; exit 1; }
wait_healthy app-v4c v1 40 || { say "FATAL v4c baseline"; exit 1; }
sleep 8   # Traefik polls the new routers

phase() { # tag host svc
  tag=$1; host=$2; svc=$3
  say "##### V4-$tag: clients up, update in 4s #####"
  docker run -d --name "ka-$tag" --network $NET --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host "$host" -every-ms 3000 -dur 50s -keepalive >/dev/null
  docker run -d --name "ctl-$tag" --network $NET --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host "$host" -every-ms 500 -dur 50s >/dev/null
  sleep 4
  docker service update --image spike-b/app:v2 "$svc" > /tmp/v4-$tag.out 2>&1
  say "V4-$tag update exit=$?"
  wait_healthy "$svc" v2 40 || say "WARN $svc v2 not healthy"
  i=0
  while [ $i -lt 55 ]; do
    R=$(docker inspect -f '{{.State.Running}}' "ka-$tag" 2>/dev/null)
    [ "$R" = "false" ] && break
    i=$((i+1)); sleep 1
  done
  say "V4-$tag keep-alive summary: $(docker logs ka-$tag 2>&1 | grep client-summary)"
  say "V4-$tag keep-alive bad samples:"
  docker logs "ka-$tag" 2>&1 | grep -E '"fail"|"bad"' | head -6 || echo "(none)"
  say "V4-$tag control summary:     $(docker logs ctl-$tag 2>&1 | grep client-summary)"
  say "V4-$tag control bad samples:"
  docker logs "ctl-$tag" 2>&1 | grep -E '"fail"|"bad"' | head -6 || echo "(none)"
  docker rm -f "ka-$tag" "ctl-$tag" >/dev/null 2>&1 || true
  sleep 2
}

phase a v4a.local app-v4a
say "(B uses serversTransport short: idleConnTimeout=1s maxIdleConnsPerHost=1)"
phase b v4b.local app-v4b
say "(C backend is graceful: GRACEFUL=1 stop-grace 15s)"
phase c v4c.local app-v4c

say "--- Traefik log 5xx excerpts:"
docker service logs edge --tail 400 2>&1 | grep -E ' 502 | 503 ' | tail -10 || echo "(none)"
docker service rm app-v4a app-v4b app-v4c >/dev/null 2>&1 || true
say "B3C-V4V2-EXPERIMENT-DONE"
