#!/bin/sh
# e2e dind upgrade（P1，docs/design/2026-10-03-optimization-proposals.md）：
# 升级矩阵 CI 的单配对形态——HEAD~1（现役版）→ HEAD（新版）fleetlyd 升级，
# 用户负载零扰动断言（ADR-0015 验收的 CI 化；staging 2026-10-03 pgvector
# WAL 损坏事故的系统性防线，stop-first 根修 3b3dd85 的回归锚）。
#
# 三件代表性负载：
#   ①web + sslip Route（受管 traefik）——升级全程探针断言请求零失败
#     （存量路由不依赖控制面活着，架构 §8 降级矩阵 Edge 行的实证）；
#   ②第二 App 无 Route——断言平台零重启用户 Workload（task 行零新增）；
#   ③受管 postgres Database——断言零滚动零硬杀（task 恰 1 Running）+
#     pg_isready 活体（挂卷 stop-first 的数据面回归锚）。
#
# 旧版来源：HEAD~1——仓尚无 git tag（Releases 通道随首 tag 批次生效），
# 连续验证"上一版→本版"；tag 通道成型后切 latest-tag→HEAD 配对。
# 升级动作 = 真实用户路径：SIGTERM 优雅退出（30s 排水窗）→ 二进制替换 →
# 起新版（启动自动 goose 前滚 + Managed Provider reconcile）。Platform
# Backup 前置与失败回滚路径属 F2.3 升级工具批，本脚本不含（升上来单向）。
#
# 前置：本机 docker 可用且可拉取 nginx:1.27 / traefik:v3.5 /
# traefik/whoami:v1.10 / postgres:17-bookworm（宿侧 pull 后 save|load 注入
# dind，dind 内离线确定性不赌网络——与既有三脚本同款）。
set -eu

# Git-Bash（MSYS）会把以 / 开头的容器侧路径参数转译成本机路径（实证坑，
# CI Linux 无此变量天然 no-op）。
export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
DIND_CID=""
OLDWT="$WORKDIR/old-src"

cleanup() {
  if [ -n "$DIND_CID" ]; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  fi
  git worktree remove --force "$OLDWT" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

# 1. 编译两代二进制：新版 = 当前工作树；旧版 = HEAD~1 的临时 worktree
#    （worktree 不动当前工作树；go build -C 进旧树编译）。
log "cross-compiling NEW binaries at HEAD (linux/amd64)"
mkdir -p "$WORKDIR/bins-new" "$WORKDIR/bins-old"
for b in fleetlyd fleetly; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins-new/$b" "./cmd/$b"
done
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins-new/h2cclient" ./e2e/h2cclient

log "cross-compiling OLD binaries at HEAD~1 via worktree"
git worktree add --detach "$OLDWT" HEAD~1 >/dev/null 2>&1
for b in fleetlyd fleetly; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C "$OLDWT" -o "$WORKDIR/bins-old/$b" "./cmd/$b"
done
log "binaries ready: old=$(git -C "$OLDWT" rev-parse --short HEAD) new=$(git rev-parse --short HEAD)"

# 2. 起 dind（privileged；29 线与生产对齐）+ 预载镜像。
log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-upgrade-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  echo "dind daemon did not become ready" >&2
  exit 1
fi

# dind 容器 IP：受管 traefik 从 swarm 网络经 gwbridge 回连控制面 :9082 用
# （与 dind-h2c-route.sh 同款）；探针 Host 头与 sslip route 也由它拼出。
DIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
if [ -z "$DIND_IP" ]; then
  echo "could not resolve dind container IP" >&2
  exit 1
fi
log "dind ip: $DIND_IP"

log "preloading images (nginx / traefik / whoami / postgres)"
for img in nginx:1.27 traefik:v3.5 traefik/whoami:v1.10 postgres:17-bookworm; do
  docker image inspect "$img" >/dev/null 2>&1 || docker pull "$img" >/dev/null
  docker image save "$img" | docker exec -i "$DIND_CID" docker load >/dev/null
done
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

# 3. 旧版一行安装（install.sh bin-dir 模式 + Edge 配置端点直达 fleetlyd：
# traefik 经 HTTP provider 拉配置，见 dind-h2c-route.sh 先例）。
log "running install.sh inside dind with OLD binaries"
docker cp "$WORKDIR/bins-old" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins \
  -e FLEETLY_EDGE_CONFIG_ENDPOINT="http://$DIND_IP:9082/edge/config" \
  "$DIND_CID" sh /root/install.sh

# 4. 身份链（与 smoke 同款：bootstrap → init 铸 CLI token → 默认吊销）。
log "identity chain via fleetly init (old cli)"
CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
if [ -z "$CURRENT_TOKEN" ]; then
  echo "bootstrap token file missing or empty" >&2
  exit 1
fi
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR="127.0.0.1:9080" "$DIND_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$NEW_TOKEN" ]; then
  echo "fleetly init did not mint a CLI token" >&2
  exit 1
fi
CURRENT_TOKEN="$NEW_TOKEN"

