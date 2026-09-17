#!/bin/sh
# E2b inner: registry cache round-trip (type=registry cache-to/cache-from).
# Proves the speedup comes from the registry: export -> prune ALL local ->
# cold control build (slow) -> import build (fast/CACHED).
set -eu
cd /work
DRIVER=/work/spikerailpack
export BUILDKIT_HOST=docker-container://spike-a-buildkitd

echo "===== E2b.1 registry catalog before (expect empty) ====="
wget -qO- -T 3 http://127.0.0.1:5444/v2/_catalog || echo "probe failed"
echo

echo "===== E2b.2 build with --cache-to type=registry,mode=max ====="
echo "start $(date +%H:%M:%S)"
"$DRIVER" build /work/fixtures/node-app --progress plain --name spike-a/node-app:v1 \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 \
  --cache-to type=registry,ref=spike-a-registry:5000/spike-a-cache:node,mode=max 2>&1 \
  | grep -e "Successfully built image in" -e "ERROR" -e "error:" | head -10
echo "end $(date +%H:%M:%S)"

echo "===== E2b.3 registry catalog after export ====="
wget -qO- -T 3 http://127.0.0.1:5444/v2/_catalog || echo "probe failed"
echo

echo "===== E2b.4 wipe local buildkit cache entirely ====="
# buildkit v0.32.2 buildctl prune has NO --force (fixed 2026-09-17, was
# silently failing and leaving local cache in place)
docker exec spike-a-buildkitd buildctl prune --all
echo "--- buildctl du after prune (expect no records):"
docker exec spike-a-buildkitd buildctl du

echo "===== E2b.5 control: rebuild WITHOUT --cache-from after prune (cold) ====="
echo "start $(date +%H:%M:%S)"
"$DRIVER" build /work/fixtures/node-app --progress plain --name spike-a/node-app:v1 \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 2>&1 \
  | grep -e "Successfully built image in" -e "ERROR" -e "error:" | head -10
echo "end $(date +%H:%M:%S)"

echo "===== E2b.6 prune again, rebuild WITH --cache-from (expect fast + CACHED) ====="
docker exec spike-a-buildkitd buildctl prune --all
echo "--- buildctl du after prune (expect no records):"
docker exec spike-a-buildkitd buildctl du
echo "start $(date +%H:%M:%S)"
"$DRIVER" build /work/fixtures/node-app --progress plain --name spike-a/node-app:v1 \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 \
  --cache-from type=registry,ref=spike-a-registry:5000/spike-a-cache:node 2>&1 \
  | grep -e "Successfully built image in" -e "CACHED" -e "ERROR" -e "error:" | head -20
echo "end $(date +%H:%M:%S)"
echo "===== E2b done ====="
