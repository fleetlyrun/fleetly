#!/bin/sh
# Spike B inner infra bootstrap. Runs INSIDE docker:29.8.1-dind (alpine
# busybox sh). Sets up: single-node swarm, attachable overlay, alpine +
# traefik v3 pulls, five fixture images (buildx, provenance/sbom off per
# spike/a lesson 9), the Traefik HTTP-provider config service, and the
# Traefik swarm service. Ends with cross-overlay DNS sanity checks.
set -u
say() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
NET=spike-b-net
CTX=/work-src/dockerctx

say "=== B0.i engine ==="
docker version --format 'engine={{.Server.Version}} api={{.Server.APIVersion}}' || exit 1

say "=== B0.1 swarm init (single node, manager) ==="
if docker info --format '{{.Swarm.ControlAvailable}}' 2>/dev/null | grep -q true; then
  say "swarm already active"
else
  ADV=$(hostname -i | awk '{print $1}')
  say "advertise-addr=$ADV"
  docker swarm init --advertise-addr "$ADV" || exit 1
fi

say "=== B0.2 attachable overlay network ==="
docker network rm $NET >/dev/null 2>&1 || true
docker network create -d overlay --attachable $NET || exit 1

say "=== B0.3 pull alpine:3.20 ==="
docker pull alpine:3.20 || exit 1

say "=== B0.4 pull traefik (pinned v3 line, first available tag wins) ==="
TRAEFIK_TAG=""
for t in v3.5 v3.4 v3.3 v3.2; do
  say "trying traefik:$t"
  if docker pull "traefik:$t"; then TRAEFIK_TAG="$t"; break; fi
done
[ -n "$TRAEFIK_TAG" ] || { say "FATAL: no traefik v3 tag pullable"; exit 1; }
say "pinned traefik tag=$TRAEFIK_TAG"
docker image inspect "traefik:$TRAEFIK_TAG" --format 'traefik image-id={{.Id}}'
echo "$TRAEFIK_TAG" > /tmp/spike-b-traefik-tag

say "=== B0.5 build fixture images (provenance/sbom OFF, spike/a lesson 9) ==="
bf() { # tag version healthmode delay crash
  docker build --provenance=false --sbom=false \
    --build-arg APP_VERSION="$2" --build-arg HEALTH_MODE="$3" \
    --build-arg LISTEN_DELAY_S="$4" --build-arg CRASH_ON_START="$5" \
    -f "$CTX/Dockerfile.fixture" -t "spike-b/app:$1" "$CTX" > "/tmp/build-$1.log" 2>&1
  rc=$?
  tail -2 "/tmp/build-$1.log"
  if [ $rc -ne 0 ]; then say "FATAL build $1 rc=$rc"; cat "/tmp/build-$1.log"; exit 1; fi
}
bf v1     v1     ok   0 0
bf v2     v2     ok   0 0
bf v2bad  v2bad  fail 0 0
bf v2slow v2     ok   6 0
bf crash  crash  ok   0 1
docker image ls spike-b/app --format '{{.Repository}}:{{.Tag}} {{.ID}}'

say "=== B0.6 standalone sanity of v1 fixture ==="
docker rm -f b-sanity >/dev/null 2>&1 || true
docker run -d --name b-sanity spike-b/app:v1 >/dev/null || exit 1
sleep 2
docker exec b-sanity /probe hc -url http://127.0.0.1:8080/health || { say "FATAL sanity /health"; exit 1; }
docker exec b-sanity /probe hc -url http://127.0.0.1:8080/ || { say "FATAL sanity /"; exit 1; }
docker rm -f b-sanity >/dev/null

say "=== B0.7 cfgsvc (control-plane stand-in for HTTP provider) ==="
mkdir -p /tmp/spike-b-cfg
echo '{}' > /tmp/spike-b-cfg/config.json
docker rm -f cfg >/dev/null 2>&1 || true
docker run -d --name cfg --network $NET -v /tmp/spike-b-cfg:/data \
  --entrypoint /probe spike-b/app:v1 cfgsvc -dir /data -addr :9000 >/dev/null || exit 1
sleep 1

say "=== B0.8 traefik v3 as swarm service (HTTP provider, poll 2s) ==="
docker service rm edge >/dev/null 2>&1 || true
docker service create --name edge --network $NET \
  "traefik:$TRAEFIK_TAG" \
  --entryPoints.web.address=:80 \
  --providers.http.endpoint=http://cfg:9000/config \
  --providers.http.pollInterval=2s --providers.http.pollTimeout=5s \
  --log.level=INFO >/dev/null || exit 1
ST=""
i=0
while [ $i -lt 45 ]; do
  ST=$(docker service ps edge --format '{{.CurrentState}}' 2>/dev/null | head -1)
  case "$ST" in Running*) break;; esac
  i=$((i+1)); sleep 1
done
say "edge current-state=$ST (after ${i}s)"
case "$ST" in
  Running*) ;;
  *) say "FATAL edge not running"; docker service ps edge --no-trunc; exit 1;;
esac

say "=== B0.9 infra sanity: overlay DNS + config fetch ==="
docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 resolve -host cfg || exit 1
docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 resolve -host edge || exit 1
docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 resolve -host tasks.edge || exit 1
docker run --rm --network $NET --entrypoint /probe spike-b/app:v1 client -url http://cfg:9000/healthz -count 1 -every-ms 0 -dur 0s || exit 1

say "B0-INFRA-OK"
