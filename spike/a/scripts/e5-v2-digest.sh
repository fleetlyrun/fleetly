#!/bin/sh
# E5 / V2: local digest reference without a registry (MUST run inside
# docker:29.8.1-dind, privileged). POSIX sh (busybox ash).
# Evidence written to /tmp/e5-report.txt; host reads it and also greps the
# dockerd log (container stdout) for pull attempts.
#
# Fixes 2026-09-17 (second run, see FINDINGS §6):
#  - buildx default build produces a PROVENANCE ATTESTATION MANIFEST LIST;
#    swarm tasks resolving that list digest crashed with
#    "exec /busybox: no such file or directory". Build with
#    --provenance=false --sbom=false so the tag digest == image content.
#  - standalone `docker run` sanity check BEFORE any service create.
#  - busybox `timeout` on every daemon-blocking call (first run hung >10min
#    inside `docker service create` while the task was crash-looping).
#  - swarm init idempotent; `.Driver` moved to `docker info --format`.
set -u

R=/tmp/e5-report.txt
T() { timeout "$@"; }   # readability
: > "$R"
log() { echo "[e5] $*" ; echo "[e5] $*" >> "$R" ; }
say() { echo "$*" | tee -a "$R" ; }

# wait until the first task of a service reaches Running (45s deadline)
wait_task() {
  i=0
  while [ "$i" -lt 45 ]; do
    st=$(T 15 docker service ps --format '{{.Current State}}' "$1" 2>/dev/null | head -1)
    case "$st" in Running*) return 0 ;; esac
    i=$((i+1)); sleep 1
  done
  return 1
}

# dump events in [ts,now] and report pull-related lines
evwin() {
  tag=$1; ts=$2
  now=$(date +%s)
  T 60 docker events --since "$ts" --until "$now" --format "EVT {{.Type}} {{.Action}} {{.Actor.Attributes.name}}" > "/tmp/evt-$tag.txt" 2>/dev/null
  say "--- events [$tag]: $(wc -l < /tmp/evt-$tag.txt) total; pull-related:"
  grep -i -e pull -e image /tmp/evt-$tag.txt | head -15 | tee -a "$R"
  say "pull-attempt events [$tag]: $(grep -i -c pull /tmp/evt-$tag.txt 2>/dev/null || echo 0)"
}

log "=== 0. engine identity ==="
docker version --format 'Server: {{.Server.Version}} API: {{.Server.APIVersion}}' 2>&1 | tee -a "$R"
say "storage driver: $(docker info --format '{{.Driver}}' 2>/dev/null)"

log "=== 1. swarm init (single node, idempotent) ==="
docker swarm init --advertise-addr 127.0.0.1 2>&1 | head -2 | tee -a "$R"
docker swarm init --advertise-addr 127.0.0.1 >/dev/null 2>&1 || say "(swarm already active - ok)"

log "=== 2. produce a local image with zero registry involvement ==="
mkdir -p /tmp/ctx
cp /bin/busybox /tmp/ctx/busybox
# busybox in alpine dind is DYNAMICALLY linked (ELF ET_DYN, needs musl loader);
# FROM scratch without the loader fails "exec /busybox: no such file or
# directory" (first two runs of 2026-09-17 crashed on exactly this).
cp /lib/ld-musl-x86_64.so.1 /tmp/ctx/ld-musl-x86_64.so.1
if docker buildx version >/dev/null 2>&1; then
  log "buildx present -> real docker build (FROM scratch, NO attestations)"
  # NOTE: alpine dind busybox has NO httpd/wget applets (busybox-extras only),
  # so the task just sleeps; liveness = task Running + exec echo probe.
  printf 'FROM scratch\nCOPY busybox /busybox\nCOPY ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1\nENTRYPOINT ["/busybox"]\nCMD ["sleep","3000"]\n' > /tmp/ctx/Dockerfile
  docker build --provenance=false --sbom=false -t spike-a/app:v1 /tmp/ctx 2>&1 | tail -4 | tee -a "$R"
else
  log "buildx absent -> docker import fallback"
  tar -C /tmp/ctx -c busybox ld-musl-x86_64.so.1 | docker import \
    --change 'ENTRYPOINT ["/busybox"]' --change 'CMD ["sleep","3000"]' \
    - spike-a/app:v1 2>&1 | tee -a "$R"
fi

IMG_ID=$(docker image inspect --format '{{.Id}}' spike-a/app:v1)
say "image id:        $IMG_ID"
say "repo digests:    $(docker image inspect --format '{{json .RepoDigests}}' spike-a/app:v1)"

