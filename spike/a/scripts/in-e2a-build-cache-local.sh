#!/bin/sh
# E2a + E2c(railpack side) inner: builds, runnability, local layer cache,
# secrets-hash invalidation (tok-a hit / tok-b invalidate / tok-a revert),
# plan-level secret leakage check. Runs inside dind.
# Railpack prints "Successfully built image in X.XXs" per build (timing).
set -eu
cd /work
DRIVER=/work/spikerailpack
export BUILDKIT_HOST=docker-container://spike-a-buildkitd

build_and_note() {
  name=$1; tag=$2; tok=$3
  echo "----- build $name:$tag token=$tok start $(date +%H:%M:%S) -----"
  "$DRIVER" build "/work/fixtures/node-app" --progress plain --name "spike-a/$name:$tag" \
    --env RAILPACK_NODE_VERSION=22.17.0 --env "NPM_TOKEN=$tok" 2>&1 | grep -e "Successfully built image in" -e "CACHED" -e "ERROR" -e "error:" | head -30
  echo "----- build $name:$tag end $(date +%H:%M:%S) -----"
}

echo "===== BUILD-A1 node first build (cold baseline) ====="
build_and_note node-app v1 tok-a-123456

echo "===== BUILD-A2 node rebuild same input (expect CACHED + shorter) ====="
build_and_note node-app v1 tok-a-123456

echo "===== BUILD-B go ====="
echo "----- go build start $(date +%H:%M:%S) -----"
"$DRIVER" build /work/fixtures/go-app --progress plain --name spike-a/go-app:v1 \
  --env RAILPACK_GO_VERSION=1.24.6 2>&1 | grep -e "Successfully built image in" -e "ERROR" -e "error:" | head -20
echo "----- go build end $(date +%H:%M:%S) -----"

echo "===== RUNNABLE proof (HTTP answers) ====="
docker rm -f spike-a-node-run spike-a-go-run 2>/dev/null || true
docker run -d --name spike-a-node-run spike-a/node-app:v1
docker run -d --name spike-a-go-run spike-a/go-app:v1
sleep 2
echo -n "node: "; docker exec spike-a-node-run wget -qO- -T 3 http://127.0.0.1:3000/ 2>&1 | head -1
echo -n "go:   "; docker exec spike-a-go-run   wget -qO- -T 3 http://127.0.0.1:3000/ 2>&1 | head -1
docker rm -f spike-a-node-run spike-a-go-run >/dev/null

echo "===== E2c-railpack secrets-hash: tokA hit / tokB invalidate / tokA revert ====="
build_and_note node-app sec tok-a-123456
build_and_note node-app sec tok-a-123456
build_and_note node-app sec tok-b-98765
build_and_note node-app sec tok-a-123456

echo "===== E2c-railpack plan-level: different secrets -> same plan bytes? ====="
"$DRIVER" prepare /work/fixtures/node-app --hide-pretty-plan \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-a-123456 \
  --plan-out /work/plans/node.plan.tokA.json
"$DRIVER" prepare /work/fixtures/node-app --hide-pretty-plan \
  --env RAILPACK_NODE_VERSION=22.17.0 --env NPM_TOKEN=tok-b-98765 \
  --plan-out /work/plans/node.plan.tokB.json
if cmp -s /work/plans/node.plan.tokA.json /work/plans/node.plan.tokB.json; then
  echo "PLAN_WITH_DIFFERENT_SECRETS_IDENTICAL (hash is computed at BUILD time, not archived)"
else
  echo "PLAN_WITH_DIFFERENT_SECRETS_DIFFER"
fi
if grep -q -e tok-a-123456 -e tok-b-98765 /work/plans/node.plan.tokA.json; then
  echo "SECRET_VALUE_IN_PLAN"
else
  echo "SECRET_VALUE_NOT_IN_PLAN"
fi
echo "===== E2a/E2c-railpack done ====="
