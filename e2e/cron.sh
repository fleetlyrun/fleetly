#!/bin/sh
# e2e/cron.sh — E5 Cron/定时任务端到端（单节点 dind 形态；架构 §4.3 细则
# 的真机闭环；s3-rustfs.sh 同骨架）：
#
# 编排（宿主侧，自足）：交叉编译 linux/amd64 fleetlyd+fleetly（或复用
# CR_BIN_DIR）→ 宿主 bridge 上起一个特权 dind（私网 10.216.0.0/24，镜像钉
# digest）→ swarm init + fleetlyd 起服 → 全部断言经 dind 内的 docker/
# fleetly CLI 驱动：
#
#   C0 部署（混合应用）：web（长驻）+ task（fleetly.cron "* * * * *" +
#      sleep 3）→ 部署成功；web 长驻服务在位，**无 cron job 服务**被当作
#      长驻创建（只声明不部署）。
#   C1 到点触发（≤2 拍口径的真机放宽）：cron.triggered 事件落库 →
#      cron.succeeded 事件 → cron runs 台账出现 succeeded 行 → job 服务
#      已删（完成后删除）→ **job 输出行进现有日志采集面**（logs history
#      读面读到 marker 行——E5 验收五项之「日志」，整改项）。
#   C2 手动触发：fleetly cron trigger → 新 run 行（同一路径）；说明：审计
#      cron.manual_triggered 无 CLI/API 读面（RecentAudits 未接 RPC），
#      审计断言由 hermetic 测试（internal/cron TestManualTriggerSamePathAndAudit）
#      承载，本脚本以台账行 + 事件为真机证据。
#   C3 replicas>0 拒绝（架构 §4.3）：cron 服务带 deploy.replicas=2 → 部署
#      拒绝，错误含 E_COMPOSE_UNSUPPORTED + one-shot jobs 文案。
#   C4 非法表达式拒绝：六段含秒表达式 → 拒绝，错误含 E_LABEL_RESERVED
#      （五段标准式契约天然成立，D-CR-1）。
#
# 断言风格与 e2e/nightly 一致（CR-x: PASS/FAIL 行 + NL_FAIL 计数 + finish）。
# usage: e2e/cron.sh
# env:
#   CR_DIND_IMAGE  dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   CR_SKIP_BUILD  1 = 跳过交叉编译，改用 CR_BIN_DIR 下的现成二进制
#   CR_BIN_DIR     CR_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   CR_VERSION     注入的版本串（默认 v0.2.0-cron-e2e）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；台账见 docs/runbooks/image-prepull.md）。
DIND_IMAGE="${CR_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
ALPINE_IMG='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'
CR_SKIP_BUILD="${CR_SKIP_BUILD:-0}"
CR_BIN_DIR="${CR_BIN_DIR:-}"
CR_VERSION="${CR_VERSION:-v0.2.0-cron-e2e}"

BR_NET=fleetly-cron-br
BR_SUBNET=10.216.0.0/24
DIND=fleetly-cron-dind
APP=cronapp
SVC=task
WEB_SVC=web

NL_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

nl() { printf '[cron-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    NL_FAIL=$((NL_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
fatal() { nl "FATAL $*"; exit 1; }
finish() {
    if [ "$NL_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$NL_FAIL"
    exit 1
}

# poll_until <cap-s> <sh-test...> — 每 2s 重试一条宿主侧测试命令直到 rc=0。
poll_until() {
    cap=$1
    shift
    i=0
    while [ "$i" -lt "$cap" ]; do
        if "$@"; then return 0; fi
        i=$((i + 2))
        sleep 2
    done
    return 1
}

m() { docker exec "$DIND" "$@"; }         # dind 内直跑
msh() { docker exec "$DIND" sh -c "$*"; } # dind 内跑 shell 段
# fcli <args...> — dind 内的 fleetly CLI（gRPC 面 + bootstrap token）。
fcli() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$CR_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# events_grep <pattern> — 现抓事件流快照（busybox timeout 掐断 follow 流）
# 并在快照中检索。watch 是长驻流，快照即「迄今全部事件」。
events_grep() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$CR_TOKEN" \
        "$DIND" sh -c 'timeout 4 /opt/fleetly/bin/fleetly events watch --since-seq 0 > /tmp/cron-events.txt 2>/dev/null; exit 0'
    m grep -q "$1" /tmp/cron-events.txt
}

cleanup() {
    for d in $SUITE_DINDS; do
        docker rm -f "$d" >/dev/null 2>&1 || true
    done
    if [ -n "$ACTIVE_NET" ]; then
        docker network rm "$ACTIVE_NET" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP"
}
on_exit() {
    rc=$?
    if [ "$rc" -ne 0 ] && [ -n "$SUITE_DINDS" ]; then
        nl "suite RED (rc=$rc) — dumping dind log tail"
        docker logs "$DIND" --tail 120 2>&1 | tail -60 || true
    fi
    cleanup
    exit "$rc"
}
trap on_exit EXIT INT TERM

# stage <dind> <local-file> <remote-path> — exec+stdin 直传 + 两侧体积校验 +
# 文本脚本 CR 剥离（run.sh 同款：busybox ash 不执行 CRLF；Windows 检出风险面）。
stage() {
    d=$1
    f=$2
    r=$3
    docker exec -i "$d" sh -c "cat > '$r'" <"$f" || fatal "staging $r"
    hsz=$(wc -c <"$f" | tr -d ' ')
    gsz=$(docker exec "$d" sh -c "wc -c < '$r'" | tr -d ' ')
    [ "$hsz" = "$gsz" ] || fatal "size mismatch for $r: host=$hsz dind=$gsz"
    case "$r" in
    *.sh | *.yaml | *.yml)
        docker exec "$d" sed -i 's/\r$//' "$r" || fatal "strip CR from $r"
        ;;
    esac
}

