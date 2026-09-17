#!/bin/sh
# Stamp helper executed INSIDE a node dind. Finds the running task container
# of a service on this node and writes/reads the volume witness stamp through
# it (or, with -helper, through a throwaway alpine container so the volume can
# be read even when no task is running on the node).
# usage: in-stamp.sh <svc> <vol> <dir> read
#        in-stamp.sh <svc> <vol> <dir> write <token>
#        in-stamp.sh <svc> <vol> <dir> read-helper   (no task container needed)
SVC=$1; VOL=$2; DIR=$3; OP=$4; TOK=$5
P=/opt/probe
echo "stamp-ms=$($P ts) svc=$SVC vol=$VOL op=$OP"
CTR=$(docker ps --filter "label=com.docker.swarm.service.name=$SVC" --format "{{.ID}}" | head -1)
if [ "$OP" = "read-helper" ]; then
  docker run --rm -v "$VOL:$DIR" -v /opt/probe:/probe alpine:3.20 /probe stamp -dir "$DIR" read
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
    # stops parsing at the first positional argument.
    docker exec "$CTR" /probe stamp -dir "$DIR" -token "$TOK" write
    rc=$?; echo "stamp-rc=$rc"; exit $rc ;;
  read)
    docker exec "$CTR" /probe stamp -dir "$DIR" read
    rc=$?; echo "stamp-rc=$rc"; exit $rc ;;
  *)
    echo "unknown op $OP"; exit 2 ;;
esac
