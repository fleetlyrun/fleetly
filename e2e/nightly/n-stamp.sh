#!/bin/sh
# e2e/nightly — volume witness stamp helper.
# Runs INSIDE a node dind. Finds the running task container of a service on
# this node and writes/reads the volume stamp through it; with `read-helper`
# a throwaway alpine container mounts the volume so it can be read even when
# no task runs on the node (used while a node is drained / blocked).
# Staged by run.sh at /opt/stamp.sh on every node; needs /opt/probe
# (spike/c probe, stamp mode: read rc 0=hit, 3=miss).
# Derived from spike/c/scripts/in-stamp.sh (Spike C; unchanged logic).
# usage: sh /opt/stamp.sh <svc> <vol> <dir> read
#        sh /opt/stamp.sh <svc> <vol> <dir> write <token>
#        sh /opt/stamp.sh <svc> <vol> <dir> read-helper
SVC=$1
VOL=$2
DIR=$3
OP=$4
TOK=$5
P=/opt/probe
# 台账钉版（docs/runbooks/image-prepull.md #3；e2e/nightly 钉版票）。
ALPINE_IMG='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'
echo "stamp-ms=$($P ts) svc=$SVC vol=$VOL op=$OP"
CTR=$(docker ps --filter "label=com.docker.swarm.service.name=$SVC" --format '{{.ID}}' | head -1)
if [ "$OP" = "read-helper" ]; then
    docker run --rm -v "$VOL:$DIR" -v /opt/probe:/probe "$ALPINE_IMG" /probe stamp -dir "$DIR" read
    rc=$?
    echo "stamp-rc=$rc"
    exit $rc
fi
if [ -z "$CTR" ]; then
    echo "NO-TASK-CONTAINER (service $SVC has no running task on this node)"
    exit 5
fi
echo "task-container=$CTR"
case "$OP" in
write)
    # NOTE: flags must precede the write|read subcommand - Go's flag package
    # stops parsing at the first positional argument (spike/c README #4).
    docker exec "$CTR" /probe stamp -dir "$DIR" -token "$TOK" write
    rc=$?
    echo "stamp-rc=$rc"
    exit $rc
    ;;
read)
    docker exec "$CTR" /probe stamp -dir "$DIR" read
    rc=$?
    echo "stamp-rc=$rc"
    exit $rc
    ;;
*)
    echo "unknown op $OP"
    exit 2
    ;;
esac
