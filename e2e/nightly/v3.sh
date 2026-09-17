#!/bin/sh
# e2e/nightly V3 — zero-failure routing across a healthy rolling update,
# service VIP stable across the update.
# Runs INSIDE a docker:29.8.1-dind prepared by infra-b.sh (Traefik v3 via
# HTTP provider; cfgsvc re-reads config.json per request).
#
# Derived from spike/b/scripts/in-b3-v3v4.sh, V3 section (spike/b README
# section 4). Keep from the spike: 100ms client cadence, VIP captured via
# `network inspect -v` BEFORE and AFTER the update (inspect needs -v to show
# .Services at all, spike/b README #7).
#   V3-ASSERT-1 ROUTING_ZERO_FAILURE
#   V3-ASSERT-2 VIP_STABLE
set -u
. /tmp/lib.sh

NET=nightly-b-net
CFG=/tmp/nightly-cfg/config.json

vips() {
    docker network inspect -v "$NET" --format '{{range $n, $s := .Services}}{{if $n}}{{$n}}={{$s.VIP}} {{end}}{{end}}'
}

docker service rm app-v3 >/dev/null 2>&1 || true
docker rm -f cl-v3 >/dev/null 2>&1 || true
sleep 2

nl "=== V3.1 publish route v3.local -> app-v3:8080 via cfgsvc ==="
cat >"$CFG" <<'EOF'
{"http":{"routers":{"v3":{"rule":"Host(`v3.local`)","service":"v3svc","entryPoints":["web"]}},
"services":{"v3svc":{"loadBalancer":{"servers":[{"url":"http://app-v3:8080"}]}}}}}
EOF

nl "=== V3.2 create healthy app-v3 baseline ==="
docker service create --name app-v3 --network "$NET" \
    --health-cmd "/probe hc -url http://127.0.0.1:8080/health" \
    --health-interval 1s --health-timeout 1s --health-retries 3 --health-start-period 2s \
    --update-failure-action pause --update-order start-first --update-monitor 5s \
    spike-b/app:v1 >/dev/null || fatal "create app-v3"
wait_run app-v3 40 || {
    docker service ps app-v3 --no-trunc
    fatal "app-v3 baseline never healthy"
}
sleep 6 # let Traefik poll the config (pollInterval=2s)
VIP_BEFORE=$(vips)
nl "VIP before: $VIP_BEFORE"
echo "$VIP_BEFORE" | grep -q 'app-v3=' || fatal "no VIP for app-v3 (inspect -v empty?)"

nl "=== V3.3 prober up (100ms), then healthy update v1 -> v2 ==="
docker run -d --name cl-v3 --network "$NET" --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host v3.local -every-ms 100 -dur 40s >/dev/null || fatal "client prober"
sleep 2
docker service update --image spike-b/app:v2 app-v3 >/tmp/v3-update.out 2>&1
nl "update exit=$?"
wait_img app-v3 spike-b/app:v2 40 || {
    docker service ps app-v3 --no-trunc
    fatal "app-v3 not healthy after update"
}
sleep 3
VIP_AFTER=$(vips)
nl "VIP after:  $VIP_AFTER"

wait_container_gone cl-v3 60 || nl "WARN client still running"
nl "--- V3 client summary:"
docker logs cl-v3 2>&1 | grep '"ev":"client-summary"'
V3FAILS=$(docker logs cl-v3 2>&1 | grep '"ev":"req"' | grep -c '"fail"')
[ "$V3FAILS" = "0" ]
assert "V3-ASSERT-1 ROUTING_ZERO_FAILURE" $? "fails=$V3FAILS"

[ "$VIP_BEFORE" = "$VIP_AFTER" ]
assert "V3-ASSERT-2 VIP_STABLE" $? "before=[$VIP_BEFORE] after=[$VIP_AFTER]"

docker rm -f cl-v3 >/dev/null 2>&1 || true
docker service rm app-v3 >/dev/null 2>&1 || true
finish
