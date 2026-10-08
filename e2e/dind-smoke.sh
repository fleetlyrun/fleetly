#!/bin/sh
# e2e dind smoke（F0.24 + N0 收口批）：单节点真实链路——dind 容器内
# install.sh 一行安装（bin-dir 模式，F0.1）→ fleetlyd（真 swarm Runtime +
# 真 Builder）→ fleetly init 身份链（F0.2）→ doctor 冒烟（F0.4）→
# 全链 project → app → deploy(image) → succeeded → 优雅退出与被杀重放
# 回归（F0.12 场景 1/2）→ 零 drift 断言（N0.1 P1-2 证据闭环：tag 镜像
# spec 逐字过 moby API 存真，扫描 ≥2 拍零 workload.drift_detected）→
# rollback → read paths →
# webhook HTTP 段（F0.13：容器内 curl 直发 gateway）。
# h2c Route 端到端（traefik 受管）随 nightly 多拓扑批接入（镜像拉取与
# ACME 时窗不适合单节点 smoke 的预算）。
#
# 前置：本机 docker 可用；本脚本经 mise 任务或直接 sh 执行。
set -eu

# Git-Bash（MSYS）会把以 / 开头的容器侧路径参数转译成本机路径（/tmp、
# /root 均实证命中；CI Linux 无此变量，天然 no-op）。
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

# 1. 交叉编译两个二进制（linux/amd64；纯 Go 零 cgo）——install.sh 的
#    bin-dir 模式消费；h2cclient 同批（webhook 段的 HTTP 面自备——dind 无
#    curl、apk 依赖出站网，离线确定性不赌网络）；webhookrecv 同批（F2.5
#    告警通道的真投递接收面，ADR-0041 锚 2/3）。
log "cross-compiling fleetlyd + fleetly + h2cclient + webhookrecv (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cclient" ./e2e/h2cclient
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/webhookrecv" ./e2e/webhookrecv

# 2. 起 dind（privileged；29 线与生产对齐）。
log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-smoke-"$$" \
  --cgroupns=host \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)
# --cgroupns=host：嵌套容器的 cgroup 在宿主层级可见——cadvisor（受管指标
# 采集端）读 /sys 才能看到 dind 内容器（私有 cgroupns 下只见 root/system
# 条目，容器样本缺席，告警评估面失明——CI 实证 2026-10-04）。

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
# tag 批次生效。容器侧路径用 /root（/tmp 是 MSYS 挂载点，宿侧 Git-Bash
# 会转译 docker cp 的容器路径——实证坑）。
log "running install.sh inside dind (FLEETLY_BIN_DIR mode)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins "$DIND_CID" sh /root/install.sh

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
#     无通道 warn 面（ADR-0041 锚 4 的前半，F2.5/doctor 两件）：init 后
#     零通道——"notification channels" 检查必须是 warn 且带可行动建议
#     （通道在场后的消警 ok 面在 F2.5 段断言——同一检查的两面）。
log "fleetly doctor smoke"
docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" fleetly doctor --json > "$WORKDIR/doctor.json"
grep -q '"fail": *0' "$WORKDIR/doctor.json" || {
  echo "doctor reported failures:" >&2
  cat "$WORKDIR/doctor.json" >&2
  exit 1
}
grep -A 1 '"name": *"notification channels"' "$WORKDIR/doctor.json" | grep -q '"status": *"warn"' || {
  echo "doctor: 'notification channels' must warn while no channel is configured" >&2
  cat "$WORKDIR/doctor.json" >&2
  exit 1
}
grep -q 'fleetly channels create' "$WORKDIR/doctor.json" || {
  echo "doctor: the channel warn must carry actionable advice" >&2
  exit 1
}
log "doctor green; notification-channels warn present (no channel configured yet)"

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
DRIFT_ANCHOR=$(date +%s) # 零 drift 断言窗起点（首个 tag 镜像部署成功）