posix_path() { printf '%s' "$1" | tr '\\' '/'; }
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——原生 go.exe 在 MSYS_NO_PATHCONV=1
# 下拿不到映射；经 cygpath -m 归一为 C:/… 混合形态（sh/go/docker 三方都认）。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || fatal 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || fatal 'docker not on PATH'
command -v go >/dev/null 2>&1 || CR_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$CR_SKIP_BUILD" != '1' ]; then
    nl "cross-compiling linux/amd64 fleetlyd+fleetly ($CR_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CR_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CR_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    CR_BIN_DIR="$TMP"
else
    CR_BIN_DIR="${CR_BIN_DIR:?CR_SKIP_BUILD=1 requires CR_BIN_DIR}"
    nl "using prebuilt binaries from $CR_BIN_DIR"
fi
[ -f "$CR_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $CR_BIN_DIR"
[ -f "$CR_BIN_DIR/fleetly" ] || fatal "fleetly missing in $CR_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
# 防御性清扫：上次崩溃残留会污染断言。
leftovers=$(docker ps -aq --filter name=fleetly-cron- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    nl "WARN removing leftover cron-e2e containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

docker rm -f "$DIND" >/dev/null 2>&1 || true
docker run -d --name "$DIND" --privileged --hostname mgr \
    --network "$BR_NET" --ip 10.216.0.10 "$DIND_IMAGE" >/dev/null ||
    fatal "docker run $DIND"
SUITE_DINDS="$DIND"
i=0
while ! docker exec "$DIND" docker info >/dev/null 2>&1; do
    i=$((i + 2))
    if [ "$i" -ge 90 ]; then
        docker logs "$DIND" --tail 40 || true
        fatal 'inner dockerd not ready within 90s'
    fi
    sleep 2
done
nl "dind $DIND ready (engine $(docker exec "$DIND" docker version --format '{{.Server.Version}}' 2>/dev/null))"

nl 'staging binaries + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$CR_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$CR_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
docker exec "$DIND" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'

# fleetlyd 配置：离线 dind 形态——ACME/git 关闭、base_domain 留空。
cat >"$TMP/config.yaml" <<'EOF'
addr: "0.0.0.0:8420"
grpc:
  addr: "127.0.0.1:8421"
state:
  db_path: "/var/lib/fleetly/fleetly.db"
secrets:
  key_path: "/var/lib/fleetly/fleetly.key"
build:
  cache_dir: "/var/lib/fleetly/build-cache"
  artifacts_dir: "/var/lib/fleetly/build-artifacts"
logs:
  dir: "/var/lib/fleetly/fleetly-logs"
ingress:
  token_file: "/var/lib/fleetly/fleetly-ingress.token"
  cert_dir: "/var/lib/fleetly/fleetly-certs"
  acme:
    enabled: false
engine:
  deploy_timeout_seconds: 300
  observe_seconds: 5
  replicas_below_seconds: 5
  poll_seconds: 1
  drift_interval_seconds: 3600
git:
  enabled: false
logging:
  level: info
EOF
stage "$DIND" "$TMP/config.yaml" /opt/fleetly/etc/config.yaml

nl 'pre-pulling fixture images (pinned digests)'
docker exec "$DIND" docker pull -q "$ALPINE_IMG" >/dev/null || fatal "pull $ALPINE_IMG"

docker exec "$DIND" docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/cron-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
CR_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if m sh -c 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        CR_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$CR_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/cron-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
CR_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
[ -n "$CR_TOKEN" ] || fatal 'empty bootstrap token'
nl 'fleetlyd live (liveness 200), bootstrap token read'

# ───────────────── C0: 混合应用部署（长驻 web + cron task）
nl '=== C0: mixed app deploy (web long-running + task cron schedule) ==='
cat >"$TMP/app-compose.yaml" <<EOF
name: $APP
services:
  $WEB_SVC:
    image: $ALPINE_IMG
    command: ["sleep", "31536000"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 2s
      timeout: 1s
      retries: 100
      start_period: 0s
  $SVC:
    image: $ALPINE_IMG
    command: ["sh", "-c", "echo cron-log-marker-\$(date +%s); sleep 3"]
    labels:
      fleetly.cron: "* * * * *"
EOF
stage "$DIND" "$TMP/app-compose.yaml" /opt/fleetly/app-compose.yaml

C0_OUT=$(fcli deploy --timeout 180s /opt/fleetly/app-compose.yaml 2>&1)
case "$C0_OUT" in
*failed* | *E_*) fail "CR-C0 MIXED_APP_DEPLOY" "deploy did not succeed: $(printf '%s' "$C0_OUT" | tail -3)" ;;
*) assert "CR-C0 MIXED_APP_DEPLOY" 0 ;;
esac

web_running() {
    msh "docker service ps fleetly-$APP-$WEB_SVC --format '{{.CurrentState}}' | grep -q '^Running'" 2>/dev/null
}
if poll_until 120 web_running; then
    assert "CR-C0a WEB_SERVICE_RUNNING" 0
else
    m docker service ls || true
    assert "CR-C0a WEB_SERVICE_RUNNING" 1 "long-running web service never ran"
fi

# 只声明不部署：cron 服务不得以长驻形态创建（fleetly-cronapp-task 不存在）。
cron_longrunning_absent() {
    msh "docker service ls --format '{{.Name}}'" 2>/dev/null | grep -qx "fleetly-$APP-$SVC"
    [ $? -ne 0 ]
}
poll_until 10 cron_longrunning_absent
assert "CR-C0b CRON_SERVICE_NOT_LONGRUNNING" $? "service fleetly-$APP-$SVC must not exist as a long-running service"

# ───────────────── C1: 到点触发 + 完成收口 + job 服务删除
nl '=== C1: scheduled fire -> succeeded -> job service removed ==='
if poll_until 120 events_grep cron.triggered; then
    assert "CR-C1a TRIGGERED_EVENT" 0
else
    events_grep cron || true
    m grep -c cron /tmp/cron-events.txt 2>/dev/null || true
    assert "CR-C1a TRIGGERED_EVENT" 1 "cron.triggered never appeared within 120s"
fi

if poll_until 90 events_grep cron.succeeded; then
    assert "CR-C1b SUCCEEDED_EVENT" 0
else
    assert "CR-C1b SUCCEEDED_EVENT" 1 "cron.succeeded never appeared within 90s"
fi

runs_succeeded() {
    fcli cron runs --json "$APP" "$SVC" 2>/dev/null | grep -q '"status": "succeeded"'
}
if poll_until 60 runs_succeeded; then
    assert "CR-C1c RUN_LEDGER_SUCCEEDED_ROW" 0
else
    fcli cron runs --json "$APP" "$SVC" || true
    assert "CR-C1c RUN_LEDGER_SUCCEEDED_ROW" 1 "no succeeded row in cron_runs ledger"
fi

job_service_removed() {
    msh "docker service ls --format '{{.Name}}'" 2>/dev/null | grep -q '^fleetly-cron-'
    [ $? -ne 0 ]
}
if poll_until 60 job_service_removed; then
    assert "CR-C1d JOB_SERVICE_REMOVED" 0
else
    m docker service ls || true
    assert "CR-C1d JOB_SERVICE_REMOVED" 1 "one-shot job service still present after completion"
fi

# ───────────────── C1e: job 输出行进现有日志采集面（验收五项之「日志」）
nl '=== C1e: job output captured by the existing log pipeline ==='
# job 服务存活期（sleep 3 + 完成检测拍，秒级到拍级）内采集器从零全量回读，
# 行按 (app, compose 服务) 归属进湖——SearchLogs 读面即断言面。修正
#（2026-09-23 W1-S5 全量回归）：断言面原为 `logs history`，那是磁盘 JSONL
# 读面；E6 W5-S1 起 victorialogs 为缺省后端，容器行只入湖不落盘
#（internal/logs/manager.go「双写不留，磁盘不翻倍」，设计 §2.3），history
# 面对容器日志在缺省形态下恒空——断言面随平台切换改统一检索
#（keyword + service 过滤，归属语义不变），logs-victorialogs.sh B6 同款。
job_log_captured() {
    fcli logs search --keyword 'cron-log-marker-' --service "$SVC" --source container "$APP" 2>/dev/null | grep -q 'cron-log-marker-'
}
if poll_until 120 job_log_captured; then
    assert "CR-C1e JOB_LOG_IN_PIPELINE" 0
else
    fcli logs search --keyword 'cron-log-marker-' --service "$SVC" "$APP" || true
    assert "CR-C1e JOB_LOG_IN_PIPELINE" 1 "job output never appeared in the logs search face"
fi

# ───────────────── C2: 手动触发（同一路径）
nl '=== C2: manual trigger via CLI (same trigger chain) ==='
BEFORE_RUNS=$(fcli cron runs --json "$APP" "$SVC" 2>/dev/null | grep -c '"id"')
C2_OUT=$(fcli cron trigger "$APP" "$SVC" 2>&1)
# 同路径语义：撞上在途的到点 run 时返回 skipped(overlap) 行——同属触发链的
# 合法回执（与调度器处置一致）。
case "$C2_OUT" in
*"status: started"* | *"status: succeeded"* | *"status: skipped"*)
    assert "CR-C2a MANUAL_TRIGGER_RUN" 0 ;;
