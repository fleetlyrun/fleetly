#!/bin/sh
# Spike B experiment 3: V3 (routing stability across a healthy rolling
# update through Traefik) + V4 (stale keep-alive backend connections,
# Dokploy #5281 pattern, and the serversTransport mitigation).
# Traefik runs as a swarm service configured ONLY via the HTTP provider
# (cfgsvc serves /tmp/spike-b-cfg/config.json, re-read per request).
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
psfmt='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'
CFG=/tmp/spike-b-cfg/config.json

wait_healthy() { # svc timeout_s
  svc=$1; i=0
  while [ $i -lt $2 ]; do
    st=$(docker service ps "$svc" --format '{{.CurrentState}}' 2>/dev/null | head -1)
    case "$st" in Running*) return 0;; esac
    i=$((i+1)); sleep 1
  done
  return 1
}
wait_prober() { # container timeout_s
  i=0
  while [ $i -lt $2 ]; do
    R=$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)
    [ "$R" = "false" ] && return 0
    i=$((i+1)); sleep 1
  done
  say "WARN prober $1 still running"
}
vips() { docker network inspect -v $NET --format '{{range $n, $s := .Services}}{{if $n}}{{$n}}={{$s.VIP}} {{end}}{{end}}'; }

docker service rm app-v3 app-v4 >/dev/null 2>&1 || true
docker rm -f cl-v3 ka-v4 ctl-v4 >/dev/null 2>&1 || true
sleep 2

#######################################
say "############ V3: zero-failure routing through a healthy update ############"
cat > "$CFG" <<'EOF'
{"http":{"routers":{"v3":{"rule":"Host(`v3.local`)","service":"v3svc","entryPoints":["web"]}},
"services":{"v3svc":{"loadBalancer":{"servers":[{"url":"http://app-v3:8080"}]}}}}}
EOF
docker service create --name app-v3 --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-v3 30 || { say "FATAL app-v3 baseline"; exit 1; }
sleep 6   # let Traefik poll the config (pollInterval=2s)
VIP_BEFORE=$(vips)
say "VIP before: $VIP_BEFORE"

docker run -d --name cl-v3 --network $NET --entrypoint /probe spike-b/app:v1 \
  client -url http://edge/ -host v3.local -every-ms 100 -dur 40s >/dev/null
sleep 2
say "T0: healthy update v1 -> v2 (image switch, healthcheck gated)"
docker service update --image spike-b/app:v2 app-v3 > /tmp/v3.update.out 2>&1
say "update exit=$?"
wait_healthy app-v3 30 || { say "FATAL app-v3 not healthy after update"; exit 1; }
sleep 3
VIP_AFTER=$(vips)
say "VIP after:  $VIP_AFTER"
wait_prober cl-v3 60
say "--- V3 prober summary:"
docker logs cl-v3 2>&1 | grep '"ev":"client-summary"'
say "--- V3 version sightings:"
docker logs cl-v3 2>&1 | grep '"ver"' | head -4
V3FAILS=$(docker logs cl-v3 2>&1 | grep '"ev":"req"' | grep -c '"fail"' || true)
if [ "$V3FAILS" = "0" ]; then say "V3-ASSERT-1 ROUTING_ZERO_FAILURE: PASS"; else say "V3-ASSERT-1 ROUTING_ZERO_FAILURE: FAIL ($V3FAILS)"; fi
if [ "$VIP_BEFORE" = "$VIP_AFTER" ]; then say "V3-ASSERT-2 VIP_STABLE: PASS"; else say "V3-ASSERT-2 VIP_STABLE: FAIL"; fi
docker rm -f cl-v3 >/dev/null 2>&1 || true

#######################################
say "############ V4 phase 1: stale keep-alive reproduction (Dokploy #5281) ############"
cat > "$CFG" <<'EOF'
{"http":{"routers":{"v4":{"rule":"Host(`v4.local`)","service":"v4svc","entryPoints":["web"]}},
"services":{"v4svc":{"loadBalancer":{"servers":[{"url":"http://app-v4:8080"}]}}}}}
EOF
docker service create --name app-v4 --network $NET \
  --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
  --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
  --update-failure-action pause --update-order start-first --update-monitor 5s \
  spike-b/app:v1 >/dev/null || exit 1
wait_healthy app-v4 30 || { say "FATAL app-v4 baseline"; exit 1; }
sleep 6
# keep-alive client: request every 6s on one pooled connection (transport
# idle timeout 10min so the pool always outlives the backend task)
docker run -d --name ka-v4 --network $NET --entrypoint /probe spike-b/app:v1 \
  client -url http://edge/ -host v4.local -every-ms 6000 -dur 110s -keepalive >/dev/null
# control client: fresh connection per request, 500ms cadence
docker run -d --name ctl-v4 --network $NET --entrypoint /probe spike-b/app:v1 \
  client -url http://edge/ -host v4.local -every-ms 500 -dur 110s >/dev/null
sleep 4   # inside a 6s idle gap after the ka client's first request
say "T0: update v1 -> v2 (old task dies ~3-5s later, mid idle gap)"
docker service update --image spike-b/app:v2 app-v4 > /tmp/v4a.update.out 2>&1
say "update exit=$?"
wait_healthy app-v4 30 || { say "FATAL app-v4 not healthy after update"; exit 1; }
say "waiting out phase-1 window..."
sleep 25

say "############ V4 phase 2: serversTransport mitigation (idleConnTimeout=1s) ############"
cat > "$CFG" <<'EOF'
{"http":{"routers":{"v4":{"rule":"Host(`v4.local`)","service":"v4svc","entryPoints":["web"]}},
"services":{"v4svc":{"loadBalancer":{"servers":[{"url":"http://app-v4:8080"}],"serversTransport":"short@http"}}},
"serversTransports":{"short":{"maxIdleConnsPerHost":1,"forwardingTimeouts":{"idleConnTimeout":"1s","dialTimeout":"5s"}}}}}
EOF
say "mitigation config written; waiting 8s for Traefik poll..."
sleep 8
docker service logs edge --tail 5 2>&1 | tail -3
say "T1: update back v2 -> v1 with mitigation active"
docker service update --image spike-b/app:v1 app-v4 > /tmp/v4b.update.out 2>&1
say "update exit=$?"
wait_healthy app-v4 30 || { say "FATAL app-v4 not healthy after revert"; exit 1; }
wait_prober ka-v4 120
wait_prober ctl-v4 120
say "--- V4 keep-alive client summary:"
docker logs ka-v4 2>&1 | grep '"ev":"client-summary"'
say "--- V4 keep-alive client failure samples:"
docker logs ka-v4 2>&1 | grep '"fail"' || echo "(none)"
say "--- V4 control (fresh-conn) client summary:"
docker logs ctl-v4 2>&1 | grep '"ev":"client-summary"'
say "--- V4 control failure samples:"
docker logs ctl-v4 2>&1 | grep '"fail"' || echo "(none)"
say "--- V4 Traefik log excerpts (502/disconnect/error):"
docker service logs edge --tail 300 2>&1 | grep -iE '502|error|disconnect|reset' | tail -15
docker rm -f ka-v4 ctl-v4 >/dev/null 2>&1 || true

say "B3-V3V4-EXPERIMENT-DONE"
