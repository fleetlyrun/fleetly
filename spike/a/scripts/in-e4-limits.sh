#!/bin/sh
# E4 inner: containerized buildkitd under hard cgroup limits.
# Recreates spike-a-buildkitd with --memory 1g --memory-swap 1g --cpus 1.5,
# runs full cold builds, verifies the engine-side ceiling and OOM state.
set -u
cd /work
DRIVER=/work/spikerailpack
export BUILDKIT_HOST=docker-container://spike-a-buildkitd

echo "===== E4.1 recreate buildkitd with hard limits ====="
docker rm -f spike-a-buildkitd
docker run -d --name spike-a-buildkitd --privileged --network spike-a-net \
  --memory 1g --memory-swap 1g --cpus 1.5 \
  moby/buildkit:v0.32.2
docker exec -i spike-a-buildkitd sh -c 'mkdir -p /etc/buildkit && cat > /etc/buildkit/buildkitd.toml' < /work/scripts/buildkitd.toml
docker restart spike-a-buildkitd >/dev/null
sleep 3
docker exec spike-a-buildkitd buildctl debug info | grep -e Version -e Worker

echo "===== E4.2 ceiling as recorded by the engine ====="
docker inspect spike-a-buildkitd --format 'Memory={{.HostConfig.Memory}} NanoCpus={{.HostConfig.NanoCpus}} Privileged={{.HostConfig.Privileged}}'

build_cold() {
  tag=$1
  docker exec spike-a-buildkitd buildctl prune --all --force >/dev/null 2>&1
  echo "----- cold build spike-a/node-app:$tag start $(date +%H:%M:%S) -----"
  "$DRIVER" build /work/fixtures/node-app --progress plain --name "spike-a/node-app:$tag" \
    --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 2>&1 \
    | grep -e "Successfully built image in" -e "ERROR" -e "error:" | head -5
  echo "----- cold build $tag end $(date +%H:%M:%S) -----"
  docker stats --no-stream --format "{{.Name}}: cpu={{.CPUPerc}} mem={{.MemUsage}}" spike-a-buildkitd
  docker inspect spike-a-buildkitd --format 'OOMKilled={{.State.OOMKilled}} Status={{.State.Status}}'
}

echo "===== E4.3 cold build #1 under limits ====="
build_cold limited1

echo "===== E4.4 cold build #2 under limits (repeatable) ====="
build_cold limited2

echo "===== E4.5 warm rebuild under limits (cache intact across restart) ====="
echo "start $(date +%H:%M:%S)"
"$DRIVER" build /work/fixtures/node-app --progress plain --name spike-a/node-app:limited-warm \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 2>&1 \
  | grep -e "Successfully built image in" -e "CACHED" | head -8
echo "end $(date +%H:%M:%S)"
echo "===== E4-limits done ====="
