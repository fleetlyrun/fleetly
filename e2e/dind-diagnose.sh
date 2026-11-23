#!/bin/sh
# e2e dind-diagnose（Z1.4-B2b 故障注入腿）：victoria-logs 镜像缺席（受管
# 组件载体恒 RED 的真实事故形态）→ 组件健康 DEGRADED → ListPlatformIncidents
# 在册（component:victorialogs；波及面明细只在平台面）→ apps diagnose：
# 健康应用不出条目（事故不捏造 Finding）→ 补载镜像 → 载体恢复 →
# incidents 出册。
#
# 未覆盖（挂账，2026-11-23 实测立案）：workload 维度的诊断改写（platform 的
# 错永远不读成用户的错）无法在本腿验证——swarm 上 App 域的崩溃循环不产生
# 终态事实（崩溃后被重启策略替换的旧 task DesiredState=shutdown，被当作
# 历史任务滤出观测；workload.container_exited 恒为 0），F_WORKLOAD_CRASH_LOOP
# 在真 swarm 上不触发，拿不到 workload 锚定的条目。该维度由 diagnosis 包单测
# 承载（TestFactsIncidentsAssembly + RewriteByIncident）。
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

log "preloading nginx:1.27 + traefik (+ zot if present locally) into dind"
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

# ---- 故障注入：victoria-logs 镜像缺席（受管组件载体恒 RED 的真实事故
# 形态——traefik scale-0 是故意缩零非事故，Phase 1 立法不出行） ----
log "injecting fault: VL image absent (carrier stays RED)"

# 组件健康 DEGRADED（status 面聚合快照单源——victoria-logs 载体 0/1 恒 RED）。
STATE=$(cli --json status | grep -o '"state": *"[^"]*"' | head -1)
case "$STATE" in
  *DEGRADED*) log "status degraded as expected (VL carrier RED)" ;;
  *) echo "status not degraded with VL RED (state: $STATE)" >&2; exit 1 ;;
esac

# ListPlatformIncidents 在册（component:victorialogs 条目——波及面明细只在平台面）。
INC=$(cli --json platform incidents 2>/dev/null | grep -c "component:victorialogs" || true)
if [ "$INC" -lt 1 ]; then
  echo "platform incidents missing victorialogs entry" >&2
  cli --json platform incidents 2>&1 | head -8 >&2
  exit 1
fi
log "platform incident in registry (victorialogs x$INC lines)"

# ---- 诊断面（硬断言；设计 §2.3）：健康应用在事故期间不出条目——平台事故
# 不捏造 Finding（健康也是答案）。workload 维度的改写见文件头「未覆盖」。
DIAG=$(cli --json apps diagnose --project "$PROJECT_ID" diagapp 2>&1)
if echo "$DIAG" | grep -q '"finding_id"'; then
  echo "healthy app yields findings during a platform incident (findings must not be fabricated):" >&2
  echo "$DIAG" | head -16 >&2
  exit 1
fi
log "healthy app stays finding-free during the incident"

# ---- 出册验证：补载 victoria-logs 镜像 → 载体收敛 running → 事故出册 ----
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


log "ALL GREEN: diagnose e2e (fault injection, degraded status, incident registry, no fabricated findings, recovery)"
