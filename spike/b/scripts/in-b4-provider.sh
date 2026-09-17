#!/bin/sh
# Spike B experiment 4: Traefik HTTP-provider delivery discipline.
#   a) good config  -> both app routes live
#   b) EMPTY routers/services served -> what does Traefik do? (informs the
#      "empty config must not be persisted" control-plane rule)
#   c) one app's router broken (nonexistent middleware) -> other app keeps
#      routing; then fully malformed JSON -> Traefik keeps last good config
#   d) config service unreachable -> Traefik keeps serving previous config
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
CFG=/tmp/spike-b-cfg/config.json

probe_once() { # host -> prints the one sample line + summary
  docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 \
    client -url http://edge/ -host "$1" -count 1 -every-ms 0 -dur 0s 2>&1 | grep -E '"ev":"req"|"ev":"client-summary"' | head -2
}

docker service rm app-x1 app-x2 >/dev/null 2>&1 || true
sleep 2
docker service create --name app-x1 --network $NET spike-b/app:v1 >/dev/null || exit 1
docker service create --name app-x2 --network $NET spike-b/app:v1 >/dev/null || exit 1
i=0
while [ $i -lt 30 ]; do
  S1=$(docker service ps app-x1 --format '{{.CurrentState}}' | head -1)
  S2=$(docker service ps app-x2 --format '{{.CurrentState}}' | head -1)
  case "$S1$S2" in Running*Running*) break;; esac
  i=$((i+1)); sleep 1
done
say "baselines: x1=$S1 x2=$S2"

GOOD='{"http":{"routers":{"x1":{"rule":"Host(`x1.local`)","service":"x1svc","entryPoints":["web"]},"x2":{"rule":"Host(`x2.local`)","service":"x2svc","entryPoints":["web"]}},"services":{"x1svc":{"loadBalancer":{"servers":[{"url":"http://app-x1:8080"}]}},"x2svc":{"loadBalancer":{"servers":[{"url":"http://app-x2:8080"}]}}}}}'

say "=== P-a: good config -> both routes live ==="
echo "$GOOD" > "$CFG"
sleep 8
say "a/x1: $(probe_once x1.local | tr '\n' ' ')"
say "a/x2: $(probe_once x2.local | tr '\n' ' ')"

say "=== P-b: EMPTY routers/services ==="
echo '{"http":{"routers":{},"services":{}}}' > "$CFG"
sleep 8
say "b-empty/x1: $(probe_once x1.local | tr '\n' ' ')"
say "b-empty/x2: $(probe_once x2.local | tr '\n' ' ')"
say "b-empty object '{}' variant:"
echo '{}' > "$CFG"
sleep 8
say "b-empty2/x1: $(probe_once x1.local | tr '\n' ' ')"
say "--- restoring good config"
echo "$GOOD" > "$CFG"
sleep 8
say "b-restore/x1: $(probe_once x1.local | tr '\n' ' ')"

say "=== P-c1: single app bad config (x2 references nonexistent middleware) ==="
cat > "$CFG" <<'EOF'
{"http":{"routers":{"x1":{"rule":"Host(`x1.local`)","service":"x1svc","entryPoints":["web"]},"x2":{"rule":"Host(`x2.local`)","service":"x2svc","entryPoints":["web"],"middlewares":["nope@file"]}},"services":{"x1svc":{"loadBalancer":{"servers":[{"url":"http://app-x1:8080"}]}},"x2svc":{"loadBalancer":{"servers":[{"url":"http://app-x2:8080"}]}}}}}
EOF
sleep 8
say "c1/x1 (must keep serving): $(probe_once x1.local | tr '\n' ' ')"
say "c1/x2 (broken app):        $(probe_once x2.local | tr '\n' ' ')"

say "=== P-c2: malformed JSON -> Traefik keeps last good config ==="
printf 'not-json{{{' > "$CFG"
sleep 8
say "c2/x1: $(probe_once x1.local | tr '\n' ' ')"
say "c2/x2 (c1-bad router should now be back per last GOOD config? or still broken): $(probe_once x2.local | tr '\n' ' ')"
echo "$GOOD" > "$CFG"
sleep 8

say "=== P-d: config service unreachable -> Traefik keeps serving ==="
docker stop cfg >/dev/null
sleep 8
say "d/x1: $(probe_once x1.local | tr '\n' ' ')"
say "d/x2: $(probe_once x2.local | tr '\n' ' ')"
docker start cfg >/dev/null
sleep 4

say "--- Traefik log excerpts:"
docker service logs edge --tail 200 2>&1 | grep -iE 'error|warn|skip|retry' | tail -15

docker service rm app-x1 app-x2 >/dev/null 2>&1 || true
say "B4-PROVIDER-EXPERIMENT-DONE"
