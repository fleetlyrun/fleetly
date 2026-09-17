#!/bin/sh
# E2a runnable-proof redo: the first attempt used `docker exec <ctr> wget`,
# but railpack runtime images ship no wget/shell utils. Probe from the dind
# host netns via container IP instead. Evidence -> /work/logs/in-e2a-runnable.log
set -u
{
  echo "===== RUNNABLE proof redo (dind-side probe via container IP) ====="
  docker rm -f spike-a-node-run spike-a-go-run 2>/dev/null
  docker run -d --name spike-a-node-run spike-a/node-app:v1
  docker run -d --name spike-a-go-run spike-a/go-app:v1
  sleep 2
  NIP=$(docker inspect spike-a-node-run --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
  GIP=$(docker inspect spike-a-go-run --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
  echo "node container ip=$NIP  go container ip=$GIP"
  echo -n "node / -> "
  wget -qO- -T 3 "http://$NIP:3000/"
  echo -n "go / -> "
  wget -qO- -T 3 "http://$GIP:3000/"
  echo "--- why first attempt failed (runtime image has no wget):"
  docker run --rm --entrypoint sh spike-a/node-app:v1 -c 'command -v wget || echo NO_WGET' 2>&1 | head -2
  docker rm -f spike-a-node-run spike-a-go-run >/dev/null
  echo "===== runnable redo done ====="
} > /work/logs/in-e2a-runnable.log 2>&1
cat /work/logs/in-e2a-runnable.log
