#!/bin/sh
# E3 inner: build-time credentials must not end up in final images.
# Checks per image: docker history --no-trunc / image config Env / runtime
# filesystem grep. Includes the anti-pattern contrast image (secret as ARG).
set -u
cd /work
TOKA=tok-a-123456
TOKB=tok-b-98765
ARGVAL=supersecret-arg-leak

echo "===== E3.1 anti-pattern control: ARG-carried secret lands in history ====="
docker exec spike-a-buildkitd buildctl build --frontend dockerfile.v0 \
  --local context=/tmp/ctx-raw --local dockerfile=/tmp/ctx-raw \
  --opt "filename=Dockerfile.leaky" --opt "build-arg:LEAKY=$ARGVAL" \
  --output type=docker,name=spike-a/raw:leaky \
  > /work/artifacts/e3-leaky.tar 2> /work/artifacts/e3-leaky.errlog
echo "buildctl exit=$?"
docker load -i /work/artifacts/e3-leaky.tar >/dev/null
if docker history --no-trunc spike-a/raw:leaky | grep -q "$ARGVAL"; then
  echo "LEAK_CONFIRMED_IN_HISTORY (anti-pattern proven: ARG value is in history)"
else
  echo "NO_LEAK_IN_HISTORY (unexpected)"
fi

echo "===== E3.2 secret-mounted images: history must be clean ====="
for img in spike-a/raw:vB spike-a/raw:hB spike-a/node-app:sec spike-a/go-app:v1; do
  if docker history --no-trunc "$img" | grep -q -e "$TOKA" -e "$TOKB"; then
    echo "$img HISTORY_LEAK"
  else
    echo "$img HISTORY_CLEAN"
  fi
done
echo "--- hash stamps in hB history are by-design (sha256 prefixes only, never values):"
docker history --no-trunc spike-a/raw:hB | grep -e NPM_TOKEN_HASH || true

echo "===== E3.3 image config Env must be clean ====="
for img in spike-a/raw:vB spike-a/raw:hB spike-a/node-app:sec spike-a/go-app:v1; do
  envs=$(docker image inspect "$img" --format '{{json .Config.Env}}')
  echo "$img Config.Env: $envs"
  if echo "$envs" | grep -q -e "$TOKA" -e "$TOKB"; then
    echo "$img ENV_LEAK"
  else
    echo "$img ENV_CLEAN"
  fi
done

echo "===== E3.4 runtime filesystem grep (what actually ships) ====="
# NOTE(2026-09-17): `grep -r / ...` over the whole rootfs HANGS on /proc
# (observed: container stuck >2min even on a 12MB alpine rootfs). Scan real
# filesystem locations only; details in in-e3-fscheck-fix.sh / FINDINGS.
for img in spike-a/raw:vB spike-a/raw:hB spike-a/node-app:sec; do
  docker rm -f spike-a-fsprobe >/dev/null 2>&1
  docker run -d --name spike-a-fsprobe --entrypoint sleep "$img" 300 >/dev/null
  if docker exec spike-a-fsprobe sh -c "grep -r -l -e $TOKA -e $TOKB /app /work /root /home /etc /tmp /usr /var /opt 2>/dev/null | head -3"; then
    echo "$img FS_LEAK"
  else
    echo "$img FS_CLEAN (grep exit 1 = no matches)"
  fi
  docker rm -f spike-a-fsprobe >/dev/null
done
echo "--- /run/secrets must not exist in the final image (secret mounts are"
echo "--- build-time only):"
docker rm -f spike-a-fsprobe2 >/dev/null 2>&1
docker run -d --name spike-a-fsprobe2 --entrypoint sleep spike-a/raw:hB 300 >/dev/null
docker exec spike-a-fsprobe2 sh -c "ls -la /run/secrets 2>&1; ls /work"
docker rm -f spike-a-fsprobe2 >/dev/null

echo "===== E3.5 archived railpack plans must not contain secret values ====="
if grep -q -e "$TOKA" -e "$TOKB" /work/plans/*.json 2>/dev/null; then
  echo "PLAN_LEAK"
else
  echo "PLANS_CLEAN"
fi
echo "===== E3 done ====="
