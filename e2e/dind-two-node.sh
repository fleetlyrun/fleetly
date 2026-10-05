#!/bin/sh
# e2e dind 双节点（F0.20 多节点就绪）：两个 dind 容器组真 swarm——
# manager 起完整平台（install.sh + init），worker 零平台安装物，经
# `fleetly nodes enroll` 输出的加入材料 docker swarm join。
# 验收断言：① join 后双节点在场；② 同 App 双节点部署（无卷 process 的
# 副本分落两节点）；③ 卷钉住（有卷 process 的副本全部落在钉住节点 +
# 调度约束以平台节点 ID 为锚）。
#
# 前置：本机 docker 可用且持有 nginx:1.27。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
MGR_CID=""
WRK_CID=""

cleanup() {
  [ -n "$WRK_CID" ] && docker rm -f "$WRK_CID" >/dev/null 2>&1 || true
  [ -n "$MGR_CID" ] && docker rm -f "$MGR_CID" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

wait_docker() {
  cid="$1"; i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$cid" docker info >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "dind daemon did not become ready ($cid)" >&2
  return 1
}

log "cross-compiling fleetlyd + fleetly (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly

log "starting manager + worker dind containers"
# --cgroupns=host（14abbae 只给 smoke 加过的同款修正，此处补齐两节点面）：
# 嵌套容器的 cgroup 在宿主层级可见——cadvisor（受管指标采集端，全局任务
# 两节点各一）读 /sys 才能看到 dind 内容器样本（私有 cgroupns 下只见
# root/system 条目，容器序列缺席——CI 实证 2026-10-04）。两节点 metrics
# 断言（node 标签双值）依赖此面。
MGR_CID=$(docker run -d --privileged --name fleetly-e2e-mgr-"$$" --cgroupns=host -e DOCKER_TLS_CERTDIR= docker:29-dind)
WRK_CID=$(docker run -d --privileged --name fleetly-e2e-wrk-"$$" --cgroupns=host -e DOCKER_TLS_CERTDIR= docker:29-dind)
wait_docker "$MGR_CID"
wait_docker "$WRK_CID"
MGR_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$MGR_CID")
log "manager ip: $MGR_IP"

log "preloading nginx into both nodes"
docker image save nginx:1.27 | docker exec -i "$MGR_CID" docker load >/dev/null
docker image save nginx:1.27 | docker exec -i "$WRK_CID" docker load >/dev/null

log "installing fleetly on the manager (worker stays zero-install)"
docker cp "$WORKDIR/bins" "$MGR_CID":/root/bins
docker cp install.sh "$MGR_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins "$MGR_CID" sh /root/install.sh

CURRENT_TOKEN=$(docker exec "$MGR_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$MGR_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$NEW_TOKEN" ] || { echo "fleetly init did not mint a token" >&2; exit 1; }
CURRENT_TOKEN="$NEW_TOKEN"

cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$MGR_CID" fleetly "$@"
}
cli whoami >/dev/null

# 双节点 join：enroll 输出的材料是可直接执行的 docker swarm join 命令。
log "joining the worker via 'fleetly nodes enroll' material"
JOIN_CMD=$(cli --json nodes enroll | sed -n 's/.*"join_command": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$JOIN_CMD" ]; then
  # 人类形态兜底解析（--json 字段名以实际输出为准）。
  JOIN_CMD=$(cli nodes enroll | grep -o 'docker swarm join --token [^ ]* [^ ]*')
fi
[ -n "$JOIN_CMD" ] || { echo "nodes enroll produced no join command" >&2; exit 1; }
log "join: $JOIN_CMD"
docker exec "$WRK_CID" $JOIN_CMD

i=0
while [ "$i" -lt 30 ]; do
  n=$(docker exec "$MGR_CID" docker node ls --format '{{.Status}}' | grep -c Ready || true)
  [ "$n" -ge 2 ] && break
  i=$((i + 1))
  sleep 2
done
if [ "${n:-0}" -lt 2 ]; then
  echo "manager never saw the worker as Ready" >&2
  docker exec "$MGR_CID" docker node ls >&2 || true
  exit 1
fi
log "two nodes Ready in the cluster"

log "creating project + volume + app (default network rides project birth)"
cli projects create twonode >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
# default 网络由项目出生同事务建行（31bcd86）——显式再建必撞 E_ALREADY_EXISTS。
cli volumes create --project "$PROJECT_ID" data >/dev/null
cli apps create --project "$PROJECT_ID" shop >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
log "project=$PROJECT_ID app=$APP_ID"

# 双 process：web（无卷，副本分两节点）+ store（挂卷，钉住单节点）。
# compose 经 stdin 直灌容器（$WORKDIR 是 MSYS 路径，docker cp 在
# MSYS_NO_PATHCONV=1 下按 Windows 域解析——两域不一致，实证坑）。
log "deploying the app on both nodes (web spreads, store is volume-pinned)"
docker exec -i "$MGR_CID" sh -c 'cat > /root/compose.yaml' <<'YAML'
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    networks: [default]
    deploy: {replicas: 2}
  store:
    image: nginx:1.27
    volumes: ["data:/usr/share/nginx/html"]
    networks: [default]
    deploy: {replicas: 2}
volumes:
  data:
YAML
cli deploy --app "$APP_ID" --compose-file /root/compose.yaml >/dev/null

i=0
state=""
while [ "$i" -lt 240 ]; do
  state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
  [ "$state" = "succeeded" ] && break
  case "$state" in
    failed|superseded|cancelled)
      cli --json deployments list --app "$APP_ID" >&2 || true
      echo "deployment reached $state" >&2
      exit 1
      ;;
  esac
  i=$((i + 1))
  sleep 1
