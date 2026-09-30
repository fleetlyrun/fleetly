#!/bin/sh
# e2e dind smoke（F0.24 + N0 收口批）：单节点真实链路——dind 容器内
# install.sh 一行安装（bin-dir 模式，F0.1）→ fleetlyd（真 swarm Runtime +
# 真 Builder）→ fleetly init 身份链（F0.2）→ doctor 冒烟（F0.4）→
# 全链 project → app → deploy(image) → succeeded → rollback → read paths →
# webhook HTTP 段（F0.13：容器内 curl 直发 gateway）。
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

# 1. 交叉编译两个二进制（linux/amd64；纯 Go 零 cgo）——install.sh 的
# bin-dir 模式消费。
log "cross-compiling fleetlyd + fleetly (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly

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

log "preloading nginx:1.27 into dind (offline-stable deploy)"
docker image save nginx:1.27 | docker exec -i "$DIND_CID" docker load >/dev/null

# 3. 一行安装（F0.1 冒烟）：install.sh 的本地 bin-dir 模式在容器内走完
# OS 检测 → Docker 检测 → swarm init → fleetlyd 起服（无 systemd 分支：
# setsid 后台 + /var/log/fleetlyd.log）→ 健康等待。Releases 通道随首个
# tag 批次生效。
log "running install.sh inside dind (FLEETLY_BIN_DIR mode)"
docker cp "$WORKDIR/bins" "$DIND_CID":/tmp/bins
docker cp install.sh "$DIND_CID":/tmp/install.sh
docker exec -e FLEETLY_BIN_DIR=/tmp/bins "$DIND_CID" sh /tmp/install.sh

# 3b. 身份链（F0.2 收口）：bootstrap → fleetly init（建 admin + 铸 CLI
# token + 写凭据 + 默认吊销 bootstrap）→ 旧凭证下一个调用即 401。此后
# 全链以 init 铸的 CURRENT_TOKEN 行进。
log "identity chain via fleetly init"
CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
if [ -z "$CURRENT_TOKEN" ]; then
  echo "bootstrap token file missing or empty" >&2
  exit 1
fi

cli() {
  # 连接参数经 FLEETLY_ADDR/FLEETLY_TOKEN 环境变量（conn 解析序：
  # 显式旗标 > env > 凭据文件；避开动词级 flag 和位置参数的顺序约束）。
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

# init 的 flag 必须在位置参数前（Go flag 在首个位置参数处停止解析）。
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$NEW_TOKEN" ]; then
  echo "fleetly init did not mint a CLI token" >&2
  exit 1
fi

if cli projects list >/dev/null 2>&1; then
  echo "revoked bootstrap token still works; expected 401" >&2
  exit 1
fi
log "bootstrap consumed by init; old token rejected as expected"

CURRENT_TOKEN="$NEW_TOKEN"
cli whoami >/dev/null
log "identity chain green (init -> credentials chain, revoke->401)"

# 3c. doctor 冒烟（F0.4）：本机诊断 + 远程 status 合流；全绿退出 0。
log "fleetly doctor smoke"
docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" fleetly doctor --json > "$WORKDIR/doctor.json"
grep -q '"fail": *0' "$WORKDIR/doctor.json" || {
  echo "doctor reported failures:" >&2
  cat "$WORKDIR/doctor.json" >&2
  exit 1
}

# 4. 全链冒烟。
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

# 5. webhook 段（F0.13）：配置 hook（首配铸造，secret 一次）→ 容器内 curl
# 直发 gateway :9081 /v1/hooks/<token>（HMAC 宿侧计算）。
log "webhook chain over the gateway HTTP face"
HOOK_SECRET=$(cli --json hooks set --app "$APP_ID" --repo https://github.com/acme/shop.git --branch main --watch web/ \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$HOOK_SECRET" ]; then
  echo "hooks set did not mint a secret" >&2
  exit 1
fi

docker exec "$DIND_CID" sh -c 'command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1'

SIGN() {
  printf '%s' "$1" | openssl dgst -sha256 -hmac "$HOOK_SECRET" | sed -n 's/.*= *\([0-9a-f]*\).*/\1/p'
}
post_hook() {
  # $1 event $2 delivery $3 signature $4 payload-file → "code body"
  code=$(docker exec "$DIND_CID" sh -c "curl -s -o /tmp/hook-body -w '%{http_code}' \
    -X POST -H 'Content-Type: application/json' \
    -H 'X-GitHub-Event: $1' -H 'X-GitHub-Delivery: $2' -H 'X-Hub-Signature-256: $3' \
    --data-binary @/tmp/hook-payload http://127.0.0.1:9081/v1/hooks/$HOOK_SECRET")
  body=$(docker exec "$DIND_CID" cat /tmp/hook-body)
  printf '%s %s' "$code" "$body"
}

payload() { printf '%s' "$1" | docker exec -i "$DIND_CID" sh -c 'cat > /tmp/hook-payload'; }

# 错误签名 → 401。
payload '{"zen":"ping"}'
resp=$(post_hook ping d-bad "sha256=0000000000000000000000000000000000000000000000000000000000000000")
case "$resp" in
  401\ *) log "bad signature rejected (401)" ;;
  *) echo "expected 401 for a bad signature, got: $resp" >&2; exit 1 ;;