cli() {
  docker exec -e FLEETLY_ADDR="127.0.0.1:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}
cli whoami >/dev/null
log "identity chain green"

# 5. 负载三件。①quickstart：web + sslip Route + 受管 traefik 挂项目网。
log "deploying loadout 1: quickstart web + sslip route (managed traefik)"
cli quickstart --image traefik/whoami:v1.10 --port 80 >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
WEB_APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
log "project=$PROJECT_ID web-app=$WEB_APP_ID"

wait_deployment() {
  app="$1"; want="$2"; i=0; state=""
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$app" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$app" >&2 || true
        echo "deployment reached $state before $want" >&2
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  echo "timed out waiting for $want (last=$state)" >&2
  return 1
}

wait_database() {
  want="$1"; i=0; status=""
  while [ "$i" -lt 180 ]; do
    status=$(cli --json databases list --project "$PROJECT_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$status" = "$want" ]; then
      return 0
    fi
    case "$status" in
      failed)
        cli --json databases list --project "$PROJECT_ID" >&2 || true
        echo "database reached failed before $want" >&2
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  echo "timed out waiting for database $want (last=$status)" >&2
  return 1
}

# ②第二 App（无 Route，直接镜像部署）。
log "deploying loadout 2: second app (no route, nginx)"
cli apps create --project "$PROJECT_ID" worker >/dev/null
WORKER_APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | tail -1)
cli --json deploy --app "$WORKER_APP_ID" --image nginx:1.27 >/dev/null
wait_deployment "$WORKER_APP_ID" succeeded