log "verifying swarm carriers exist"
# 载体名 = fleetly-<team>-<projectID>-<appID>-<process>（ULID 进名字）；
# 副本健康已由 deployment succeeded（L1 门）背书，此处校验载体存在。
docker exec "$DIND_CID" docker service ls --format '{{.Name}} {{.Replicas}}' | grep "fleetly-default-" || true
docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep -q "fleetly-default-"

# 4b/4c. 优雅退出与被杀重放回归（F0.12）：
#   场景 1（领域模型 §5）：在途部署中 SIGKILL → 重启按 Generation 幂等
#   重放收口——同一 Deployment 到 succeeded、无重复下发（task 恰两行：
#   gen1 Shutdown + gen2 Running）、已成功基线不被回滚（无新增回滚行）。
#   场景 2：observing 中 SIGTERM（宽限契约：有界退出）→ 重启观察窗续算
#   （deadline 持久化在 deployment 行，不整窗重开——验收口径见断言）。
restart_fleetlyd() {
  # env 同源 /etc/fleetlyd.env（install.sh 无 systemd 形态落的启动 env）——
  # 手拼 env 会丢受管面地址（F2.5 实证：丢 FLEETLY_METRICS_ADDR = Metrics
  # 面静默停用，采集/查询全灭）。文件缺席回退旧内联形态（健壮性）。
  docker exec "$DIND_CID" sh -c \
    'ENVARGS="$(grep -v "^$" /etc/fleetlyd.env 2>/dev/null | tr "\n" " ")"; [ -n "$ENVARGS" ] || ENVARGS="FLEETLY_DATA_ROOT=/var/lib/fleetly"; setsid env $ENVARGS /usr/local/bin/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &'
  i=0
  while [ "$i" -lt 60 ]; do
    if cli whoami >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "fleetlyd did not come back after restart" >&2
  exit 1
}

deployment_states() {
  cli --json deployments list --app "$APP_ID" \
    | grep -o '"state": *"[a-z-]*"' | sed 's/.*"\([a-z-]*\)"$/\1/'
}

service_name() {
  docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep '^fleetly-default-'
}

log "scenario 1: SIGKILL mid-deploy (F0.12 domain model scenario 1)"
DEP2_ID=$(cli --json deploy --app "$APP_ID" --image nginx:1.27 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
# 尽力命中 releasing（拉起窗口短；滑到 observing 也属在途，同样成立）。
i=0
while [ "$i" -lt 8 ]; do
  state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
  case "$state" in
    releasing|observing) break ;;
  esac
  i=$((i + 1))
  sleep 0.3
done
# 前置断言：kill 时必须在途（queued/preparing/releasing/observing）。到达
# failed/rolling-back 说明部署自身已败——不属于本场景，直接取证失败。
case "$state" in
  queued|preparing|releasing|observing) ;;
  *)
    echo "scenario 1 precondition broken: deployment already in $state (expected in-flight)" >&2
    cli --json deployments list --app "$APP_ID" >&2 || true
    exit 1
    ;;
esac
log "scenario 1: killing fleetlyd (deployment state at kill: $state)"
docker exec "$DIND_CID" pkill -9 fleetlyd || true
sleep 1
log "scenario 1: restarting fleetlyd"
restart_fleetlyd
wait_state succeeded
log "scenario 1: replay converged to succeeded"

# 收口断言：恰好两条部署且全部 succeeded 终态——基线保持 succeeded（未
# 被回滚）、重放未另起新行（无重复下发的事务面）。
states1=$(deployment_states | tr '\n' ' ')
if [ "$(deployment_states | wc -w)" -ne 2 ] || [ "$(deployment_states | grep -c succeeded)" -ne 2 ]; then
  echo "scenario 1: expected exactly 2 succeeded deployments, got: $states1" >&2
  exit 1
fi

