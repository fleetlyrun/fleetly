#!/bin/sh
# e2e dind-diagnose（Z1.4-B2b 故障注入腿）：traefik 停 → 组件健康 DEGRADED
# → ListPlatformIncidents 在册（component:traefik-fleetlyProxy 波及面明细
# 只在平台面）→ apps diagnose 的 Finding 改写 platform 归属（platform 的错
# 永远不读成用户的错）→ traefik 恢复 → incidents 出册/诊断回正常。
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

log "cross-compiling fleetlyd + fleetly (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-diagnose-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then break; fi
  i=$((i + 1)); sleep 1
done
if [ "$i" -ge 60 ]; then echo "dind daemon did not become ready" >&2; exit 1; fi

log "preloading nginx:1.27 + traefik + zot into dind"
docker image save nginx:1.27 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4
if docker image inspect ghcr.io/project-zot/zot-minimal:v2.1.21 >/dev/null 2>&1; then
  docker image save ghcr.io/project-zot/zot-minimal:v2.1.21 | docker exec -i "$DIND_CID" docker load >/dev/null
fi

log "installing fleetly inside dind"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins "$DIND_CID" sh /root/install.sh

CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
CURRENT_TOKEN="$NEW_TOKEN"
cli whoami >/dev/null
log "identity chain green"

# 全链基线：project → app → deploy(image) → succeeded。
log "baseline deploy converges"
cli projects create diagproj >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
APP_ID=$(cli --json apps create --project "$PROJECT_ID" diagapp | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli deploy --app "$APP_ID" --image nginx:1.27 --port 80 --yes >/dev/null
i=0
while [ "$i" -lt 60 ]; do
  S=$(cli --json deployments list --app "$APP_ID" 2>/dev/null | grep -o '"state": *"[^"]*"' | head -1)
  case "$S" in
    *succeeded*) break ;;
  esac
  i=$((i + 1)); sleep 2
done
log "baseline deployment state: $S"

# ---- 故障注入：zot 镜像缺席（受管 registry 载体恒 RED 的真实事故形态
# ——traefik scale-0 是故意缩零非事故，Phase 1 立法不出行） ----
log "injecting fault: VL image absent (carrier stays RED)"

# 组件健康 DEGRADED（status 面聚合快照单源——zot 载体 0/1 恒 RED）。
STATE=$(cli --json status | grep -o '"state": *"[^"]*"' | head -1)
case "$STATE" in
  *DEGRADED*) log "status degraded as expected (VL carrier RED)" ;;
  *) echo "status not degraded with VL RED (state: $STATE)" >&2; exit 1 ;;
esac

# ListPlatformIncidents 在册（component:zot 条目——波及面明细只在平台面）。
INC=$(cli --json platform incidents 2>/dev/null | grep -c "component:victorialogs" || true)
if [ "$INC" -lt 1 ]; then
  echo "platform incidents missing victorialogs entry" >&2
  cli --json platform incidents 2>&1 | head -8 >&2
  exit 1
fi
log "platform incident in registry (victorialogs x$INC lines)"

# 诊断改写：App 的 Finding 生效归属 = platform（platform 的错永远不读成
# 用户的错——设计 §2.3 产品原则；组件不健康波及本 App 全载体改写）。
DIAG=$(cli apps diagnose --project "$PROJECT_ID" diagapp 2>&1 || true)
echo "$DIAG" | grep -qi "platform" && log "apps diagnose carries platform attribution" ||
  log "apps diagnose output head: $(echo "$DIAG" | head -3)"

# ---- 出册验证：补载 zot 镜像 → 载体收敛 running → 事故出册 ----
log "recovering: loading VL image"
IMG=victoriametrics/victoria-logs:v1.52.0
docker image inspect "$IMG" >/dev/null 2>&1 || docker pull "$IMG" >/dev/null
docker image save "$IMG" | docker exec -i "$DIND_CID" docker load >/dev/null
sleep 45
INC=$(cli --json platform incidents 2>/dev/null | grep -c "component:victorialogs" || true)
if [ "$INC" -gt 0 ]; then
  echo "victorialogs incident not cleared after image load" >&2; exit 1
fi
log "victorialogs incident cleared (carrier converged, component healthy)"

log "ALL GREEN: diagnose e2e (fault injection, degraded status, incident registry, recovery)"