done
if [ "$state" != "succeeded" ]; then
  echo "timed out waiting for succeeded (last=$state)" >&2
  echo "--- deployment rows ---" >&2
  cli --json deployments list --app "$APP_ID" >&2 || true
  for p in web store; do
    echo "--- service ps $p ---" >&2
    docker exec "$MGR_CID" docker service ps "fleetly-default-$PROJECT_ID-$APP_ID-$p" >&2 || true
    echo "--- service inspect $p (placement/state) ---" >&2
    docker exec "$MGR_CID" docker service inspect "fleetly-default-$PROJECT_ID-$APP_ID-$p" \
      --format 'constraints={{join .Spec.TaskTemplate.Placement.Constraints ","}} replicas={{.Spec.Mode.Replicated.Replicas}}' >&2 || true
  done
  echo "--- nodes ---" >&2
  docker exec "$MGR_CID" docker node ls >&2 || true
  echo "--- fleetlyd log tail ---" >&2
  docker exec "$MGR_CID" sh -c 'tail -n 40 /var/log/fleetlyd.log' >&2 || true
  exit 1
fi
log "deployment succeeded across both nodes"

svc_name() {
  # $1 = process → 按 fleetly.workload.id 标记定位载体（服务名截断+哈希，
  # 不可反解；平台标记是唯一稳定锚——架构 §5）。
  docker exec "$MGR_CID" docker service ls --quiet --filter "label=fleetly.workload.id=$APP_ID-$1" | head -1
}

svc_tasks() {
  # $1 = service id → 每行一个运行中 task 的 Node。
  sid=$(svc_name "$1")
  [ -n "$sid" ] || return 0
  docker exec "$MGR_CID" docker service ps "$sid" \
    --format '{{.Node}}' --filter desired-state=running 2>/dev/null || true
}

log "asserting web replicas span both nodes"
WEB_NODES=$(svc_tasks web | sort -u)
N_WEB=$(printf '%s\n' "$WEB_NODES" | grep -c . || true)
if [ "$N_WEB" -lt 2 ]; then
  echo "web tasks did not spread across nodes (got: $WEB_NODES)" >&2
  docker exec "$MGR_CID" docker service ps "$(svc_name web)" >&2 || true
  exit 1
fi
log "web spans: $(printf '%s ' $WEB_NODES)"

log "asserting store replicas are pinned to one node"
STORE_NODES=$(svc_tasks store | sort -u)
N_STORE=$(printf '%s\n' "$STORE_NODES" | grep -c . || true)
if [ "$N_STORE" -ne 1 ]; then
  echo "volume-pinned tasks span $N_STORE nodes (expected exactly 1): $STORE_NODES" >&2
  exit 1
fi
PINNED_HOST=$(printf '%s\n' "$STORE_NODES" | head -1)
log "store pinned to: $PINNED_HOST"

log "asserting the placement constraint anchors the platform node id"
STORE_SID=$(svc_name store)
CONSTRAINT=$(docker exec "$MGR_CID" docker service inspect "$STORE_SID" \
  --format '{{join .Spec.TaskTemplate.Placement.Constraints ","}}')
