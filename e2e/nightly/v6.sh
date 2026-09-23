#!/bin/sh
# e2e/nightly V6 — placement & binding semantics (V6a + V6b drain subset).
# HOST-side orchestrator: drives a dual-dind swarm (mgr + w1 + w2, formed by
# run.sh) purely through `docker exec`. Never runs `docker service ...`
# against the outer engine.
#
# Derived from spike/c/scripts/c3a-run.bat (empty-volume accident),
# c3b-run.bat (pin + kill + restart rebind), c4a-run.bat (drain/active
# roundtrip) and their inner helpers in-waitsvc.sh / in-stamp.sh (staged at
# /opt on each node). Findings: spike/c/README.md sections 3-4.
#
# Asserts (single-node-safe subset excluded; this is the dual-dind form):
#   V6A-ASSERT-1 UNPINNED_TASK_MIGRATED_TO_MGR     (c3-app, no constraint)
#   V6A-ASSERT-2 EMPTY_VOLUME_ACCIDENT             (mgr gets a fresh volume)
#   V6A-ASSERT-3 NEW_NODE_STAMP_MISSING            (stamp rc=3 on mgr)
#   V6A-ASSERT-4 ORIGINAL_DATA_PRESERVED_ON_DEAD_NODE (docker cp from w1)
#   V6A-ASSERT-5 PINNED_STAYS_PENDING_NO_NEWTASK   (c3b-app pinned to w2)
#   V6A-ASSERT-6 AUTO_REBIND_AFTER_RESTART         (w2 restarted, cert identity)
#   V6A-ASSERT-7 SAME_VOLUME_AFTER_ROUNDTRIP       (CreatedAt unchanged)
#   V6A-ASSERT-8 STAMP_INTACT_AFTER_ROUNDTRIP      (same token reads back)
#   V6B-ASSERT-1 DRAIN_BLOCKS_APP                  (drain w2 -> Pending)
#   V6B-ASSERT-2 VOLUME_DATA_ALIVE_DURING_DRAIN    (helper read on w2)
#   V6B-ASSERT-3 AUTO_REBIND_AFTER_ACTIVE          (active -> back on w2)
#   V6B-ASSERT-4 DATA_INTACT_AFTER_ROUNDTRIP
set -u
NL_ROOT=${NL_ROOT:?NL_ROOT must be exported by run.sh}
. "$NL_ROOT/e2e/nightly/lib.sh"

NL_MGR=${NL_MGR:?NL_MGR must be exported by run.sh}
NL_W1=${NL_W1:?NL_W1 must be exported by run.sh}
NL_W2=${NL_W2:?NL_W2 must be exported by run.sh}
NL_ART=${NL_ART:?NL_ART must be exported by run.sh}
# 台账钉版（docs/runbooks/image-prepull.md #3；e2e/nightly 钉版票）。
ALPINE_IMG='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'

m() { docker exec "$NL_MGR" "$@"; }
msh() { docker exec "$NL_MGR" sh -c "$*"; }
w2sh() { docker exec "$NL_W2" sh -c "$*"; }
waitsvc() { # <svc> <want> <node|''> <cap>
    docker exec "$NL_MGR" sh /opt/waitsvc.sh "$1" "$2" "$3" "$4" >/dev/null
}
stamp_w1() { docker exec "$NL_W1" sh /opt/stamp.sh "$@"; }
stamp_w2() { docker exec "$NL_W2" sh /opt/stamp.sh "$@"; }
stamp_m() { docker exec "$NL_MGR" sh /opt/stamp.sh "$@"; }
vol_ts() { # <node-ctr> <vol>  -> CreatedAt of the volume ON that node
    docker exec "$1" docker volume inspect "$2" --format '{{.CreatedAt}}'
}

#######################################
nl "############ V6a-1: C3a empty-volume accident (unpinned volume service) ############"
m docker service rm c3-app >/dev/null 2>&1 || true
sleep 2
# drain-trick: w1 must be the ONLY candidate, so drain BOTH mgr and w2 (w2
# exists for the C3b phase; spike/c only had mgr+w1 and drained mgr alone).
m docker node update --availability drain mgr >/dev/null
m docker node update --availability drain w2 >/dev/null
m docker service create --name c3-app --replicas 1 \
    --mount type=volume,source=c3vol,target=/data \
    --mount type=bind,source=/opt/probe,target=/probe,readonly \
    "$ALPINE_IMG" sleep 31536000 >/dev/null || fatal "create c3-app"
if waitsvc c3-app 1 w1 120; then
    nl "V6A.1 c3-app placed on w1"
else
    m docker service ps c3-app --no-trunc
    fatal "c3-app never Running on w1 (drain-trick placement failed)"
fi
m docker node update --availability active mgr >/dev/null
# w2 stays drained until the migration phase is done: mgr is then the only
# reschedule target, keeping V6A-ASSERT-1 deterministic.

