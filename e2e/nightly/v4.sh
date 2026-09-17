#!/bin/sh
# e2e/nightly V4 (minimal subset) — connection governance at task kill:
#   A) abrupt-exit backend + continuous in-flight POST (non-replayable)
#      -> the kill MUST surface as >=1 gateway 502 (accident class still
#      reproducible = detection capability intact; Dokploy #5281 family)
#   C) graceful-drain backend (GRACEFUL=1, stop-grace 15s), same kill
#      -> zero failures (governance primary key works)
#   K) auxiliary (serversTransport): keep-alive GET client across a healthy
#      update with short-idle serversTransport configured -> zero failures
#      (idle-pool staleness self-heals; serversTransport is a hygiene item,
#      spike/b README section 5).
# Runs INSIDE a docker:29.8.1-dind prepared by infra-b.sh.
#
# Derived from spike/b/scripts/in-b3d-v4post.sh (phases A/C) and the
# serversTransport section of spike/b/scripts/in-b3-v3v4.sh.
set -u
. /tmp/lib.sh

NET=nightly-b-net
CFG=/tmp/nightly-cfg/config.json

wait_img_strict() { # <svc> <image> <cap_s> (wait_img alias for readability)
    wait_img "$1" "$2" "$3"
}

cat >"$CFG" <<'EOF'
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

nl "=== V4.1 backends: A abrupt / C graceful, slow responses (2s) for in-flight ==="
docker service create --name app-v4a --network "$NET" \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
    --update-failure-action pause --update-order start-first --update-monitor 5s \
    --env SLOW_MS=2000 spike-b/app:v1 >/dev/null || fatal "create app-v4a"
docker service create --name app-v4c --network "$NET" \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
    --update-failure-action pause --update-order start-first --update-monitor 5s \
    --env SLOW_MS=2000 --env GRACEFUL=1 --stop-grace-period 15s spike-b/app:v1 >/dev/null || fatal "create app-v4c"
wait_img_strict app-v4a spike-b/app:v1 40 || fatal "v4a baseline never healthy"
wait_img_strict app-v4c spike-b/app:v1 40 || fatal "v4c baseline never healthy"
sleep 8

# phase <tag> <host> <svc>: continuous in-flight POST, then image update +
# --force (two kills per phase, spike R3 protocol)
phase() {
    tag=$1
    host=$2
    svc=$3
    nl "##### V4-POST-$tag: client up (continuous in-flight), update in 4s #####"
    docker run -d --name "post-$tag" --network "$NET" --entrypoint /probe spike-b/app:v1 \
        client -url http://edge/ -host "$host" -method POST -every-ms 0 -dur 55s >/dev/null || fatal "post client $tag"
    sleep 4
    docker service update --image spike-b/app:v2 "$svc" >/tmp/v4p-$tag.out 2>&1
    nl "V4-POST-$tag update exit=$?"
    wait_img_strict "$svc" spike-b/app:v2 40 || nl "WARN $svc v2 not healthy (update may have paused)"
    sleep 3
    nl "V4-POST-$tag second kill (--force)"
    docker service update --force "$svc" >/tmp/v4p-$tag-f.out 2>&1
    nl "V4-POST-$tag force exit=$?"
    wait_img_strict "$svc" spike-b/app:v2 40 || nl "WARN $svc v2 not healthy after force"
    wait_container_gone "post-$tag" 60 || nl "WARN post-$tag still running"
    nl "V4-POST-$tag summary: $(docker logs "post-$tag" 2>&1 | grep '"ev":"client-summary"')"
    nl "V4-POST-$tag bad samples:"
    docker logs "post-$tag" 2>&1 | grep -E '"fail"|"bad"' | head -8 || echo "(none)"
}

nl "=== V4.2 phase A: abrupt backend ==="
phase a v4a.local app-v4a
A_FAILS=$(docker logs post-a 2>&1 | grep '"ev":"client-summary"' | grep -o '"total_fail":[0-9]*' | tr -dc '0-9')
A_FAILS=${A_FAILS:-0}
[ "$A_FAILS" -ge 1 ]
assert "V4-ASSERT-1 ABRUPT_INFLIGHT_POST_KILL_SURFACES_502" $? "total_fail=$A_FAILS (expected >=1)"
docker rm -f post-a >/dev/null 2>&1 || true

nl "=== V4.3 phase C: graceful backend (GRACEFUL=1, stop-grace 15s) ==="
phase c v4c.local app-v4c
C_FAILS=$(docker logs post-c 2>&1 | grep '"ev":"client-summary"' | grep -o '"total_fail":[0-9]*' | tr -dc '0-9')
C_FAILS=${C_FAILS:-0}
[ "$C_FAILS" = "0" ]
assert "V4-ASSERT-2 GRACEFUL_INFLIGHT_POST_ZERO_FAILURE" $? "total_fail=$C_FAILS (expected 0)"
docker rm -f post-c >/dev/null 2>&1 || true

nl "=== V4.4 aux: serversTransport (short idle) + keep-alive GET across update ==="
cat >"$CFG" <<'EOF'
{"http":{"routers":{"v4k":{"rule":"Host(`v4k.local`)","service":"v4sk","entryPoints":["web"]}},
"services":{"v4sk":{"loadBalancer":{"servers":[{"url":"http://app-v4a:8080"}],"serversTransport":"short@http"}}},
"serversTransports":{"short":{"maxIdleConnsPerHost":1,"forwardingTimeouts":{"idleConnTimeout":"1s","dialTimeout":"5s"}}}}}
EOF
sleep 8 # Traefik poll cycles
nl "T0: update app-v4a v2 -> v1 with mitigation active (keep-alive client in idle gap)"
docker run -d --name ka-v4 --network "$NET" --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host v4k.local -every-ms 6000 -dur 45s -keepalive >/dev/null || fatal "ka client"
sleep 4 # inside a 6s idle gap after the client's first request
docker service update --image spike-b/app:v1 app-v4a >/tmp/v4k.out 2>&1
nl "update exit=$?"
wait_img_strict app-v4a spike-b/app:v1 40 || nl "WARN app-v4a v1 not healthy"
wait_container_gone ka-v4 60 || nl "WARN ka client still running"
nl "--- keep-alive client summary:"
docker logs ka-v4 2>&1 | grep '"ev":"client-summary"'
KA_FAILS=$(docker logs ka-v4 2>&1 | grep '"ev":"client-summary"' | grep -o '"total_fail":[0-9]*' | tr -dc '0-9')
KA_FAILS=${KA_FAILS:-0}
[ "$KA_FAILS" = "0" ]
assert "V4-ASSERT-3 KEEPALIVE_IDLE_POOL_ZERO_FAILURE" $? "total_fail=$KA_FAILS (expected 0)"

PROV_ERRS=$(docker service logs edge --tail 400 2>&1 | grep -ci 'provider error')
PROV_ERRS=${PROV_ERRS:-0}
[ "$PROV_ERRS" = "0" ]
assert "V4-ASSERT-4 TRAEFIK_CONFIG_APPLIED_CLEAN" $? "provider error lines=$PROV_ERRS"
docker rm -f ka-v4 >/dev/null 2>&1 || true

docker service rm app-v4a app-v4c >/dev/null 2>&1 || true
finish