case "$CONSTRAINT" in
  *node.labels.fleetly.node.id*) log "constraint: $CONSTRAINT" ;;
  *) echo "placement constraint missing/wrong: $CONSTRAINT" >&2; exit 1 ;;
esac

# P3 单机假设审计发现 A 的根治锚（F2.4/ADR-0040 已落地）：StreamLogs 重做
# 为集群面 ServiceList+ServiceLogs——两节点容器的日志都必须在实时流内。
# 帧的 node 字段（swarm 节点归因，details 面携带）双节点齐全。
log "asserting log stream covers BOTH nodes (finding A fixed)"
LOG_NODES=$(cli --json logs --app "$APP_ID" --process web --tail 50 \
  | sed -n 's/.*"node": *"\([^"]*\)".*/\1/p' | sort -u | grep -c . || true)
if [ "${LOG_NODES:-0}" -lt 2 ]; then
  echo "log stream covered $LOG_NODES nodes (expected both; finding A regression)" >&2
  cli --json logs --app "$APP_ID" --process web --tail 50 >&2 || true
  exit 1
fi
log "log stream covers both nodes ($LOG_NODES node ids seen)"

# 受管日志存储（F2.4/ADR-0040）：VL 起服 + 采集环落地后 --text 检索路径
# 可查（跨节点历史 + 全文过滤；install.sh 默认物化 logging.addr）。
log "waiting for the managed log store to come up"
VL_UP=0
i=0
while [ "$i" -lt 90 ]; do
  if docker exec "$MGR_CID" docker service ls --filter name=fleetly-fleetly-system-logging-victorialogs \
    --format '{{.Replicas}}' 2>/dev/null | grep -q '1/1'; then
    VL_UP=1; break
  fi
  i=$((i + 1)); sleep 2
done
[ "$VL_UP" = "1" ] || { echo "managed victoria-logs never became 1/1" >&2; \
  docker exec "$MGR_CID" docker service ps fleetly-fleetly-system-logging-victorialogs >&2 || true; exit 1; }
log "managed victoria-logs running"

log "asserting --text search returns persisted frames (collection loop live)"
TEXT_HITS=0
i=0
while [ "$i" -lt 60 ]; do
  n=$(cli --json logs --app "$APP_ID" --text worker --tail 20 2>/dev/null | grep -c '"line"' || true)
  if [ "${n:-0}" -gt 0 ]; then TEXT_HITS=1; break; fi
  i=$((i + 1)); sleep 2
done
[ "$TEXT_HITS" = "1" ] || { echo "log search (--text) returned no persisted frames" >&2; exit 1; }
log "log search --text live over the retention store"

# 受管指标面（F2.5/ADR-0041 锚 1 的两节点形态）：install.sh 默认物化
# FLEETLY_METRICS_ADDR（<advertise>:8428，bd3f04c 同源）——受管 VM 1/1 +
# cadvisor 全局 2/2（每节点恰一 task）。序列按 node 标签双值：从
# nodes list 取两节点平台 ID，逐节点查 cadvisor 序列非空（采集环按
# 平台节点归因入库——双节点采集的查询面证据）。
log "waiting for the managed metrics store (VM + global cAdvisor)"
i=0
while [ "$i" -lt 120 ]; do
  vm=$(docker exec "$MGR_CID" docker service ls --filter name=fleetly-fleetly-system-metrics-victoriametrics --format '{{.Replicas}}' 2>/dev/null)
  cd_=$(docker exec "$MGR_CID" docker service ls --filter name=fleetly-fleetly-system-metrics-cadvisor --format '{{.Replicas}}' 2>/dev/null)
  [ "$vm" = "1/1" ] && [ "$cd_" = "2/2" ] && break
  i=$((i + 1)); sleep 2
done
[ "${vm:-}" = "1/1" ] || { echo "victoria-metrics never became 1/1 on the two-node cluster (got ${vm:-none})" >&2; \
  docker exec "$MGR_CID" docker service ps fleetly-fleetly-system-metrics-victoriametrics >&2 || true; exit 1; }
[ "${cd_:-}" = "2/2" ] || { echo "cadvisor never became 2/2 (one global task per node; got ${cd_:-none})" >&2; \
  docker exec "$MGR_CID" docker service ps fleetly-fleetly-system-metrics-cadvisor >&2 || true; exit 1; }
log "managed VM 1/1 + cAdvisor 2/2 (global: one task per node)"