# 载体无重复下发：同 spec 的 gen2 Ensure 是 update no-op；kill 前后的重放
# 不得产生额外 task（恰两行：gen1 Shutdown + gen2 Running）。
SVC=$(service_name)
task_rows=$(docker exec "$DIND_CID" docker service ps "$SVC" --format '{{.CurrentState}}')
n_running=$(printf '%s\n' "$task_rows" | grep -c '^Running ' || true)
n_shutdown=$(printf '%s\n' "$task_rows" | grep -c '^Shutdown ' || true)
n_total=$(printf '%s\n' "$task_rows" | grep -c . || true)
if [ "$n_total" -ne 2 ] || [ "$n_running" -ne 1 ] || [ "$n_shutdown" -ne 1 ]; then
  echo "scenario 1: expected 2 task rows (1 Running + 1 Shutdown), got ($n_total/$n_running/$n_shutdown):" >&2
  printf '%s\n' "$task_rows" >&2
  exit 1
fi
log "scenario 1: no duplicate dispatch (2 task rows: 1 running + 1 shutdown), baseline intact"

log "scenario 2: SIGTERM during observing (drain grace) then window continuation"
cli --json deploy --app "$APP_ID" --image nginx:1.27 >/dev/null
wait_state observing
OBS_AT=$(date +%s)
# 走进窗口中段再杀（默认 60s 窗，留足续算/重开的区分裕度）。
sleep 15
TERM_AT=$(date +%s)
docker exec "$DIND_CID" pkill -TERM fleetlyd || true
# 宽限契约：lynx 排水窗固定 30s（在途收口 + 摘流窗口），空载也走满——
# 有界退出（≤30s + 余量）即契约达成，不追求秒退。
i=0
while [ "$i" -lt 80 ]; do
  if ! docker exec "$DIND_CID" pgrep fleetlyd >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 0.5
done
if [ "$i" -ge 80 ]; then
  echo "fleetlyd did not exit within the drain grace (40s > 30s budget + margin)" >&2
  exit 1
fi
log "scenario 2: fleetlyd exited gracefully in $(($(date +%s) - TERM_AT))s"
restart_fleetlyd
wait_state succeeded
DONE_AT=$(date +%s)
# 续算断言：deadline 持久化在行上——成功应在 observing 检出后 ~62s 内
#（含检测/轮询迟滞）；若整窗重开，最早 observing+15s(杀) + ~5s(重启) +
# 60s(窗) ≈ 80s。72s 阈值两侧各留 ≥8s 裕度。
if [ $((DONE_AT - OBS_AT)) -ge 72 ]; then
  echo "scenario 2: observe window restarted instead of continuing (took $((DONE_AT - OBS_AT))s)" >&2
  exit 1
fi
if [ "$(deployment_states | wc -w)" -ne 3 ]; then
  echo "scenario 2: expected 3 deployment rows, got: $(deployment_states | tr '\n' ' ')" >&2
  exit 1
fi
log "scenario 2: window continued after restart (succeeded at +$((DONE_AT - OBS_AT))s)"

# 4d. 零 drift 断言（证据闭环，N0.1 P1-2）：compareSpecs 对镜像逐字比对，
#     前提是 moby API 不把 tag 引用钉版成 repo:tag@sha256 存入 service
#     spec（CLI 路径才会钉版）。该前提此前只有 staging 实证与 fake 单测
#     （回显 spec，无证伪力）——此处用真实 swarm 钉死：自首个 tag 部署
#     succeeded 起已历场景 1/2 的重放与两次重启（重启后 spec 缓存重建会
#     重扫，钉版形态必然再发），窗满 ≥70s（drift 扫描周期 30s，≥2 拍）
#     后窗口内必须零 workload.drift_detected。有界轮询 fail-fast；失败
#     输出事件片段辅助诊断。
log "zero-drift assertion over the tag-deploy window (spec pass-through pin)"
i=0
while :; do
  ELAPSED=$(($(date +%s) - DRIFT_ANCHOR))
  DRIFT_EVENTS=$(cli --json events list --limit 200 | grep -c '"name": *"workload.drift_detected"') || true
  if [ -z "$DRIFT_EVENTS" ]; then
    echo "events list read failed (empty count)" >&2
    exit 1
  fi
  if [ "$DRIFT_EVENTS" -ne 0 ]; then
    echo "zero-drift assertion failed: $DRIFT_EVENTS workload.drift_detected event(s) within ${ELAPSED}s of the first tag deploy" >&2
    cli --json events list --limit 200 | grep -B 4 -A 2 '"workload.drift_detected"' >&2 || true
    exit 1
  fi
  if [ "$ELAPSED" -ge 70 ]; then
    break
  fi
  if [ "$i" -ge 24 ]; then # 24 拍 x 5s：窗满兜底上界（既有等待段下正常路径到不了）
    echo "zero-drift window did not elapse in time (elapsed=${ELAPSED}s)" >&2
    exit 1
  fi
  i=$((i + 1))
  sleep 5
