#!/bin/sh
# E2c inner (raw BuildKit / Dockerfile fallback path): the secrets accident
# and its mitigation, driven with buildctl against spike-a-buildkitd.
# No set -e: every buildctl exit code is reported explicitly.
set -u
cd /work
mkdir -p /work/artifacts

echo "===== E2c.0 stage build context inside buildkitd ====="
docker exec spike-a-buildkitd sh -c 'rm -rf /tmp/ctx-raw && mkdir -p /tmp/ctx-raw'
for f in Dockerfile.accident Dockerfile.hash Dockerfile.leaky payload.txt tokenA.txt tokenB.txt; do
  docker exec -i spike-a-buildkitd sh -c "cat > /tmp/ctx-raw/$f" < "/work/scripts/raw-ctx/$f"
done
docker exec spike-a-buildkitd sha256sum /tmp/ctx-raw/payload.txt

# run_bc <dockerfile> <tokenfile> <hash-or-""> <outname> <logprefix>
# builds and docker-loads image spike-a/raw:<outname>
run_bc() {
  df=$1; tok=$2; hash=$3; out=$4; logp=$5
  if [ -n "$hash" ]; then
    docker exec spike-a-buildkitd buildctl build --frontend dockerfile.v0 \
      --local context=/tmp/ctx-raw --local dockerfile=/tmp/ctx-raw \
      --opt "filename=$df" --opt "build-arg:NPM_TOKEN_HASH=$hash" \
      --secret id=npm_token,src="/tmp/ctx-raw/$tok" \
      --output "type=docker,name=spike-a/raw:$out" \
      > "/work/artifacts/$logp.tar" 2> "/work/artifacts/$logp.errlog"
  else
    docker exec spike-a-buildkitd buildctl build --frontend dockerfile.v0 \
      --local context=/tmp/ctx-raw --local dockerfile=/tmp/ctx-raw \
      --opt "filename=$df" \
      --secret id=npm_token,src="/tmp/ctx-raw/$tok" \
      --output "type=docker,name=spike-a/raw:$out" \
      > "/work/artifacts/$logp.tar" 2> "/work/artifacts/$logp.errlog"
  fi
  rc=$?
  echo "buildctl exit=$rc (image spike-a/raw:$out)"
  tail -5 "/work/artifacts/$logp.errlog"
  echo "--- cached/executed lines:"
  grep -e "CACHED" -e "executing" "/work/artifacts/$logp.errlog" | head -8 || true
  return "$rc"
}

echo "===== E2c.1 accident build #1 with token A (cold; sleep 3 executes) ====="
echo "start $(date +%H:%M:%S)"
run_bc Dockerfile.accident tokenA.txt "" vA e2c-acc1 || exit 1
echo "end $(date +%H:%M:%S)"
docker load -i /work/artifacts/e2c-acc1.tar >/dev/null
echo "manifest inside vA (fingerprint of TOKEN A):"
docker run --rm spike-a/raw:vA

echo "===== E2c.2 accident build #2 with token B (expect RUN CACHED = stale) ====="
echo "start $(date +%H:%M:%S)"
run_bc Dockerfile.accident tokenB.txt "" vB e2c-acc2 || exit 1
echo "end $(date +%H:%M:%S)"
docker load -i /work/artifacts/e2c-acc2.tar >/dev/null
echo "manifest inside vB built with TOKEN B (expect fingerprint OF TOKEN A = stale):"
docker run --rm spike-a/raw:vB

echo "===== E2c.3 mitigation: fold sha256(secret) into ARG stamp ====="
HASH_A=$(sha256sum /work/scripts/raw-ctx/tokenA.txt | cut -c1-16)
HASH_B=$(sha256sum /work/scripts/raw-ctx/tokenB.txt | cut -c1-16)
echo "HASH_A=$HASH_A HASH_B=$HASH_B"

run_bc Dockerfile.hash tokenA.txt "$HASH_A" hA e2c-hashA1 || exit 1
docker load -i /work/artifacts/e2c-hashA1.tar >/dev/null
echo "manifest inside hA (fingerprint of TOKEN A):"
docker run --rm spike-a/raw:hA

echo "--- rebuild hA identical input (expect CACHED) ---"
run_bc Dockerfile.hash tokenA.txt "$HASH_A" hA2 e2c-hashA2 || exit 1

echo "--- build with HASH_B + token B (expect EXECUTED, invalidated) ---"
echo "start $(date +%H:%M:%S)"
run_bc Dockerfile.hash tokenB.txt "$HASH_B" hB e2c-hashB || exit 1
echo "end $(date +%H:%M:%S)"
docker load -i /work/artifacts/e2c-hashB.tar >/dev/null
echo "manifest inside hB built with TOKEN B + hash stamp (expect fingerprint OF TOKEN B):"
docker run --rm spike-a/raw:hB
echo "===== E2c-raw done ====="