*)
    fail "CR-C2a MANUAL_TRIGGER_RUN" "unexpected trigger output: $(printf '%s' "$C2_OUT" | tail -3)" ;;
esac
manual_row_landed() {
    now_runs=$(fcli cron runs --json "$APP" "$SVC" 2>/dev/null | grep -c '"id"')
    [ "${now_runs:-0}" -gt "${BEFORE_RUNS:-0}" ]
}
if poll_until 60 manual_row_landed; then
    assert "CR-C2b MANUAL_RUN_LEDGER_ROW" 0
else
    fcli cron runs --json "$APP" "$SVC" || true
    assert "CR-C2b MANUAL_RUN_LEDGER_ROW" 1 "manual trigger did not land a ledger row"
fi

# ───────────────── C3: replicas>0 拒绝（架构 §4.3 声明行）
nl '=== C3: replicas>0 with fleetly.cron refused ==='
cat >"$TMP/replicas-compose.yaml" <<EOF
name: $APP
services:
  bad:
    image: $ALPINE_IMG
    command: ["sleep", "3"]
    deploy:
      replicas: 2
    labels:
      fleetly.cron: "* * * * *"
EOF
stage "$DIND" "$TMP/replicas-compose.yaml" /opt/fleetly/replicas-compose.yaml
C3_OUT=$(fcli deploy --timeout 60s /opt/fleetly/replicas-compose.yaml 2>&1)
case "$C3_OUT" in
*E_COMPOSE_UNSUPPORTED*one-shot\ jobs* | *E_COMPOSE_UNSUPPORTED*one-shot*)
    assert "CR-C3 REPLICAS_GT0_REFUSED" 0 ;;
*)
    fail "CR-C3 REPLICAS_GT0_REFUSED" "deploy output missing E_COMPOSE_UNSUPPORTED/one-shot refusal: $(printf '%s' "$C3_OUT" | tail -3)" ;;
esac

# ───────────────── C4: 六段含秒表达式拒绝（五段标准式契约）
nl '=== C4: 6-field expression refused ==='
cat >"$TMP/badexpr-compose.yaml" <<EOF
name: $APP
services:
  bad:
    image: $ALPINE_IMG
    command: ["sleep", "3"]
    labels:
      fleetly.cron: "* * * * * *"
EOF
stage "$DIND" "$TMP/badexpr-compose.yaml" /opt/fleetly/badexpr-compose.yaml
C4_OUT=$(fcli deploy --timeout 60s /opt/fleetly/badexpr-compose.yaml 2>&1)
case "$C4_OUT" in
*E_LABEL_RESERVED*)
    assert "CR-C4 SIX_FIELD_EXPRESSION_REFUSED" 0 ;;
*)
    fail "CR-C4 SIX_FIELD_EXPRESSION_REFUSED" "deploy output missing E_LABEL_RESERVED: $(printf '%s' "$C4_OUT" | tail -3)" ;;
esac

finish