W1_TS=$(vol_ts "$NL_W1" c3vol)
nl "V6A.2 volume c3vol on w1 CreatedAt=$W1_TS"
stamp_w1 c3-app c3vol /data write c3a-initial >/dev/null || fatal "stamp write on w1"
stamp_w1 c3-app c3vol /data read
nl "V6A.3 killing w1 (host docker kill)"
docker kill "$NL_W1" >/dev/null
docker inspect "$NL_W1" --format 'w1 FinishedAt={{.State.FinishedAt}}'

nl "V6A.4 waiting for reschedule onto mgr (down-judgment ~13.5s + reschedule, spike/c C2)"
# CAUTION (spike/c C2): the dead node's task row stays "|w1|Running" for tens
# of seconds after the kill (stale row) — a "Running on ANY node" poll would
# match it instantly. Poll for Running ON MGR specifically (the only active
# node while w2 is still drained).
if waitsvc c3-app 1 mgr 120; then
    :
else
    m docker service ps c3-app --no-trunc
    fatal "c3-app never rescheduled onto mgr after w1 death"
fi
msh "docker service ps c3-app --format '{{.Node}}|{{.CurrentState}}' | grep -q '^mgr|Running'"
assert "V6A-ASSERT-1 UNPINNED_TASK_MIGRATED_TO_MGR" $?

MGR_TS=$(vol_ts "$NL_MGR" c3vol)
nl "volume c3vol on mgr CreatedAt=$MGR_TS (w1 had $W1_TS)"
[ -n "$MGR_TS" ] && [ "$MGR_TS" != "$W1_TS" ]
assert "V6A-ASSERT-2 EMPTY_VOLUME_ACCIDENT" $? "mgr=$MGR_TS w1=$W1_TS (expected different)"

m sh /opt/stamp.sh c3-app c3vol /data read >/dev/null 2>&1
[ $? -eq 3 ]
assert "V6A-ASSERT-3 NEW_NODE_STAMP_MISSING" $? "expected stamp miss (rc=3) on mgr"

# docker cp destination is a HOST path handled by the native docker binary;
# under MSYS_NO_PATHCONV (Git Bash) it must be a Windows path (cygpath is
# absent on Linux CI -> used as-is).
CP_ART="$NL_ART"
if command -v cygpath >/dev/null 2>&1; then
    CP_ART=$(cygpath -w "$NL_ART")
fi
docker cp "$NL_W1:/var/lib/docker/volumes/c3vol/_data/stamp.json" "$CP_ART/c3a-stamp.json" ||
    nl "NOTE docker cp from stopped w1 rc=$?"
grep -q '"token":[[:space:]]*"c3a-initial"' "$NL_ART/c3a-stamp.json"
assert "V6A-ASSERT-4 ORIGINAL_DATA_PRESERVED_ON_DEAD_NODE" $? "cp/grep from stopped w1 failed"
m docker service rm c3-app >/dev/null
m docker node update --availability active w2 >/dev/null
sleep 3

#######################################
nl "############ V6a-2: C3b hard pin (kill + restart rebind, on w2) ############"
m docker service rm c3b-app >/dev/null 2>&1 || true
sleep 2
m docker service create --name c3b-app --replicas 1 \
    --constraint node.labels.fleetly.node-id==w2 \
    --mount type=volume,source=c3bvol,target=/data \
    --mount type=bind,source=/opt/probe,target=/probe,readonly \
    "$ALPINE_IMG" sleep 31536000 >/dev/null || fatal "create c3b-app"
waitsvc c3b-app 1 w2 120 || {
    m docker service ps c3b-app --no-trunc
    fatal "c3b-app never Running on w2"
}
W2_TS=$(vol_ts "$NL_W2" c3bvol)
stamp_w2 c3b-app c3bvol /data write c3b-initial >/dev/null || fatal "stamp write on w2"
stamp_w2 c3b-app c3bvol /data read

nl "V6A.5 killing w2 (pidfile pre-clean per spike/c README 7#2, then SIGKILL)"
docker exec "$NL_W2" rm -f /run/docker/containerd/containerd.pid /run/docker/containerd/containerd.sock /var/run/docker.pid
docker kill "$NL_W2" >/dev/null
docker inspect "$NL_W2" --format 'w2 FinishedAt={{.State.FinishedAt}}'

# Poll until the down-judgment has propagated AND no task runs on the only
# live node (mgr). The dead node's own row may legitimately linger as
# "|w2|Running" (stale row, spike/c C2), so "Running count == 0" is NOT the
# criterion; the criterion is "nothing Running on a live node" + a Pending
# row with an EMPTY node column (scheduled nowhere; spike/c C3B).
DOWN=0
MGRRUN=1
PEND=0
i=0
while [ "$i" -lt 90 ]; do
    m docker node ls --format '{{.Hostname}}={{.Status}}' | grep -q '^w2=Down' && DOWN=1
    MGRRUN=$(msh "docker service ps c3b-app --format '{{.Node}}|{{.CurrentState}}' | grep -c '^mgr|Running'")
    MGRRUN=${MGRRUN:-0}
    PEND=$(msh "docker service ps c3b-app --format '{{.Node}}|{{.CurrentState}}' | grep -c '^|Pending'")
    PEND=${PEND:-0}
    [ "$DOWN" = "1" ] && [ "$MGRRUN" = "0" ] && [ "$PEND" -ge 1 ] && break
    i=$((i + 1))
    sleep 1
