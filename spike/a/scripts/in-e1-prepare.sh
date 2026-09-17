#!/bin/sh
# E1 inner: railpack pinned-version plan generation + reproducibility.
# Runs inside dind. Uses /work/spikerailpack (driver embedding
# railwayapp/railpack v0.39.0 as a library).
set -eu
cd /work
mkdir -p /work/logs /work/plans

echo "===== E1.1 node plan run #1 (pinned RAILPACK_NODE_VERSION=22.17.0) ====="
/work/spikerailpack prepare /work/fixtures/node-app --hide-pretty-plan \
  --env RAILPACK_NODE_VERSION=22.17.0 \
  --plan-out /work/plans/node.plan.json
echo "exit=$?"

echo "===== E1.2 node plan run #2 (same input; byte-compare follows) ====="
/work/spikerailpack prepare /work/fixtures/node-app --hide-pretty-plan \
  --env RAILPACK_NODE_VERSION=22.17.0 \
  --plan-out /work/plans/node.plan.repro.json
echo "exit=$?"

echo "===== E1.3 go plan run #1 (pinned RAILPACK_GO_VERSION=1.24.6) ====="
/work/spikerailpack prepare /work/fixtures/go-app --hide-pretty-plan \
  --env RAILPACK_GO_VERSION=1.24.6 \
  --plan-out /work/plans/go.plan.json
echo "exit=$?"

echo "===== E1.4 go plan run #2 ====="
/work/spikerailpack prepare /work/fixtures/go-app --hide-pretty-plan \
  --env RAILPACK_GO_VERSION=1.24.6 \
  --plan-out /work/plans/go.plan.repro.json
echo "exit=$?"

echo "===== E1.5 drift probe: node plan WITHOUT version pin ====="
/work/spikerailpack prepare /work/fixtures/node-app --hide-pretty-plan \
  --plan-out /work/plans/node.plan.unpinned.json
echo "exit=$?"

echo "===== E1.6 byte-compare ====="
if cmp -s /work/plans/node.plan.json /work/plans/node.plan.repro.json; then
  echo "NODE_PLAN_REPRODUCIBLE sha256=$(sha256sum /work/plans/node.plan.json | cut -d' ' -f1)"
else
  echo "NODE_PLAN_NOT_REPRODUCIBLE"
  diff /work/plans/node.plan.json /work/plans/node.plan.repro.json | head -10
fi
if cmp -s /work/plans/go.plan.json /work/plans/go.plan.repro.json; then
  echo "GO_PLAN_REPRODUCIBLE sha256=$(sha256sum /work/plans/go.plan.json | cut -d' ' -f1)"
else
  echo "GO_PLAN_NOT_REPRODUCIBLE"
  diff /work/plans/go.plan.json /work/plans/go.plan.repro.json | head -10
fi
if cmp -s /work/plans/node.plan.json /work/plans/node.plan.unpinned.json; then
  echo "UNPINNED_IDENTICAL"
else
  echo "UNPINNED_DIFFERS (see diff below)"
  diff /work/plans/node.plan.json /work/plans/node.plan.unpinned.json | head -30
fi
echo "===== E1 done ====="
