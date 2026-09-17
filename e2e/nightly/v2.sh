#!/bin/sh
# e2e/nightly V2 — local digest reference, zero pull attempts.
# Runs INSIDE a dedicated (fresh) docker:29.8.1-dind so the dockerd log has
# no background noise; the host runner (run.sh) greps that log for registry
# manifest fetches after this script exits (pull attempts are invisible in
# `docker events`, spike/a README #12 — dockerd log is the only channel).
#
# Derived from spike/a/scripts/e5-v2-digest.sh (Spike A E5; findings in
# spike/a/README.md section 6). Kept from the original:
#   - build with --provenance=false --sbom=false (attestation manifest lists
#     crash swarm tasks, spike/a README #9); docker import fallback;
#   - busybox `timeout` on every daemon-blocking call;
#   - standalone sanity run before any service create;
#   - alpine busybox has no httpd/wget applets -> liveness = task Running +
#     exec echo; scratch image must bundle the dynamic musl loader.
# Dropped: swarm leave / rmi cleanup (the whole dind is destroyed after).
set -u
. /tmp/lib.sh

T() { timeout "$@"; }

wait_task() { # <svc> <cap_s> -> 0 when some task is Running
    i=0
    while [ "$i" -lt "$2" ]; do
        st=$(T 15 docker service ps --format '{{.CurrentState}}' "$1" 2>/dev/null | head -1)
        case "$st" in
        Running*) return 0 ;;
        esac
        i=$((i + 1))
        sleep 1
    done
    return 1
}

nl "=== V2.0 engine identity ==="
docker version --format 'Server: {{.Server.Version}} API: {{.Server.APIVersion}}'
nl "storage driver: $(docker info --format '{{.Driver}}' 2>/dev/null)"

nl "=== V2.1 swarm init (single node, idempotent) ==="
docker swarm init --advertise-addr 127.0.0.1 >/dev/null 2>&1 ||
    nl "(swarm already active - ok)"

nl "=== V2.2 produce a local image with zero registry involvement ==="
mkdir -p /tmp/ctx
cp /bin/busybox /tmp/ctx/busybox
cp /lib/ld-musl-x86_64.so.1 /tmp/ctx/ld-musl-x86_64.so.1
if docker buildx version >/dev/null 2>&1; then
    nl "buildx present -> docker build (FROM scratch, NO attestations)"
    printf 'FROM scratch\nCOPY busybox /busybox\nCOPY ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1\nENTRYPOINT ["/busybox"]\nCMD ["sleep","3000"]\n' >/tmp/ctx/Dockerfile
    docker build --provenance=false --sbom=false -t nightly/app:v1 /tmp/ctx 2>&1 | tail -3
else
    nl "buildx absent -> docker import fallback"
    tar -C /tmp/ctx -c busybox ld-musl-x86_64.so.1 | docker import \
        --change 'ENTRYPOINT ["/busybox"]' --change 'CMD ["sleep","3000"]' \
        - nightly/app:v1
fi
IMG_ID=$(docker image inspect --format '{{.Id}}' nightly/app:v1) || fatal "image inspect"
nl "image id: $IMG_ID"

nl "=== V2.3 standalone sanity run ==="
docker rm -f v2-sanity >/dev/null 2>&1 || true
docker run -d --name v2-sanity nightly/app:v1 >/dev/null || fatal "sanity run"
sleep 1
nl "standalone state: $(docker inspect v2-sanity --format '{{.State.Status}}')"
docker exec v2-sanity /busybox echo alive-from-standalone >/dev/null || fatal "sanity exec"
docker rm -f v2-sanity >/dev/null

DIGEST_ONLY=${IMG_ID#sha256:}

nl "=== V2.4 T1: service create with name@sha256:<imageID> ==="
T 90 docker service create --name nightly-svc --replicas 1 \
    "nightly/app@sha256:$DIGEST_ONLY" >/tmp/v2-t1.out 2>&1
nl "create exit: $?"
head -3 /tmp/v2-t1.out
if wait_task nightly-svc 90; then
    nl "T1 task state: Running"
else
    docker service ps --no-trunc nightly-svc
    fatal "T1 task never Running (90s)"
fi
CTR=$(T 10 docker ps -q -f name=nightly-svc | head -1)
[ -n "$CTR" ] || fatal "T1 task container not found"
docker exec "$CTR" /busybox echo alive-from-digest-task
assert "V2-ASSERT-1 DIGEST_CREATE_TASK_RUNNING_AND_LIVE" $? "exec probe failed"

nl "=== V2.5 T2: service update --force (digest ref must restart without pull) ==="
T 90 docker service update --force nightly-svc >/tmp/v2-t2.out 2>&1
nl "update exit: $?"
head -3 /tmp/v2-t2.out
if wait_task nightly-svc 90; then
    nl "T2 task state: Running"
else
    docker service ps --no-trunc nightly-svc
    fatal "T2 task never Running (90s)"
fi
CTR2=$(T 10 docker ps -q -f name=nightly-svc | head -1)
[ -n "$CTR2" ] || fatal "T2 task container not found"
docker exec "$CTR2" /busybox echo alive-after-force
assert "V2-ASSERT-2 DIGEST_FORCE_TASK_RUNNING_AND_LIVE" $? "exec probe failed"

nl "=== V2.6 T3 control: TAG reference (expect real pull attempt, local fallback) ==="
T 90 docker service create --name nightly-svc-tag --replicas 1 nightly/app:v1 >/tmp/v2-t3.out 2>&1
nl "create exit: $?"
head -3 /tmp/v2-t3.out
if wait_task nightly-svc-tag 90; then
    nl "T3 task state: Running (tag ref fell back to local image)"
else
    nl "WARN T3 task not Running after 90s (pull attempt still logged; host assert decides)"
    docker service ps --no-trunc nightly-svc-tag
fi

for s in nightly-svc nightly-svc-tag; do
    docker service rm "$s" >/dev/null 2>&1 || true
done
finish