say "--- standalone sanity run (image must work before swarm sees it):"
docker rm -f spike-a-sanity >/dev/null 2>&1
docker run -d --name spike-a-sanity spike-a/app:v1 >/dev/null 2>&1
sleep 1
say "standalone state: $(docker inspect spike-a-sanity --format '{{.State.Status}}')"
say "standalone exec:  $(docker exec spike-a-sanity /busybox echo alive-from-standalone 2>&1 | head -1)"
docker rm -f spike-a-sanity >/dev/null 2>&1

DIGEST_ONLY=${IMG_ID#sha256:}

log "=== 3. T1 digest reference: service create name@sha256:<imageID> ==="
T1=$(date +%s)
T 90 docker service create --name spike-a-svc --replicas 1 \
  "spike-a/app@sha256:$DIGEST_ONLY" > /tmp/t1-create.txt 2>&1
say "create exit:     $?"
head -3 /tmp/t1-create.txt >> "$R"

if wait_task spike-a-svc; then say "T1 task state:   Running"; else say "T1 task state:   NOT-RUNNING (deadline)"; fi
say "--- service ls ---"
T 15 docker service ls | tee -a "$R"
say "--- service ps spike-a-svc --no-trunc ---"
T 15 docker service ps --no-trunc spike-a-svc 2>&1 | head -3 | tee -a "$R"

CTR=$(T 10 docker ps -q -f name=spike-a-svc | head -1)
say "task container:  $CTR"
if [ -n "$CTR" ]; then
  say "--- in-container liveness probe (busybox echo; no httpd applet in dind busybox) ---"
  docker exec "$CTR" /busybox echo alive-from-digest-task 2>&1 | head -2 | tee -a "$R"
  say "exec probe exit: $?"
fi
evwin t1 "$T1"

log "=== 4. T2 service update --force (digest ref must restart without pull) ==="
T2=$(date +%s)
T 90 docker service update --force spike-a-svc > /tmp/t2-update.txt 2>&1
say "update exit:     $?"
head -3 /tmp/t2-update.txt >> "$R"
if wait_task spike-a-svc; then say "T2 task state:   Running"; else say "T2 task state:   NOT-RUNNING (deadline)"; fi
say "--- service ps after --force --no-trunc ---"
T 15 docker service ps --no-trunc spike-a-svc 2>&1 | head -3 | tee -a "$R"
CTR2=$(T 10 docker ps -q -f name=spike-a-svc | head -1)
if [ -n "$CTR2" ]; then
  docker exec "$CTR2" /busybox echo alive-after-force 2>&1 | head -2 | tee -a "$R"
  say "exec probe after force exit: $?"
fi
evwin t2 "$T2"

log "=== 5. T3 control: TAG reference spike-a/app:v1 (expect pull attempt, still runs) ==="
T3=$(date +%s)
T 90 docker service create --name spike-a-svc-tag --replicas 1 spike-a/app:v1 > /tmp/t3-create.txt 2>&1
say "create exit:     $?"
if wait_task spike-a-svc-tag; then say "T3 task state:   Running (tag ref fell back to local)"; else say "T3 task state:   NOT-RUNNING (deadline)"; fi
say "--- service ps spike-a-svc-tag --no-trunc ---"
T 15 docker service ps --no-trunc spike-a-svc-tag 2>&1 | head -3 | tee -a "$R"
evwin t3 "$T3"

log "=== 6. T4 probe: bare image-ID reference (no name component) ==="
T4=$(date +%s)
T 90 docker service create --name spike-a-svc-bare --replicas 1 "sha256:$DIGEST_ONLY" > /tmp/t4-create.txt 2>&1
say "create exit:     $?"
head -3 /tmp/t4-create.txt >> "$R"
if wait_task spike-a-svc-bare; then say "T4 task state:   Running"; else say "T4 task state:   NOT-RUNNING (deadline)"; fi
T 15 docker service ps --no-trunc spike-a-svc-bare 2>&1 | head -4 | tee -a "$R"
evwin t4 "$T4"

log "=== 7. cleanup (inner) ==="
for s in spike-a-svc spike-a-svc-tag spike-a-svc-bare; do docker service rm "$s" >/dev/null 2>&1; done
sleep 4
docker swarm leave --force 2>&1 | tee -a "$R"
docker rmi spike-a/app:v1 2>&1 | tee -a "$R"

log "=== E5 inner script complete ==="
