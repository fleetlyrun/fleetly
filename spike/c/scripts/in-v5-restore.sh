#!/bin/sh
# Stage the cold backup into /var/lib/docker/swarm of a FRESH dind (v5m2).
# The running dockerd does not know the directory (it booted without swarm
# state); the host orchestrator then restarts the dind so dockerd boots onto
# the restored state. Called inside spike-c-v5m2 only.
set -e
echo "--- before: /var/lib/docker/swarm exists? ---"
ls -la /var/lib/docker/swarm 2>&1 || true
rm -rf /var/lib/docker/swarm
# docker cp ctr:/var/lib/docker/swarm <target> lands the CONTENTS flat in
# <target> (state.json, certificates/, raft/, worker/) - copy them as swarm/
cp -a /work-src/artifacts/hostbak/swarm-cp /var/lib/docker/swarm
echo "--- restored tree (top level) ---"
ls -la /var/lib/docker/swarm
echo "--- restored key file hashes (must match v5-swarm-hashes-before.txt) ---"
cd /var/lib/docker/swarm
sha256sum state.json certificates/swarm-node.crt certificates/swarm-node.key
sync
echo V5-RESTORE-STAGED