esac

# ping → 200 + pong。
SIG=$(SIGN '{"zen":"ping"}')
resp=$(post_hook ping d-ping "sha256=$SIG")
case "$resp" in
  200\ *pong*) log "ping answered pong" ;;
  *) echo "expected pong, got: $resp" >&2; exit 1 ;;
esac

# push 命中 → accepted + 部署推进出队（repo 是虚构仓库：building 后构建
# 失败是预期终态——断言引擎已接手而非 queued）。
COMMIT="1234567890123456789012345678901234567890"
PUSH="{\"ref\":\"refs/heads/main\",\"after\":\"$COMMIT\",\"head_commit\":{\"id\":\"$COMMIT\",\"message\":\"smoke push\"},\"commits\":[{\"modified\":[\"web/index.ts\"]}]}"
payload "$PUSH"
SIG=$(SIGN "$PUSH")
resp=$(post_hook push d-push-1 "sha256=$SIG")
case "$resp" in
  200\ *accepted*) ;;
  *) echo "expected accepted push, got: $resp" >&2; exit 1 ;;
esac
WEBHOOK_DEP=$(printf '%s' "$resp" | sed -n 's/.*"deployment_id": *"\([^"]*\)".*/\1/p' | head -1)

# 同 delivery 重投 → duplicate；同 commit 新 delivery → 既有部署去重。
resp=$(post_hook push d-push-1 "sha256=$SIG")
case "$resp" in
  200\ *duplicate*) log "redelivery deduped" ;;
  *) echo "expected duplicate, got: $resp" >&2; exit 1 ;;
esac
resp=$(post_hook push d-push-1b "sha256=$SIG")
case "$resp" in
  200\ *accepted*) ;;
  *) echo "expected commit dedup to accepted, got: $resp" >&2; exit 1 ;;
esac
DEDUP_DEP=$(printf '%s' "$resp" | sed -n 's/.*"deployment_id": *"\([^"]*\)".*/\1/p' | head -1)
if [ "$DEDUP_DEP" != "$WEBHOOK_DEP" ]; then
  echo "commit dedup must return the same deployment ($DEDUP_DEP != $WEBHOOK_DEP)" >&2
  exit 1
fi
log "webhook push accepted (deployment $WEBHOOK_DEP, commit dedup holds)"

# skip 标记 → skipped。
SKIP="{\"ref\":\"refs/heads/main\",\"after\":\"9999999999999999999999999999999999999999\",\"head_commit\":{\"id\":\"x\",\"message\":\"chore [skip deploy]\"},\"commits\":[]}"
payload "$SKIP"
SIG2=$(SIGN "$SKIP")
resp=$(post_hook push d-skip "sha256=$SIG2")
case "$resp" in
  200\ *skipped*) log "[skip deploy] honored" ;;
  *) echo "expected skipped, got: $resp" >&2; exit 1 ;;
esac

# 引擎已接手：webhook 部署离开 queued（虚构仓库的构建失败是预期路径）。
i=0
while [ "$i" -lt 60 ]; do
  state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
  case "$state" in
    building|releasing|observing|failed) break ;;
  esac
  i=$((i + 1))
  sleep 1
done
case "$state" in
  building|releasing|observing|failed) log "webhook deployment picked up by the engine (state=$state)" ;;
  *) echo "webhook deployment stuck in $state" >&2; exit 1 ;;
esac

# token 绝不进日志（gateway/服务端双面核对：访问日志与 fleetlyd 主日志）。
if docker exec "$DIND_CID" sh -c "grep -c '$HOOK_SECRET' /var/log/fleetlyd.log" >/dev/null 2>&1; then
  echo "hook token leaked into fleetlyd logs" >&2
  exit 1
fi
log "hook token absent from fleetlyd logs"

# 6. 回滚与读路径（原链保持）。
log "rollback (Revision Replay)"
cli rollback --app "$APP_ID" >/dev/null
wait_state succeeded

log "revisions + events read paths"
cli revisions list --app "$APP_ID" >/dev/null
cli events list --limit 5 >/dev/null
cli nodes list >/dev/null
cli hooks get --app "$APP_ID" >/dev/null
cli audit --source webhook --limit 5 >/dev/null

log "SMOKE PASSED"
