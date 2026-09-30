#!/bin/sh
# e2e dind smoke（F0.24 尾项）：单节点真实链路——dind 容器内 swarm init →
# fleetlyd（真 swarm Runtime + 真 Builder）→ fleetly CLI 全链：
#   project → app → deploy(image) → 状态机到 succeeded → nodes →
#   rollback(Replay) → revisions list/diff → events list。
# h2c Route 端到端（traefik 受管）随 nightly 多拓扑批接入（镜像拉取与
# ACME 时窗不适合单节点 smoke 的预算）。
#
# 前置：本机 docker 可用；本脚本经 mise 任务或直接 sh 执行。
set -eu

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

# 1. 交叉编译两个二进制（linux/amd64；纯 Go 零 cgo）。
log "cross-compiling fleetlyd + fleetly (linux/amd64)"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/fleetly" ./cmd/fleetly

# 2. 起 dind（privileged；29 线与生产对齐）。
log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-smoke-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

wait_docker() {
  i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "dind daemon did not become ready" >&2
  return 1
}
log "waiting for dind daemon"
wait_docker

# 3. swarm init + 传入二进制。
log "swarm init inside dind"
docker exec "$DIND_CID" docker swarm init --advertise-addr 127.0.0.1 >/dev/null

log "preloading nginx:1.27 into dind (offline-stable deploy)"
docker image save nginx:1.27 | docker exec -i "$DIND_CID" docker load >/dev/null

docker cp "$WORKDIR/fleetlyd" "$DIND_CID":/usr/local/bin/fleetlyd
docker cp "$WORKDIR/fleetly" "$DIND_CID":/usr/local/bin/fleetly

# 4. fleetlyd 起服（setsid 脱离 exec 会话进程组——docker exec 会话结束
# 时 dockerd 清理同进程组残留，是 exec 后台守护的知名坑）。数据根显式
# /data：Bootstrap Token 落 /data/bootstrap-token（F0.2/F0.6）。
log "starting fleetlyd"
docker exec "$DIND_CID" sh -c 'setsid env FLEETLY_DATA_ROOT=/data fleetlyd > /var/log/fleetlyd.log 2>&1 < /dev/null &'
sleep 1

wait_fleetlyd() {
  i=0
  last_err=""
  while [ "$i" -lt 60 ]; do
    if last_err=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" fleetly status 2>&1); then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  docker exec "$DIND_CID" sh -c 'cat /var/log/fleetlyd.log >&2 || true'
  echo "fleetlyd did not become ready; last status error: $last_err" >&2
  return 1
}
log "waiting for fleetlyd"
wait_fleetlyd

# 4b. 身份链（F0.5~F0.7）：取 Bootstrap Token → login（凭据落盘）→ 建
# admin 用户与 token → 吊销 bootstrap → 旧凭证下一个调用即 401 → 新
# token 续链。此后全链以 CURRENT_TOKEN 行进。
log "identity chain over bootstrap token"
# MSYS（Windows Git-Bash）会把 /data 形参转成本机路径；sh -c 包住使
# 路径在容器内解析。
CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /data/bootstrap-token')
if [ -z "$CURRENT_TOKEN" ]; then
  echo "bootstrap token file missing or empty" >&2
  exit 1
fi

cli() {
  # 连接参数经 FLEETLY_ADDR/FLEETLY_TOKEN 环境变量（conn 解析序：
  # 显式旗标 > env > 凭据文件；避开动词级 flag 和位置参数的顺序约束）。
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" fleetly login --token "$CURRENT_TOKEN" >/dev/null
cli users create --role builtin-admin alice >/dev/null
NEW_TOKEN=$(cli --json tokens create --role builtin-admin smoke-admin | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$NEW_TOKEN" ]; then
  echo "failed to mint admin token" >&2
  exit 1
fi

BOOT_ID=$(cli --json whoami | sed -n 's/.*"token_id": *"\([^"]*\)".*/\1/p' | head -1)
cli tokens revoke "$BOOT_ID" >/dev/null

if cli projects list >/dev/null 2>&1; then
  echo "revoked bootstrap token still works; expected 401" >&2
  exit 1
fi
log "bootstrap revoked; next call rejected as expected"

CURRENT_TOKEN="$NEW_TOKEN"
cli audit --limit 3 >/dev/null
log "identity chain green (login, user+token mint, revoke->401, audit)"

# 5. 全链冒烟。
log "creating project + app"
cli projects create shop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" web >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
log "project=$PROJECT_ID app=$APP_ID"

log "deploying nginx:1.27 (direct image)"
DEP_ID=$(cli --json deploy --app "$APP_ID" --image nginx:1.27 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)

wait_state() {
  want="$1"; i=0
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$APP_ID" >&2 || true
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

log "waiting for deployment to succeed (state machine over real swarm)"
wait_state succeeded
log "deployment succeeded"

log "verifying swarm carriers exist"
# 载体名 = fleetly-<team>-<projectID>-<appID>-<process>（ULID 进名字）；
# 副本健康已由 deployment succeeded（L1 门）背书，此处校验载体存在。
docker exec "$DIND_CID" docker service ls --format '{{.Name}} {{.Replicas}}' | grep "fleetly-default-" || true
docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep -q "fleetly-default-"

log "rollback (Revision Replay)"
cli rollback --app "$APP_ID" >/dev/null
wait_state succeeded

log "revisions + events read paths"
cli revisions list --app "$APP_ID" >/dev/null
cli events list --limit 5 >/dev/null
cli nodes list >/dev/null

log "SMOKE PASSED"