# ③受管 postgres Database（挂卷数据面——stop-first 回归锚的本体）。
log "deploying loadout 3: managed postgres database"
cli databases create --project "$PROJECT_ID" --engine postgres pg-main >/dev/null
wait_database running
DB_HOST=$(cli --json databases list --project "$PROJECT_ID" | sed -n 's/.*"host": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$DB_HOST" ]; then
  echo "could not read database host" >&2
  exit 1
fi
log "database host: $DB_HOST"

# 6. 基线：三件负载的载体 task 行数（升级后必须零新增——零重启/零滚动）。
#    载体选择走归属 label（fleetly.ns.*）而非名字：App 域服务名公式
#    fleetly-<team>-<prj>-<app>-<proc> 超长时截断+哈希（translate.go），不保
#    证含完整平台 ID——label 值才是全等锚（值与 ID 同为 sanitize 小写形态）。
svc_of() {
  docker exec "$DIND_CID" docker service ls --filter "label=$1" --format '{{.Name}}' | head -1
}
lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
WEB_SVC=$(svc_of "fleetly.ns.app=$(lc "$WEB_APP_ID")")
WORKER_SVC=$(svc_of "fleetly.ns.app=$(lc "$WORKER_APP_ID")")
DB_SVC=$(svc_of "fleetly.ns.database=$(lc "${DB_HOST#db-}")")
if [ -z "$WEB_SVC" ] || [ -z "$WORKER_SVC" ] || [ -z "$DB_SVC" ]; then
  echo "carrier not found (web=$WEB_SVC worker=$WORKER_SVC db=$DB_SVC)" >&2
  exit 1
fi
task_count() {
  docker exec "$DIND_CID" docker service ps "$1" --format '{{.CurrentState}}' | grep -c . || true
}
WEB_BASE=$(task_count "$WEB_SVC")
WORKER_BASE=$(task_count "$WORKER_SVC")
DB_BASE=$(task_count "$DB_SVC")
log "task baselines: web=$WEB_BASE worker=$WORKER_BASE db=$DB_BASE"

# 7. Route 探针（零失败预算）。h2cclient -host 设 Host 头直连 IP，零 DNS
#    依赖（dind-h2c-route.sh 同款）；STATUS 行出 stdout、body 落文件。
PROBE_FAILS=0
probe() {
  code=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "demo.$DIND_IP.sslip.io" \
    -o /tmp/probe-body "http://$DIND_IP/" \
    | sed -n 's/^STATUS \([0-9]*\).*/\1/p')
  if [ "$code" != "200" ]; then
    PROBE_FAILS=$((PROBE_FAILS + 1))
    echo "probe failed with status: $code" >&2
  fi
}
# 路由就绪窗（dind-h2c-route.sh 同款）：quickstart 返回 ≠ traefik 已发布
#——就绪重试不进零失败预算（预算只覆盖升级序列本身）。
probe_ready() {
  code=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "demo.$DIND_IP.sslip.io" \
    -o /tmp/probe-body "http://$DIND_IP/" \
    | sed -n 's/^STATUS \([0-9]*\).*/\1/p')
  [ "$code" = "200" ]
}
i=0
until probe_ready; do
  i=$((i + 1))
  if [ "$i" -ge 45 ]; then
    echo "route did not become healthy before upgrade (90s)" >&2
    exit 1
  fi
  sleep 2
done
probe || true
if [ "$PROBE_FAILS" -ne 0 ]; then
  echo "route not healthy before upgrade; aborting" >&2
  exit 1
fi
log "route probe green pre-upgrade"

# 8. 升级序列（真实用户路径）。SIGTERM → 排水窗内退出 → 替换二进制 →
#    起新版 → 等 ready。全程探针持续：控制面两代交替，路由不得断。
log "UPGRADE: SIGTERM old fleetlyd (drain window)"
docker exec "$DIND_CID" pkill -TERM fleetlyd || true
i=0
while [ "$i" -lt 80 ]; do
  if ! docker exec "$DIND_CID" pgrep fleetlyd >/dev/null 2>&1; then
    break
  fi
  probe || true
  i=$((i + 1))
  sleep 0.5
done
if [ "$i" -ge 80 ]; then
  echo "old fleetlyd did not exit within the drain grace" >&2
  exit 1
fi
log "old fleetlyd drained; swapping binaries"
probe || true

docker cp "$WORKDIR/bins-new/fleetlyd" "$DIND_CID":/usr/local/bin/fleetlyd
docker cp "$WORKDIR/bins-new/fleetly" "$DIND_CID":/usr/local/bin/fleetly
docker cp "$WORKDIR/bins-new/h2cclient" "$DIND_CID":/root/bins/h2cclient

log "UPGRADE: starting NEW fleetlyd (goose rollforward + managed reconcile)"
docker exec -e FLEETLY_EDGE_CONFIG_ENDPOINT="http://$DIND_IP:9082/edge/config" "$DIND_CID" sh -c \
  'setsid env FLEETLY_DATA_ROOT=/var/lib/fleetly /usr/local/bin/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &'

i=0
while [ "$i" -lt 60 ]; do
  if cli whoami >/dev/null 2>&1; then
    break
  fi
  probe || true
  i=$((i + 1))
  sleep 2
done
if [ "$i" -ge 60 ]; then
  echo "new fleetlyd did not become ready" >&2
  docker exec "$DIND_CID" tail -50 /var/log/fleetlyd.log >&2 || true
  exit 1
fi
probe || true
log "new fleetlyd ready; probe failures across upgrade: $PROBE_FAILS"
if [ "$PROBE_FAILS" -ne 0 ]; then
  echo "route requests failed during upgrade (ADR-0015 violation)" >&2
  exit 1
fi
UPGRADE_DONE_AT=$(date +%s) # 零 drift 断言窗起点

# 9. 零扰动断言。
log "asserting zero disturbance"
if [ "$(task_count "$WEB_SVC")" -ne "$WEB_BASE" ]; then
  echo "web workload restarted during upgrade: $(task_count "$WEB_SVC") tasks (baseline $WEB_BASE)" >&2
  exit 1
fi
if [ "$(task_count "$WORKER_SVC")" -ne "$WORKER_BASE" ]; then
  echo "worker workload restarted during upgrade: $(task_count "$WORKER_SVC") tasks (baseline $WORKER_BASE)" >&2
  exit 1
fi
if [ "$(task_count "$DB_SVC")" -ne "$DB_BASE" ]; then
  echo "database carrier rolled or restarted during upgrade: $(task_count "$DB_SVC") tasks (baseline $DB_BASE)" >&2
  docker exec "$DIND_CID" docker service ps "$DB_SVC" >&2 || true
  exit 1
fi
log "zero restart / zero roll across all three workloads"

wait_database running
DB_CID=$(docker exec "$DIND_CID" docker ps -q -f "name=$DB_SVC")
if [ -z "$DB_CID" ] || ! docker exec "$DIND_CID" docker exec "$DB_CID" pg_isready -h 127.0.0.1 >/dev/null 2>&1; then
  echo "postgres not accepting connections after upgrade" >&2
  exit 1
fi
log "postgres live and accepting connections (pg_isready)"

# 10. 升级后稳态：managed reconcile 不误触（≥2 拍 drift 扫描周期内零
#     workload.drift_detected——staging identical-update 自激环的 CI 防线）。
log "zero-drift assertion over post-upgrade window (>= 2 scan beats)"
i=0
while :; do
  ELAPSED=$(( $(date +%s) - ${UPGRADE_DONE_AT:-$(date +%s)} ))
  DRIFT_EVENTS=$(cli --json events list --limit 200 | grep -c '"name": *"workload.drift_detected"') || true
  if [ -z "$DRIFT_EVENTS" ]; then
    echo "events list read failed (empty count)" >&2
    exit 1
  fi
  if [ "$DRIFT_EVENTS" -ne 0 ]; then
    echo "drift detected after upgrade: $DRIFT_EVENTS event(s)" >&2
    cli --json events list --limit 200 | grep -B 4 -A 2 '"workload.drift_detected"' >&2 || true
    exit 1
  fi
  if [ "$ELAPSED" -ge 70 ]; then
    break
  fi
  if [ "$i" -ge 24 ]; then
    echo "zero-drift window did not elapse in time (elapsed=${ELAPSED}s)" >&2
    exit 1
  fi
  i=$((i + 1))
  sleep 5
done
log "zero workload.drift_detected after upgrade"

log "UPGRADE PASSED"
