#!/bin/sh
# e2e/nightly — in-dind infra bootstrap for the V1/V3/V4 suites.
# Runs INSIDE docker:29.8.1-dind (alpine busybox sh).
#
# Derived from spike/b/scripts/in-b0-infra.sh (Spike B B0; see
# spike/b/README.md for the findings behind every step). Differences from
# the spike original:
#   - fixture images trimmed to the three variants the nightly suites use
#     (v1 / v2 / v2bad); crash / v2slow are not needed here;
#   - network / config-dir renamed to the nightly namespace;
#   - the probe binary is staged by the host runner at /tmp/nb-ctx/probe via
#     exec+stdin (spike used a bind mount; docker cp is banned, see
#     e2e/README.md known issue);
#   - the fixture Dockerfile is spike/b/dockerctx/Dockerfile.fixture staged
#     verbatim at /tmp/nb-ctx/Dockerfile (referenced, not copied).
# Fixtures build with --provenance=false --sbom=false (spike/a README §7#9:
# buildx attestation manifest lists break local digest references).
set -u
. /tmp/lib.sh

NET=nightly-b-net
CTX=/tmp/nb-ctx
CFG=/tmp/nightly-cfg
PSFMT='{{.ID}} {{.Name}} {{.Image}} {{.CurrentState}} {{.DesiredState}}'

nl "=== infra: engine identity ==="
docker version --format 'engine={{.Server.Version}} api={{.Server.APIVersion}}' || fatal "docker version"
nl "storage driver: $(docker info --format '{{.Driver}}' 2>/dev/null)"

nl "=== infra 1/8: swarm init (single node) ==="
if docker info --format '{{.Swarm.ControlAvailable}}' 2>/dev/null | grep -q true; then
    nl "swarm already active"
else
    ADV=$(hostname -i | awk '{print $1}')
    nl "advertise-addr=$ADV"
    docker swarm init --advertise-addr "$ADV" || fatal "swarm init"
fi

nl "=== infra 2/8: attachable overlay network ==="
docker network rm "$NET" >/dev/null 2>&1 || true
docker network create -d overlay --attachable "$NET" >/dev/null || fatal "overlay create"

nl "=== infra 3/8: pull alpine:3.20 ==="
docker pull alpine:3.20 >/dev/null || fatal "pull alpine"

nl "=== infra 4/8: pull traefik (pinned v3 line, first available wins) ==="
TRAEFIK_TAG=""
for t in v3.5 v3.4 v3.3 v3.2; do
    nl "trying traefik:$t"
    if docker pull "traefik:$t"; then TRAEFIK_TAG="$t"; break; fi
done
[ -n "$TRAEFIK_TAG" ] || fatal "no traefik v3 tag pullable"
nl "pinned traefik tag=$TRAEFIK_TAG"

nl "=== infra 5/8: build fixture images (provenance/sbom OFF) ==="
bf() { # <tag> <version> <healthmode>
    docker build --provenance=false --sbom=false \
        --build-arg APP_VERSION="$2" --build-arg HEALTH_MODE="$3" \
        -f "$CTX/Dockerfile" -t "spike-b/app:$1" "$CTX" >"/tmp/build-$1.log" 2>&1
    rc=$?
    tail -2 "/tmp/build-$1.log"
    [ $rc -eq 0 ] || fatal "build fixture $1 rc=$rc"
}
bf v1 v1 ok
bf v2 v2 ok
bf v2bad v2bad fail
docker image ls spike-b/app --format '{{.Repository}}:{{.Tag}} {{.ID}}'

nl "=== infra 6/8: standalone sanity of v1 fixture ==="
docker rm -f b-sanity >/dev/null 2>&1 || true
docker run -d --name b-sanity spike-b/app:v1 >/dev/null || fatal "sanity run"
sleep 2
docker exec b-sanity /probe hc -url http://127.0.0.1:8080/health >/dev/null || fatal "sanity /health"
docker exec b-sanity /probe hc -url http://127.0.0.1:8080/ >/dev/null || fatal "sanity /"
docker rm -f b-sanity >/dev/null

nl "=== infra 7/8: cfgsvc (HTTP-provider config stand-in) ==="
mkdir -p "$CFG"
echo '{}' >"$CFG/config.json"
docker rm -f cfg >/dev/null 2>&1 || true
docker run -d --name cfg --network "$NET" -v "$CFG:/data" \
    --entrypoint /probe spike-b/app:v1 cfgsvc -dir /data -addr :9000 >/dev/null || fatal "cfgsvc"
sleep 1

nl "=== infra 8/8: traefik as swarm service (HTTP provider, poll 2s) ==="
docker service rm edge >/dev/null 2>&1 || true
docker service create --name edge --network "$NET" \
    "traefik:$TRAEFIK_TAG" \
    --entryPoints.web.address=:80 \
    --providers.http.endpoint=http://cfg:9000/config \
    --providers.http.pollInterval=2s --providers.http.pollTimeout=5s \
    --log.level=INFO >/dev/null || fatal "traefik service create"
if wait_run edge 60; then
    nl "edge task running"
else
    docker service ps edge --no-trunc
    fatal "edge not running"
fi

nl "=== infra sanity: overlay DNS + config fetch ==="
docker run --rm --network "$NET" --entrypoint /probe spike-b/app:v1 resolve -host cfg >/dev/null || fatal "resolve cfg"
docker run --rm --network "$NET" --entrypoint /probe spike-b/app:v1 resolve -host edge >/dev/null || fatal "resolve edge"
docker run --rm --network "$NET" --entrypoint /probe spike-b/app:v1 resolve -host tasks.edge >/dev/null || fatal "resolve tasks.edge"
docker run --rm --network "$NET" --entrypoint /probe spike-b/app:v1 client -url http://cfg:9000/healthz -count 1 -every-ms 0 -dur 0s >/dev/null || fatal "cfg healthz"

nl "INFRA-B-OK"
exit 0