done
log "zero workload.drift_detected events (window ${ELAPSED}s >= 2 scan beats)"

# 5. 回滚与读路径（在 webhook 段之前：后者会冻结引擎循环，见段注）。
log "rollback (Revision Replay)"
cli rollback --app "$APP_ID" >/dev/null
wait_state succeeded

log "revisions + events read paths"
cli revisions list --app "$APP_ID" >/dev/null
cli events list --limit 5 >/dev/null
cli nodes list >/dev/null

# 6. webhook 段（F0.13，本段置尾）：配置 hook（首配铸造，secret 一次）→
#    容器内经自备 h2cclient 直发 gateway :9081 /v1/hooks/<token>（HMAC
#    宿侧计算；零出站依赖）。
#    仓库指向 TEST-NET（192.0.2.1）：git clone 连接黑洞挂起——clone 在单写者
#    drive 步内同步执行，引擎循环会冻结到 5 分钟超时。置尾两点收益：commit
#    去重窗确定性在场（去重锚只匹配活跃部署；真实仓库 clone 快败曾是本段
#    的时序骰子），且脚本随后即结束，冻结的循环随容器清理一起埋葬。
log "webhook chain over the gateway HTTP face"
HOOK_SECRET=$(cli --json hooks set --app "$APP_ID" --repo https://192.0.2.1/acme/shop.git --branch main --watch web/ \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$HOOK_SECRET" ]; then
  echo "hooks set did not mint a secret" >&2
  exit 1
fi

SIGN() {
  printf '%s' "$1" | openssl dgst -sha256 -hmac "$HOOK_SECRET" | sed -n 's/.*= *\([0-9a-f]*\).*/\1/p'
}
post_hook() {
  # $1 event $2 delivery $3 signature → "code body"（payload 恒在容器内
  # /tmp/hook-payload，payload() 先行写入）。
  code=$(docker exec "$DIND_CID" /root/bins/h2cclient -X POST -o /tmp/hook-body \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: $1" -H "X-GitHub-Delivery: $2" -H "X-Hub-Signature-256: $3" \
    -data "@/tmp/hook-payload" http://127.0.0.1:9081/v1/hooks/$HOOK_SECRET \
    | sed -n 's/^STATUS \([0-9]*\).*/\1/p')
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

# push 命中 → accepted + 部署推进出队（TEST-NET 仓库：building 挂起至
# 脚本结束——断言引擎已接手而非 queued）。
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

# 同 delivery 重投 → 幂等重放首响应（Q-21/ADR-0024：无独立 duplicate 状态，
# 逐字节重放——status 仍 accepted 且 deployment_id 与首投一致）；同 commit
# 新 delivery → 既有部署去重。
resp=$(post_hook push d-push-1 "sha256=$SIG")
case "$resp" in
  200\ *accepted*) ;;
  *) echo "expected idempotent replay, got: $resp" >&2; exit 1 ;;
esac
REPLAY_DEP=$(printf '%s' "$resp" | sed -n 's/.*"deployment_id": *"\([^"]*\)".*/\1/p' | head -1)
if [ "$REPLAY_DEP" != "$WEBHOOK_DEP" ]; then
  echo "redelivery must replay the original deployment ($REPLAY_DEP != $WEBHOOK_DEP)" >&2
  exit 1
fi
log "redelivery replayed the original response (deployment $REPLAY_DEP)"
# 同 commit 新 delivery → 200 accepted；deployment_id 与首投一致仅当首投
# 部署仍活跃（commit 去重只匹配活跃部署——引擎语义）。TEST-NET 仓库在 CI
# 上 clone 快败（本机黑洞挂起）：首投部署可能 200ms 内终态，此时新铸是
# 正确行为——同 id 断言的确定性覆盖在 apitest（webhook_test 的预置检出
# 目录夹具），e2e 只钉传输面契约。
resp=$(post_hook push d-push-1b "sha256=$SIG")
case "$resp" in
  200\ *accepted*) ;;
  *) echo "expected commit dedup to accepted, got: $resp" >&2; exit 1 ;;
