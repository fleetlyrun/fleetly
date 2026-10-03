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
MGR_CID=$(docker run -d --privileged --name fleetly-e2e-mgr-"$$" -e DOCKER_TLS_CERTDIR= docker:29-dind)
WRK_CID=$(docker run -d --privileged --name fleetly-e2e-wrk-"$$" -e DOCKER_TLS_CERTDIR= docker:29-dind)
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

# P3 单机假设审计（docs/reviews/2026-10-03-single-node-assumption-audit.md
# 发现 A）：StreamLogs 的容器发现走 manager 本节点的 ContainerList——
# 调度到 worker 的容器日志静默缺失（已知边界，修复归宿=F2.4 日志管线
# 重做）。本断言把"manager 节点容器日志可见"的现状下限钉进 e2e：
# web 双副本必有其一在 manager，日志流不得为空。F2.4 落地后应升级为
# "两节点容器日志都在流内"。
log "asserting log stream covers manager-node containers (known boundary until F2.4)"
LOG_LINES=$(cli --json logs --app "$APP_ID" --process web --tail 50 | grep -c '"' || true)
if [ "${LOG_LINES:-0}" -eq 0 ]; then
  echo "log stream returned no frames for web (expected at least the manager-node replica)" >&2
  exit 1
fi
log "log stream live for manager-node replicas ($LOG_LINES frames)"

log "TWO-NODE E2E PASSED"