NODE_IDS=$(cli --json nodes list | sed -n 's/.*"platform_id": *"\([^"]*\)".*/\1/p')
N_NODES=$(printf '%s\n' "$NODE_IDS" | grep -c . || true)
[ "$N_NODES" = "2" ] || { echo "expected exactly two platform node ids, got ($N_NODES): $NODE_IDS" >&2; exit 1; }
for NID in $NODE_IDS; do
  NODE_OK=0
  i=0
  while [ "$i" -lt 45 ]; do
    n=$(cli --json metrics query "max(container_cpu_usage_seconds_total{job=\"fleetly-cadvisor\",node=\"$NID\"})" 2>/dev/null | grep -c '"value"' || true)
    if [ "${n:-0}" -gt 0 ]; then NODE_OK=1; break; fi
    i=$((i + 1)); sleep 2
  done
  if [ "$NODE_OK" != "1" ]; then
    echo "metrics query returned no points for node $NID (node-label dual-value assertion)" >&2
    echo "--- raw query output (no node filter) ---" >&2
    cli --json metrics query 'max(container_cpu_usage_seconds_total{job="fleetly-cadvisor"})' >&2 2>&1 || true
    echo "--- fleetlyd journal tail (metrics) ---" >&2
    docker exec "$MGR_CID" sh -c 'grep -o "\"msg\":\"[^\"]*metrics[^\"]*\".*" /var/log/fleetlyd.log | tail -5' >&2 2>&1 || true
    exit 1
  fi
  log "cadvisor series queryable for node $NID"
done
log "metrics series carry both node labels (two-node collection attribution)"

# 内存序列非空（ADR-0041 锚 1 原文的第二序列家族——gauge 直取面）。
MEM_OK=0
i=0
while [ "$i" -lt 45 ]; do
  n=$(cli --json metrics query 'max(container_memory_working_set_bytes{job="fleetly-cadvisor"})' 2>/dev/null | grep -c '"value"' || true)
  if [ "${n:-0}" -gt 0 ]; then MEM_OK=1; break; fi
  i=$((i + 1)); sleep 2
done
[ "$MEM_OK" = "1" ] || { echo "metrics query returned no points for the memory series" >&2; \
  cli --json metrics query 'max(container_memory_working_set_bytes{job="fleetly-cadvisor"})' >&2 2>&1 || true; exit 1; }
log "memory series non-empty through the managed store"

# 8428 认证面（ADR-0041 锚 5 的 CI 半锚，staging 真机同款 2026-10-04）：
# 无凭证 → 401（VM -httpAuth 面；实证 v1.152.0 豁免 /health——401 腿打
# 根路径）；带 keys/victoriametrics.json 的平台凭证 → 200（/health 的 OK
# 体；busybox wget --header 显式 Authorization，凭据从数据根读出——密码
# 绝不进 argv 之外的日志面由 wget 输出天然满足）。
VM_CREDS=/var/lib/fleetly/keys/victoriametrics.json
VM_USER=$(docker exec "$MGR_CID" sed -n 's/.*"username": *"\([^"]*\)".*/\1/p' "$VM_CREDS")
VM_PASS=$(docker exec "$MGR_CID" sed -n 's/.*"password": *"\([^"]*\)".*/\1/p' "$VM_CREDS")
if [ -z "$VM_USER" ] || [ -z "$VM_PASS" ]; then
  echo "could not read the victoria-metrics credential file ($VM_CREDS)" >&2
  exit 1
fi
NOAUTH=$(docker exec "$MGR_CID" wget -q -O - "http://$MGR_IP:8428/" 2>&1 || true)
case "$NOAUTH" in
  *401*) log "VM 8428 rejects unauthenticated requests (401)" ;;
  *) echo "expected 401 without credentials at 8428, got: $NOAUTH" >&2; exit 1 ;;
esac
AUTHED=$(docker exec "$MGR_CID" sh -c "B64=\$(printf '%s:%s' '$VM_USER' '$VM_PASS' | base64 | tr -d '\n'); wget -q -O - --header \"Authorization: Basic \$B64\" http://$MGR_IP:8428/health" 2>&1 || true)
case "$AUTHED" in
  OK*) log "VM 8428 serves authenticated requests (200 OK)" ;;
  *) echo "expected 200 OK with platform credentials at 8428, got: $AUTHED" >&2; exit 1 ;;
esac

log "TWO-NODE E2E PASSED"