esac
DEDUP_DEP=$(printf '%s' "$resp" | sed -n 's/.*"deployment_id": *"\([^"]*\)".*/\1/p' | head -1)
if [ "$DEDUP_DEP" != "$WEBHOOK_DEP" ]; then
  log "commit dedup minted a fresh deployment (first already terminal offline): $DEDUP_DEP"
fi
log "webhook push accepted (deployment $WEBHOOK_DEP, commit dedup answered accepted)"

# skip 标记 → skipped。
SKIP="{\"ref\":\"refs/heads/main\",\"after\":\"9999999999999999999999999999999999999999\",\"head_commit\":{\"id\":\"x\",\"message\":\"chore [skip deploy]\"},\"commits\":[]}"
payload "$SKIP"
SIG2=$(SIGN "$SKIP")
resp=$(post_hook push d-skip "sha256=$SIG2")
case "$resp" in
  200\ *skipped*) log "[skip deploy] honored" ;;
  *) echo "expected skipped, got: $resp" >&2; exit 1 ;;
esac

# 引擎已接手：webhook 部署离开 queued（building = clone 已在单写者步内
# 挂起，活跃状态正是 commit 去重窗在场的直接证据）。
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

log "hook + audit read paths"
cli hooks get --app "$APP_ID" >/dev/null
cli audit --source webhook --limit 5 >/dev/null

# ---- F3.2 exec 子面（ADR-0049）：回环代理形态 ----
# manager 节点进程内回环代理随 daemon 启动连自身 gateway——单节点零
# enroll 即具备 exec 面。全链：nodes list 的 relay_online 观测 →
# `fleetly exec` one-shot（真 docker exec：nginx 容器内 /bin/echo）→
# 退出码透传（非零命令）→ `fleetly shell` 非交互 stdin EOF 形态 →
# 审计 exec.session 行带命令面（audit Detail）。
log "exec leg: loopback relay (manager node, zero-enroll form)"
i=0
while [ "$i" -lt 45 ]; do
  if cli --json nodes list | grep -q '"relay_online": *true'; then
    break
  fi
  i=$((i + 1)); sleep 2
done
cli --json nodes list | grep -q '"relay_online": *true' || {
  echo "loopback relay never came online (relay_online=false)" >&2
  cli --json nodes list >&2 || true
  exit 1
}
log "relay online (loopback form)"

EXEC_OUT=$(cli exec "$APP_ID/web" -- /bin/echo exec-loop-ok)
case "$EXEC_OUT" in
  *exec-loop-ok*) log "fleetly exec ran in the container (real docker exec chain)" ;;
  *) echo "exec output missing marker, got: $EXEC_OUT" >&2; exit 1 ;;
esac

EXEC_RC=0
cli exec "$APP_ID/web" -- /bin/false >/dev/null 2>&1 || EXEC_RC=$?
[ "$EXEC_RC" = "1" ] || { echo "exec exit code passthrough broken (expected 1 from /bin/false, got $EXEC_RC)" >&2; exit 1; }
log "exec exit code passed through (/bin/false → 1)"

SHELL_ERR=$(cli shell "$APP_ID/web" </dev/null 2>&1 >/dev/null || true)
case "$SHELL_ERR" in
  *"# session"*) log "fleetly shell session opened and closed on stdin EOF" ;;
  *) echo "shell session header missing, stderr: $SHELL_ERR" >&2; exit 1 ;;
esac

cli audit --action exec. --limit 5 | grep -q "exec.session" || {
  echo "exec.session audit rows missing" >&2
  exit 1
}
log "exec session audit rows carry the command face"