done
nl "V6A.6 after kill: w2-down=$DOWN running-on-mgr=$MGRRUN pending-no-node=$PEND after=${i}s"
m docker service ps c3b-app
[ "$DOWN" = "1" ] && [ "$MGRRUN" = "0" ] && [ "$PEND" -ge 1 ]
assert "V6A-ASSERT-5 PINNED_STAYS_PENDING_NOTHING_RUNNING" $? "down=$DOWN mgr-running=$MGRRUN pending=$PEND"

nl "V6A.7 restarting w2 (same container = same swarm certs = same node identity)"
docker start "$NL_W2" >/dev/null
i=0
while ! docker exec "$NL_W2" docker info >/dev/null 2>&1; do
    i=$((i + 2))
    [ "$i" -ge 120 ] && break
    sleep 2
done
[ "$i" -lt 120 ] || fatal "w2 dockerd not ready 120s after start"
READY=""
i=0
while [ "$i" -lt 90 ]; do
    if m docker node ls --format '{{.Hostname}}={{.Status}}' | grep -q '^w2=Ready'; then READY=1; break; fi
    i=$((i + 1))
    sleep 1
done
[ -n "$READY" ] || fatal "w2 never Ready after restart"
if waitsvc c3b-app 1 w2 120; then
    :
else
    m docker service ps c3b-app --no-trunc
    fatal "c3b-app never rebound to w2 after restart"
fi
assert "V6A-ASSERT-6 AUTO_REBIND_AFTER_RESTART" 0

W2_TS2=$(vol_ts "$NL_W2" c3bvol)
[ "$W2_TS2" = "$W2_TS" ]
assert "V6A-ASSERT-7 SAME_VOLUME_AFTER_ROUNDTRIP" $? "before=$W2_TS after=$W2_TS2"

stamp_w2 c3b-app c3bvol /data read >"$NL_ART/c3b-read.txt" 2>&1
grep -q '"token":[[:space:]]*"c3b-initial"' "$NL_ART/c3b-read.txt"
assert "V6A-ASSERT-8 STAMP_INTACT_AFTER_ROUNDTRIP" $?
m docker service rm c3b-app >/dev/null
sleep 3

#######################################
nl "############ V6b: C4a drain / active roundtrip (on w2) ############"
m docker service rm c4-app >/dev/null 2>&1 || true
sleep 2
m docker service create --name c4-app --replicas 1 \
    --constraint node.labels.fleetly.node-id==w2 \
    --mount type=volume,source=c4vol,target=/data \
    --mount type=bind,source=/opt/probe,target=/probe,readonly \
    "$ALPINE_IMG" sleep 31536000 >/dev/null || fatal "create c4-app"
waitsvc c4-app 1 w2 120 || {
    m docker service ps c4-app --no-trunc
    fatal "c4-app never Running on w2"
}
stamp_w2 c4-app c4vol /data write c4b-initial >/dev/null || fatal "stamp write on w2"

m docker node update --availability drain w2 >/dev/null
nl "V6B.1 w2 drained; waiting 20s for block semantics (control-plane action, ~1s)"
sleep 20
RUNNING_N=$(msh "docker service ps c4-app --format '{{.CurrentState}}' | grep -c '^Running'" || true)
RUNNING_N=${RUNNING_N:-0}
PENDING_N=$(msh "docker service ps c4-app --format '{{.CurrentState}}' | grep -c '^Pending'" || true)
PENDING_N=${PENDING_N:-0}
nl "after drain: running=$RUNNING_N pending=$PENDING_N"
m docker service ps c4-app
[ "$RUNNING_N" = "0" ] && [ "$PENDING_N" -ge 1 ]
assert "V6B-ASSERT-1 DRAIN_BLOCKS_APP" $? "running=$RUNNING_N pending=$PENDING_N"

stamp_w2 c4-app c4vol /data read-helper >/dev/null 2>&1
[ $? -eq 0 ]
assert "V6B-ASSERT-2 VOLUME_DATA_ALIVE_DURING_DRAIN" $?

m docker node update --availability active w2 >/dev/null
if waitsvc c4-app 1 w2 120; then
    assert "V6B-ASSERT-3 AUTO_REBIND_AFTER_ACTIVE" 0
else
    m docker service ps c4-app --no-trunc
    assert "V6B-ASSERT-3 AUTO_REBIND_AFTER_ACTIVE" 1 "task never back on w2"
fi
stamp_w2 c4-app c4vol /data read >"$NL_ART/c4-read.txt" 2>&1
grep -q '"token":[[:space:]]*"c4b-initial"' "$NL_ART/c4-read.txt"
assert "V6B-ASSERT-4 DATA_INTACT_AFTER_ROUNDTRIP" $?

m docker service rm c4-app >/dev/null
m docker service rm c3-app >/dev/null 2>&1 || true
finish
