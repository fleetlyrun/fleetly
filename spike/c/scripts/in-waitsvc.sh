#!/bin/sh
# Wait until a service has >= N running tasks (optionally on a given node),
# then print the final `docker service ps` snapshot. Runs inside a dind.
# usage: in-waitsvc.sh <svc> <want-running> [node-hostname] [cap-s]
SVC=$1; WANT=$2; NODE=$3; CAP=${4:-60}
N=0
i=0
while [ $i -lt "$CAP" ]; do
  if [ -n "$NODE" ]; then
    N=$(docker service ps "$SVC" --format '{{.Node}}|{{.CurrentState}}' 2>/dev/null | grep -c "^$NODE|Running")
  else
    N=$(docker service ps "$SVC" --format '{{.CurrentState}}' 2>/dev/null | grep -c "^Running")
  fi
  if [ "$N" -ge "$WANT" ]; then
    echo "WAITSVC-OK $SVC running=$N want=$WANT after=${i}s"
    docker service ps "$SVC"
    exit 0
  fi
  sleep 1
  i=$((i+1))
done
echo "WAITSVC-TIMEOUT $SVC running=$N want=$WANT after=${CAP}s"
docker service ps "$SVC" 2>&1
exit 1