# ---- F2.5 Metrics/Alerting 面（ADR-0041）----
# install.sh 物化 FLEETLY_METRICS_ADDR（同 registry/logging 源）——受管
# VM + cadvisor 全局端随新版起服。等待受管双件 1/1（全局服务单节点也是
# 1/1；镜像拉取有窗）。随后三段：采集序列可查 → webhook 假接收器 + 通道
# test 双面（可达 delivered=true / 不可达诚实失败）→ 阈值规则全链真投递
#（fired + 回落 resolved；能力门控语义对齐 0f8d4da 先例）。
log "waiting for the managed metrics store (VM + cAdvisor)"
i=0
while [ "$i" -lt 120 ]; do
  vm=$(docker exec "$DIND_CID" docker service ls --filter name=fleetly-fleetly-system-metrics-victoriametrics --format '{{.Replicas}}' 2>/dev/null)
  cd_=$(docker exec "$DIND_CID" docker service ls --filter name=fleetly-fleetly-system-metrics-cadvisor --format '{{.Replicas}}' 2>/dev/null)
  [ "$vm" = "1/1" ] && [ "$cd_" = "1/1" ] && break
  i=$((i + 1)); sleep 2
done
[ "${vm:-}" = "1/1" ] || { echo "victoria-metrics never became 1/1 (got ${vm:-none})" >&2; exit 1; }
[ "${cd_:-}" = "1/1" ] || { echo "cadvisor never became 1/1 (got ${cd_:-none})" >&2; exit 1; }
log "managed VM + cAdvisor running"

# 采集环落地：cadvisor 序列经 engine 推入 VM——metrics query 返回点集
#（container_cpu_usage_seconds_total 是 cadvisor 必有序列）。
log "asserting metrics query returns scraped series"
POINTS=0
i=0
while [ "$i" -lt 45 ]; do
  n=$(cli --json metrics query 'max(container_cpu_usage_seconds_total{job="fleetly-cadvisor"})' 2>/dev/null | grep -c '"value"' || true)
  if [ "${n:-0}" -gt 0 ]; then POINTS=1; break; fi
  i=$((i + 1)); sleep 2
done
if [ "$POINTS" != "1" ]; then
  echo "metrics query returned no points for the cadvisor series" >&2
  echo "--- raw query output ---" >&2
  cli --json metrics query 'max(container_cpu_usage_seconds_total{job="fleetly-cadvisor"})' >&2 2>&1 || true
  echo "--- fleetlyd journal tail (metrics) ---" >&2
  docker exec "$DIND_CID" sh -c 'grep -o "\"msg\":\"[^\"]*metrics[^\"]*\".*" /var/log/fleetlyd.log | tail -5' >&2 2>&1 || true
  echo "--- cadvisor endpoint direct ---" >&2
  docker exec "$DIND_CID" sh -c 'NODE_ADDR=$(docker node inspect self --format "{{.Status.Addr}}" 2>/dev/null); echo node-addr=$NODE_ADDR; wget -q -O - http://127.0.0.1:8080/metrics 2>/dev/null | grep -c container_cpu_usage_seconds_total' >&2 2>&1 || true
  exit 1
fi
log "cadvisor series queryable through the managed store"

# webhook 假接收器（ADR-0041 锚 2/3 的真投递面——后果节"dind e2e 走假
# webhook 接收器"的兑现）：dind 内后台跑，fleetlyd 同容器网络栈经
# loopback 投递（通道 URL 即凭证，无需 HMAC——ADR-0041 决策 4）。/reset
# 使 test 载荷与告警载荷分阶段断言互不污染。
log "starting the webhook receiver (alert delivery face)"
docker exec "$DIND_CID" sh -c 'setsid /root/bins/webhookrecv :9099 >/var/log/webhookrecv.log 2>&1 </dev/null &'
i=0
while [ "$i" -lt 15 ]; do
  if docker exec "$DIND_CID" wget -q -O /dev/null http://127.0.0.1:9099/dump 2>/dev/null; then
    break
  fi
  i=$((i + 1)); sleep 1
