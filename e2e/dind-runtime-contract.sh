#!/bin/sh
# e2e dind-runtime-contract（Z1.4-B3b）：runtimecontract 清单对真 swarm 执行
# ——dind 起 daemon → Go 执行器连 docker socket 构造 swarm provider →
# internal/testsupport/runtimecontract.RunE 全清单（Ensure/Remove 幂等、
# scale-only 零替换、代戳恒同移）。两侧同名对账守卫（runtimecontractguard）
# 钉 Fake 侧测试与清单锚零缺席。
#
# 前置：本机 docker 可用；经 mise 任务或直接 sh 执行。
set -eu
export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
DIND_CID=""

cleanup() {
  if [ -n "$DIND_CID" ]; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

log "cross-compiling runtime-contract executor (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/runtimecontract" ./e2e/runtimecontract

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-rtcontract-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then break; fi
  i=$((i + 1)); sleep 1
done
if [ "$i" -ge 60 ]; then echo "dind daemon did not become ready" >&2; exit 1; fi

log "preloading alpine:3 into dind"
docker image save alpine:3 | docker exec -i "$DIND_CID" docker load >/dev/null

log "swarm init inside dind"
docker exec "$DIND_CID" docker swarm init --advertise-addr 172.17.0.2 >/dev/null 2>&1 || \
docker exec "$DIND_CID" docker swarm init >/dev/null 2>&1 || true

log "running runtime contract against real swarm"
docker cp "$WORKDIR/bins/runtimecontract" "$DIND_CID":/root/runtimecontract
docker exec "$DIND_CID" /root/runtimecontract

log "ALL GREEN: runtime contract against real swarm"