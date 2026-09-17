#!/bin/sh
# E4 evidence inside privileged dind: rootless BuildKit worker probe.
# Runs moby/buildkit rootless variant as a nested container in 29.8.1 dind.
set -u
R=/tmp/e4-rootless-report.txt
: > "$R"
say() { echo "$*" | tee -a "$R"; }

say "=== kernel / userns baseline ==="
uname -a | tee -a "$R"
cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null >> "$R" || echo "no userns sysctl" >> "$R"

say "=== pull + start rootless buildkitd ==="
docker pull moby/buildkit:v0.32.2-rootless >/dev/null 2>&1 && say "pull ok"
docker rm -f spike-a-bk-rl >/dev/null 2>&1
docker run -d --name spike-a-bk-rl --privileged \
  moby/buildkit:v0.32.2-rootless --oci-worker-no-process-sandbox > /dev/null 2>&1
say "run exit: $?"

sleep 3
say "=== buildctl debug info (rootless worker) ==="
docker exec spike-a-bk-rl buildctl debug info 2>&1 | grep -e Version -e worker -e rootless | tee -a "$R"

say "=== tiny scratch build through the rootless worker ==="
mkdir -p /tmp/rlctx
echo spike-a-rootless-evidence > /tmp/rlctx/hello.txt
printf 'FROM scratch\nCOPY hello.txt /hello.txt\n' > /tmp/rlctx/Dockerfile
docker exec spike-a-bk-rl sh -c "rm -rf /tmp/rlctx && mkdir -p /tmp/rlctx" 
docker cp /tmp/rlctx/Dockerfile spike-a-bk-rl:/tmp/rlctx/Dockerfile
docker cp /tmp/rlctx/hello.txt spike-a-bk-rl:/tmp/rlctx/hello.txt
docker exec spike-a-bk-rl buildctl build --frontend dockerfile.v0 \
  --local context=/tmp/rlctx --local dockerfile=/tmp/rlctx \
  --output type=oci > /tmp/rl-out.tar 2>/tmp/rl-build.log
say "build exit: $?"
tail -3 /tmp/rl-build.log >> "$R"

say "=== cleanup ==="
docker rm -f spike-a-bk-rl > /dev/null 2>&1
say "=== e4-rootless complete ==="