done
if [ "$i" -ge 15 ]; then
  echo "webhook receiver did not come up" >&2
  docker exec "$DIND_CID" sh -c 'cat /var/log/webhookrecv.log 2>/dev/null' >&2 || true
  exit 1
fi
recv_dump() { docker exec "$DIND_CID" wget -q -O - http://127.0.0.1:9099/dump 2>/dev/null || true; }
recv_reset() { docker exec "$DIND_CID" wget -q -O /dev/null http://127.0.0.1:9099/reset 2>/dev/null || true; }
alert_diag() {
  echo "--- webhook receiver dump ---" >&2
  recv_dump >&2 || true
  echo "--- alerts list ---" >&2
  cli --json alerts list >&2 || true
  echo "--- fleetlyd journal tail (alerting) ---" >&2
  docker exec "$DIND_CID" sh -c 'grep -E "alert|channel" /var/log/fleetlyd.log | tail -10' >&2 2>&1 || true
}

# 通道链可达面（锚 3 成功侧——与文末不可达侧成对）：登记可达通道 +
# test 动词即时投递——delivered=true 且接收器收到 alert_test 载荷（字段
# 真值断言：type/rule_id/metric/state 一行内齐命中，不做 substring 松断言）。
log "notification channel test (reachable endpoint: delivered=true)"
CH_LIVE_ID=$(cli channels create --name ops-hook-live --kind webhook --url http://127.0.0.1:9099/hook | sed -n 's/.*(id \([^)]*\)).*/\1/p')
[ -n "$CH_LIVE_ID" ] || { echo "channel create (reachable endpoint) produced no id" >&2; exit 1; }
cli --json channels test --channel "$CH_LIVE_ID" | grep -q '"delivered": *true' || {
  echo "channel test should deliver to the reachable receiver (delivered=true)" >&2
  cli --json channels test --channel "$CH_LIVE_ID" >&2 || true
  recv_dump >&2 || true
  exit 1
}
TEST_HIT=0
i=0
while [ "$i" -lt 10 ]; do
  if recv_dump | grep -q '"type":"alert_test".*"rule_id":"test".*"metric":"test".*"state":"test"'; then
    TEST_HIT=1; break
  fi
  i=$((i + 1)); sleep 1
done
[ "$TEST_HIT" = "1" ] || {
  echo "the receiver did not record the alert_test payload (fields incomplete)" >&2
  alert_diag
  exit 1
}
log "test payload delivered and verified at the receiver"

# doctor 消警面（锚 4 后半）：通道在场 → "notification channels" 检查归位
# ok（3c 断言过无通道 warn 面——同一检查两面齐）。
cli doctor --json > "$WORKDIR/doctor2.json"
grep -A 1 '"name": *"notification channels"' "$WORKDIR/doctor2.json" | grep -q '"status": *"ok"' || {
  echo "doctor: 'notification channels' should be ok after a channel is configured" >&2
  cat "$WORKDIR/doctor2.json" >&2
  exit 1
}
log "doctor notification-channels check cleared after channel configuration"
recv_reset

# 阈值规则全链（环境能力门控）：cadvisor 的容器样本带平台标签
#（container_label_fleetly_ns_app）时才可能评估——部分嵌套环境（CI dind）
# cadvisor 看不到内容器（cgroup/docker 发现面差异），标签恒空。能力在场
# → 全链断言（memory > 1B 必越限 → firing → 真投递 → 回落 resolved 真
# 投递）；缺席 → 响亮 SKIP（真机 staging 已全链验收，ADR-0041 锚 6 偏差
# 注；CI 不谎报绿也不误报红）。
RULE_ID=$(cli --json alerts rules create --app "$APP_ID" --metric memory_working_set_bytes --threshold 1 \
  | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$RULE_ID" ] || { echo "alert rule create produced no id" >&2; exit 1; }
LABELED=$(docker exec "$DIND_CID" sh -c \
  'wget -q -O - http://127.0.0.1:8080/metrics 2>/dev/null | grep -c "container_label_fleetly_ns_app=\"0"' || true)
if [ "${LABELED:-0}" -gt 0 ]; then
  FIRING=0
  i=0
  # 窗宽 150x2s：本段在 webhook 段（TEST-NET 仓库）之后——黑洞环境下
  # clone 在单写者 drive 步内挂起、引擎循环冻结最长 5 分钟（CI 快败不受
  # 影响），评估恢复后的首个 15s 节拍即 firing；窗宽盖过冻结尾沿。
  while [ "$i" -lt 150 ]; do
    st=$(cli --json alerts list 2>/dev/null | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    [ "$st" = "firing" ] && FIRING=1 && break
    i=$((i + 1)); sleep 2
  done
  [ "$FIRING" = "1" ] || { echo "labeled cadvisor samples present but rule never fired" >&2; cli --json alerts list >&2 || true; exit 1; }
  log "threshold alert fired and is listed"

  # alert.fired 真投递（锚 2 前半）：firing 迁移沿的通道载荷到达接收器——
  # 字段真值断言（type/rule_id/app_id/metric/state 一行内齐命中；载荷是
  # engine json.Marshal 的紧凑 JSON 字段序稳定，rule/app 是本链铸的真
  # ULID——不是 substring 松断言）。
  FIRED=0
  i=0
  while [ "$i" -lt 30 ]; do
    if recv_dump | grep -q "\"type\":\"alert\".*\"rule_id\":\"$RULE_ID\".*\"app_id\":\"$APP_ID\".*\"metric\":\"memory_working_set_bytes\".*\"state\":\"firing\""; then
      FIRED=1; break
    fi
    i=$((i + 1)); sleep 2
  done
  [ "$FIRED" = "1" ] || {
    echo "alert.fired payload never reached the webhook receiver" >&2
    alert_diag
    exit 1
  }
  log "alert.fired payload delivered (rule/app/metric/state verified at the receiver)"

  # resolved 真投递（锚 2 后半——回落形态）：拆 App 使该 App 容器样本消失，
  # 下一拍评估 value 零 → firing→ok 迁移沿派发 resolved。规则行删除本身
  # 不派发（行删除即无评估面）——回落是 e2e 可确定的 resolved 路径。
  log "tearing the app down to force the resolve proxy (samples fall back)"
  cli apps delete --app "$APP_ID" >/dev/null
  RESOLVED=0
  i=0
  while [ "$i" -lt 30 ]; do
    if recv_dump | grep -q "\"type\":\"alert\".*\"rule_id\":\"$RULE_ID\".*\"state\":\"ok\""; then
      RESOLVED=1; break
    fi
    i=$((i + 1)); sleep 2
  done
  [ "$RESOLVED" = "1" ] || {
    echo "alert.resolved payload never reached the webhook receiver" >&2
    alert_diag
    exit 1
  }
  log "alert.resolved payload delivered (state=ok verified at the receiver)"
else
  log "SKIP: cAdvisor container labels unavailable in this environment (nested visibility) — alert firing + real delivery chain validated on staging (ADR-0041 anchors 2/6 deviation note); asserting rule lifecycle + channel test delivery only"
  cli alerts rules list 2>/dev/null | grep -q "memory_working_set_bytes" || {
    echo "alert rule lifecycle broken" >&2; exit 1
  }
fi

# 通道链诚实失败面（锚 3 不可达侧——与上文可达侧成对）：登记不可达通道 +
# test —— delivered=false + 错误文本是响应数据（age 信封写面 + 解封派发
# 面 + 失败记账三绿即锚）。
CH_ID=$(cli channels create --name ops-hook --kind webhook --url http://127.0.0.1:1/x | sed -n 's/.*(id \([^)]*\)).*/\1/p')
[ -n "$CH_ID" ] || { echo "channel create produced no id" >&2; exit 1; }
cli --json channels test --channel "$CH_ID" | grep -q '"error":' || {
  echo "channel test should return an honest failure response (non-empty error) for an unreachable endpoint" >&2; exit 1
}
log "notification channel test payload dispatched (unreachable endpoint reported honestly)"

log "SMOKE PASSED"
