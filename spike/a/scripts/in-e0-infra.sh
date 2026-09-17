#!/bin/sh
# E0 inner: pinned buildkitd + throwaway registry INSIDE the dind container.
# Called by e0-up.bat via `docker exec spike-a-dind sh /work/scripts/in-e0-infra.sh`.
set -eu
cd /work
echo "===== inner docker ====="
docker version --format 'inner engine: {{.Server.Version}}'

echo "===== registry (pinned registry:2.8.3) ====="
docker rm -f spike-a-buildkitd spike-a-registry 2>/dev/null || true
docker network rm spike-a-net 2>/dev/null || true
docker network create spike-a-net
docker pull registry:2.8.3 >/dev/null
docker run -d --name spike-a-registry --network spike-a-net -p 127.0.0.1:5444:5000 registry:2.8.3

echo "===== buildkitd (pinned moby/buildkit:v0.32.2, privileged) ====="
docker pull moby/buildkit:v0.32.2 >/dev/null
docker run -d --name spike-a-buildkitd --privileged --network spike-a-net moby/buildkit:v0.32.2
# exec+stdin (docker cp host->privileged dind silently drops files; inside
# dind->plain container exec+stdin is used anyway for uniform safety)
docker exec -i spike-a-buildkitd sh -c 'mkdir -p /etc/buildkit && cat > /etc/buildkit/buildkitd.toml' < /work/scripts/buildkitd.toml
docker restart spike-a-buildkitd >/dev/null
sleep 3

echo "===== buildkitd identity ====="
docker exec spike-a-buildkitd buildctl debug info 2>&1 | grep -e "BuildKit:" -e "buildkitd" -e "Worker:" || docker exec spike-a-buildkitd buildctl debug info 2>&1 | head -10

echo "===== registry reachability (from dind netns via published port) ====="
wget -qO- -T 3 http://127.0.0.1:5444/v2/_catalog || echo "registry probe FAILED"
echo
echo "===== in-e0-infra done ====="
