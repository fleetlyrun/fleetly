#!/bin/sh
# e2e/notifications.sh — E6 W5-S4 通知 Webhook 端到端（单节点 dind 形态；
# 设计 docs/design/2026-09-22-observability.md §5 的真机闭环，V2-6 Webhook
# 首发——POST + 重试 + 事件订阅；metrics.sh 同骨架——编排自足、镜像钉
# digest）：宿主 bridge 上起一个特权 dind → swarm init + fleetlyd 起服 →
# 全部断言经 dind 内的 fleetly CLI / REST / nc receiver 驱动：
#
#   receiver 形态：dind 容器内 busybox `nc -l -p <port>` 循环——预先备好
#   HTTP 200 响应文件作 stdin，请求原文（头 + 体）append 到 /tmp/recv-<p>.log
#   （fleetlyd 与 receiver 同 netns → 端点 URL 用 127.0.0.1；三端口隔离：
#   8899 = ops（deployment.*）、8898 = star（*）、59991 = dead（logs.*，
#   不可达端口）。验签在宿主侧用 python 独立重算 HMAC（不是 grep 签名存在
#   ——设计 §7 S4 验收原文）。
#
#   N1 端点创建：secret 明文一次性返回（三个端点三把不同密钥）。
#   N2-N3 订阅命中：部署 whoami 应用 → deployment.* 事件 → ops 与 star
#      receiver 都收到带签名的 POST。
#   N4-N5 HMAC 验签：宿主侧用创建响应的明文密钥重算
#      sha256=hex(HMAC-SHA256(secret, ts + "." + body)) 与
#      X-Fleetly-Signature 头比对（两把密钥各自通过——per 端点密钥隔离）。
#   N6 订阅纪律：logs.backend_updated（logs.*）→ star（*）收到；ops
#      （deployment.*）不收到（模式过滤不误投）。
#   N7 重试路径：dead 端点（不可达端口）→ 台账 attempts 增长（30s 退避
#      窗内 1→2）→ 3 次尝试耗尽终态 failed（5m 退避窗——预算 8 分钟）。
#   N8 组件红：system status 的 notifications 组件红（REST 断言 ok:false
#      且 error 带端点名）。
#   N9 恢复绿：停用 dead 端点 → notifications 组件回绿。
#   N10 test 载荷：notifications test → type=test 载荷到达 ops receiver。
#   N11 台账读面：deliveries --status ok 显示 response_code=200。
#   N12 零回环（设计红线）：事件流快照无任何 notify.* 事件——通知自身
#      零事件（订阅 * 不会把自己套进回环）。
#
# 断言风格与 e2e/metrics.sh 一致（NOT-x: PASS/FAIL 行 + NL_FAIL 计数 +
# finish）。
# usage: e2e/notifications.sh
# env:
#   NOT_DIND_IMAGE   dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   NOT_SKIP_BUILD   1 = 跳过交叉编译，改用 NOT_BIN_DIR 下的现成二进制
#   NOT_BIN_DIR      NOT_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   NOT_VERSION      注入的版本串（默认 v0.2.0-notifications-e2e）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；与 e2e/metrics.sh 同源）。
DIND_IMAGE="${NOT_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
WHOAMI_IMG='traefik/whoami:v1.10.4@sha256:02d8fe035f170f91cbb5e458a57f4cefab747436f8244a0eb2d66785fe5e565f'
NOT_SKIP_BUILD="${NOT_SKIP_BUILD:-0}"
NOT_BIN_DIR="${NOT_BIN_DIR:-}"
NOT_VERSION="${NOT_VERSION:-v0.2.0-notifications-e2e}"

BR_NET=fleetly-n-br
BR_SUBNET=10.218.0.0/24
DIND=fleetly-n-e2e-dind
APP=notifyapp
SVC=web
OPS_PORT=8899
STAR_PORT=8898
DEAD_PORT=59991

NL_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

nl() { printf '[not-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
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
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_PROJECT="$FOUNDER_PROJECT" -e FLEETLY_TOKEN="$NOT_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# events_grep <pattern> — 事件流快照检索（watch 快照即「迄今全部事件」）。
events_grep() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$NOT_TOKEN" \
        "$DIND" sh -c 'timeout 4 /opt/fleetly/bin/fleetly events watch --since-seq 0 > /tmp/not-events.txt 2>/dev/null; exit 0'
    m grep -q "$1" /tmp/not-events.txt
}
# host_python — 宿主侧 python 解释器（Git Bash 与 CI ubuntu 均有）。
host_python() {
    if command -v python >/dev/null 2>&1; then python "$@"; else python3 "$@"; fi
}
# fetch_json_secret <json> — 从 protojson 创建响应提取 secret。
fetch_json_secret() {
    printf '%s' "$1" | host_python -c 'import json,sys;print(json.load(sys.stdin)["secret"])'
}
# fetch_json_field <json> <field> — 通用单字段提取（endpoint.x 取法：传 "endpoint" 调用方自行再取）。
# start_receiver <port> — dind 内起一个 nc 接收循环（请求原文 append 落盘）。
start_receiver() {
    port=$1
    m sh -c "printf 'HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n' > /tmp/resp-$port.txt"
    rm -f "$TMP/recv-$port.log"
    docker exec -d "$DIND" sh -c "while true; do nc -l -p $port < /tmp/resp-$port.txt >> /tmp/recv-$port.log 2>/dev/null; sleep 0.05; done"
}
# pull_log <port> — 把 dind 内的接收日志拷回宿主（$TMP/recv-<port>.log）。
pull_log() {
    port=$1
    m sh -c "cat /tmp/recv-$port.log 2>/dev/null" >"$TMP/recv-$port.log" 2>/dev/null || true
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
}
trap on_exit EXIT INT TERM

# stage <dind> <local-file> <remote-path> — exec+stdin 直传 + 两侧体积校验 +
# 文本脚本 CR 剥离（busybox ash 不执行 CRLF）。
stage() {
    d=$1
    f=$2
    r=$3
    docker exec -i "$d" sh -c "cat > '$r'" <"$f" || fatal "staging $r"
    hsz=$(wc -c <"$f" | tr -d ' ')
    gsz=$(docker exec "$d" sh -c "wc -c < '$r'" | tr -d ' ')
    [ "$hsz" = "$gsz" ] || fatal "size mismatch for $r: host=$hsz dind=$gsz"
    case "$r" in
    *.sh | *.yaml | *.yml | *Dockerfile)
        docker exec "$d" sed -i 's/\r$//' "$r" || fatal "strip CR from $r"
        ;;
    esac
}

posix_path() { printf '%s' "$1" | tr '\\' '/'; }
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || fatal 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || fatal 'docker not on PATH'
command -v go >/dev/null 2>&1 || NOT_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$NOT_SKIP_BUILD" != '1' ]; then
    nl "cross-compiling linux/amd64 fleetlyd+fleetly ($NOT_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$NOT_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$NOT_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    NOT_BIN_DIR="$TMP"
else
    NOT_BIN_DIR="${NOT_BIN_DIR:?NOT_SKIP_BUILD=1 requires NOT_BIN_DIR}"
    nl "using prebuilt binaries from $NOT_BIN_DIR"
fi
[ -f "$NOT_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $NOT_BIN_DIR"
[ -f "$NOT_BIN_DIR/fleetly" ] || fatal "fleetly missing in $NOT_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
leftovers=$(docker ps -aq --filter name=fleetly-n-e2e- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    nl "WARN removing leftover not-e2e containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

docker rm -f "$DIND" >/dev/null 2>&1 || true
docker run -d --name "$DIND" --privileged --hostname mgr \
    --network "$BR_NET" --ip 10.218.0.10 "$DIND_IMAGE" >/dev/null ||
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

# 预拉 whoami（部署事件的载体）。
nl 'pre-pulling fixture images (pinned digests)'
docker exec "$DIND" docker pull -q "$WHOAMI_IMG" >/dev/null || fatal "pull $WHOAMI_IMG"

nl 'staging binaries + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$NOT_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$NOT_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
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

docker exec "$DIND" docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/not-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
NOT_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if m sh -c 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        NOT_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$NOT_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/not-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
NOT_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
[ -n "$NOT_TOKEN" ] || fatal 'empty bootstrap token'
nl 'fleetlyd live (liveness 200), bootstrap token read'
# ── v0.3 归属管道 fixture（rbac-teams §2.1/§2.3/§3.4）：注册 founder（首
# 用户 = 平台管理员 + 个人队 + 默认项目 default）→ 会话自服务铸用户 PAT
#（admin scope——founder 是平台管理员可达集；CLI 不消费会话 cookie）。
# 首次部署/建库经 FLEETLY_PROJECT=founder/default 显式携带项目归属；fcli
# 统一 env 注入。bootstrap token 已随首用户注册按设计吊销弃用。
CURLER="$DIND-curl"
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" sleep 100000 >/dev/null ||
    fatal "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://10.218.0.10:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'
docker exec "$CURLER" curl -s -c /tmp/jar -X POST "http://10.218.0.10:8420/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"founder@e2e.test","password":"founder-pass-1","display_name":"Founder"}' \
    >/dev/null || fatal 'founder register'
# W2-S5 夹具修正：凭据改铸 **machine 令牌**（平台级凭据 = 资源面 admin
# 等价，rbac-teams §2.3；W2-S4 起平台管理员在资源面被 ResolvePermission
# 短路为只读——founder 的用户 PAT 已不能再承担部署/资源写；webhook 端点
# CRUD 属 admin scope 面，机具凭据按 scope 门放行，语义不变）。
NOT_TOKEN=$(docker exec "$CURLER" curl -s -b /tmp/jar -X POST "http://10.218.0.10:8420/v1/tokens" \
    -H 'Content-Type: application/json' \
    -d '{"machine":true,"note":"e2e machine token","scopes":["admin"]}' | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$NOT_TOKEN" ] || fatal 'machine token mint failed'
# curl helper 用毕即除（fixture 只承担注册与铸 token；避免钉住 bridge 网络影响后续套件）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
FOUNDER_TEAM=founder
FOUNDER_PRJ=default
FOUNDER_PROJECT="$FOUNDER_TEAM/$FOUNDER_PRJ"
nl 'founder registered (platform admin); machine token minted; project context '"'"'"$FOUNDER_PROJECT"'"'"''


# 缺省日志后端 = victorialogs（默认捆绑）——本套件不消费检索面，切 jsonl
# 免去 VL 镜像拉取与 ingest 降级事件噪声（logs.backend_updated 事件本身
# 也是 N6 的触发器之一）。
logs_backend_ok() {
    fcli logs backend set jsonl >/dev/null 2>&1
}
poll_until 30 logs_backend_ok || fatal 'logs backend set jsonl never accepted'

# receiver 就位（三个端口：ops/star/dead）。
start_receiver "$OPS_PORT"
start_receiver "$STAR_PORT"
nl "receivers up on $OPS_PORT/$STAR_PORT (dead endpoint targets 127.0.0.1:$DEAD_PORT)"

# ───────── N1: 端点创建（secret 明文一次性返回）
nl '=== N1: endpoint create returns the secret once ==='
NOT_OPS_JSON=$(fcli notifications endpoint create --json --patterns 'deployment.*' ops "http://127.0.0.1:$OPS_PORT/hook") || fatal 'create ops endpoint'
NOT_OPS_SECRET=$(fetch_json_secret "$NOT_OPS_JSON")
NOT_STAR_JSON=$(fcli notifications endpoint create --json --patterns '*' star-relay "http://127.0.0.1:$STAR_PORT/hook") || fatal 'create star endpoint'
NOT_STAR_SECRET=$(fetch_json_secret "$NOT_STAR_JSON")
NOT_DEAD_JSON=$(fcli notifications endpoint create --json --patterns 'logs.*' dead-relay "http://127.0.0.1:$DEAD_PORT/hook") || fatal 'create dead endpoint'
NOT_DEAD_SECRET=$(fetch_json_secret "$NOT_DEAD_JSON")
[ -n "$NOT_OPS_SECRET" ] && [ -n "$NOT_STAR_SECRET" ] && [ -n "$NOT_DEAD_SECRET" ] || fatal 'empty secrets'
# 三把密钥互不相同（per 端点密钥）。
[ "$NOT_OPS_SECRET" != "$NOT_STAR_SECRET" ] && [ "$NOT_OPS_SECRET" != "$NOT_DEAD_SECRET" ] || fatal 'secrets must differ per endpoint'
assert "NOT-N1 ENDPOINT_CREATE_RETURNS_SECRET" 0

# 清单只出指纹不出明文。
LIST_OUT=$(fcli notifications endpoint list 2>/dev/null)
list_hides_secret() {
    printf '%s' "$LIST_OUT" | grep -q "$NOT_OPS_SECRET" && return 1
    printf '%s' "$LIST_OUT" | grep -q 'secret=0123\|secret=' || return 1
    return 0
}
if list_hides_secret; then
    assert "NOT-N1b LIST_HIDES_SECRET_SHOWS_FINGERPRINT" 0
else
    printf '%s\n' "$LIST_OUT" || true
    assert "NOT-N1b LIST_HIDES_SECRET_SHOWS_FINGERPRINT" 1 "list leaked the plaintext secret or lacks fingerprints"
fi

# ───────── N2-N3: 订阅命中（deployment.* → ops + star 收到带签名 POST）
nl '=== N2: app deploy fires deployment.* deliveries ==='
cat >"$TMP/app-compose.yaml" <<EOF
name: $APP
services:
  $SVC:
    image: $WHOAMI_IMG
    deploy:
      replicas: 1
    expose: ["80"]
    healthcheck:
      test: ["NONE"]
EOF
stage "$DIND" "$TMP/app-compose.yaml" /opt/fleetly/not-compose.yaml
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$NOT_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c '/opt/fleetly/bin/fleetly deploy --timeout 300s /opt/fleetly/not-compose.yaml > /tmp/not-deploy.log 2>&1'
deploy_succeeded() {
    fcli deployments list --json "$APP" 2>/dev/null | grep -q '"status": "succeeded"'
}
if poll_until 300 deploy_succeeded; then
    assert "NOT-N2a APP_DEPLOY_SUCCEEDED" 0
else
    fcli deployments list --json "$APP" 2>/dev/null || true
    msh 'cat /tmp/not-deploy.log' || true
    assert "NOT-N2a APP_DEPLOY_SUCCEEDED" 1 "deployment never succeeded"
fi

ops_received() {
    pull_log "$OPS_PORT"
    grep -q 'X-Fleetly-Signature: sha256=' "$TMP/recv-$OPS_PORT.log" 2>/dev/null
}
if poll_until 120 ops_received; then
    assert "NOT-N2b OPS_RECEIVER_GOT_SIGNED_POST" 0
else
    msh "cat /tmp/recv-$OPS_PORT.log 2>/dev/null" || true
    assert "NOT-N2b OPS_RECEIVER_GOT_SIGNED_POST" 1 "ops receiver never saw a signed POST"
fi
star_received() {
    pull_log "$STAR_PORT"
    grep -q 'X-Fleetly-Signature: sha256=' "$TMP/recv-$STAR_PORT.log" 2>/dev/null
}
if poll_until 120 star_received; then
    assert "NOT-N3 STAR_RECEIVER_GOT_SIGNED_POST" 0
else
    msh "cat /tmp/recv-$STAR_PORT.log 2>/dev/null" || true
    assert "NOT-N3 STAR_RECEIVER_GOT_SIGNED_POST" 1 "star receiver never saw a signed POST"
fi

# ───────── N4-N5: 宿主侧 HMAC 独立重算验签（真实重算，非 grep 签名存在）
nl '=== N4: host-side HMAC recompute over the captured request ==='
cat >"$TMP/verify_hmac.py" <<'EOF'
import hashlib, hmac, re, sys
log_path, secret = sys.argv[1], sys.argv[2]
raw = open(log_path, "rb").read()
head, _, body = raw.rpartition(b"\r\n\r\n")  # 最后一条请求：头在前、体在后
ts_matches = re.findall(rb"X-Fleetly-Timestamp: (\d+)", head)
sig_matches = re.findall(rb"X-Fleetly-Signature: sha256=([0-9a-f]+)", head)
if not ts_matches or not sig_matches:
    print("MISMATCH missing-headers")
    sys.exit(1)
ts = ts_matches[-1].decode()
sig = sig_matches[-1].decode()
mac = hmac.new(secret.encode(), ts.encode() + b"." + body, hashlib.sha256).hexdigest()
print("VERIFY-OK" if hmac.compare_digest(mac, sig) else f"VERIFY-FAIL want={mac} got={sig}")
EOF
pull_log "$OPS_PORT"
OPS_VERIFY=$(host_python "$TMP/verify_hmac.py" "$TMP/recv-$OPS_PORT.log" "$NOT_OPS_SECRET")
nl "ops verify: $OPS_VERIFY"
[ "$OPS_VERIFY" = "VERIFY-OK" ]
assert "NOT-N4 OPS_HMAC_VERIFY_OK" $?
pull_log "$STAR_PORT"
STAR_VERIFY=$(host_python "$TMP/verify_hmac.py" "$TMP/recv-$STAR_PORT.log" "$NOT_STAR_SECRET")
nl "star verify: $STAR_VERIFY"
[ "$STAR_VERIFY" = "VERIFY-OK" ]
assert "NOT-N5 STAR_HMAC_VERIFY_OK" $?

# ───────── N6: 订阅纪律（logs.* 只进 star（*），不进 ops（deployment.*））
nl '=== N6: pattern discipline across a logs.* event ==='
logs_event_ok() {
    fcli logs backend set jsonl >/dev/null 2>&1 &&
        sleep 3 && pull_log "$STAR_PORT" &&
        grep -q 'logs.backend_updated' "$TMP/recv-$STAR_PORT.log" 2>/dev/null
}
if poll_until 60 logs_event_ok; then
    assert "NOT-N6a STAR_GETS_LOGS_EVENT" 0
else
    assert "NOT-N6a STAR_GETS_LOGS_EVENT" 1 "star receiver never saw logs.backend_updated"
fi
pull_log "$OPS_PORT"
if grep -q 'logs.backend_updated' "$TMP/recv-$OPS_PORT.log" 2>/dev/null; then
    assert "NOT-N6b OPS_PATTERN_FILTER_DISCIPLINE" 1 "ops (deployment.*) received a logs.* event"
else
    assert "NOT-N6b OPS_PATTERN_FILTER_DISCIPLINE" 0
fi

# ───────── N7: 重试路径（不可达端口 → attempts 增长 → 3 次终败）
nl '=== N7: dead endpoint climbs the retry ladder to terminal failure ==='
attempts_two() {
    fcli notifications deliveries --json --endpoint dead-relay --status pending 2>/dev/null |
        grep -q '"attempts": 2'
}
if poll_until 90 attempts_two; then
    assert "NOT-N7a RETRY_BACKOFF_GROWS_ATTEMPTS" 0
else
    fcli notifications deliveries --endpoint dead-relay || true
    assert "NOT-N7a RETRY_BACKOFF_GROWS_ATTEMPTS" 1 "attempts never reached 2 within the 30s backoff window"
fi
terminal_failed() {
    fcli notifications deliveries --json --endpoint dead-relay --status failed 2>/dev/null |
        grep -q '"attempts": 3'
}
if poll_until 480 terminal_failed; then
    assert "NOT-N7b TERMINAL_FAILED_AFTER_THREE_ATTEMPTS" 0
else
    fcli notifications deliveries --endpoint dead-relay || true
    assert "NOT-N7b TERMINAL_FAILED_AFTER_THREE_ATTEMPTS" 1 "dead endpoint never reached terminal failure"
fi

# ───────── N8: notifications 组件红（Error 带端点名）
nl '=== N8: notifications component goes red ==='
cat >"$TMP/check_component.py" <<'EOF'
import json, sys
# 网关 marshaler EmitUnpopulated=false：ok=false（bool 零值）不出现在
# JSON——「ok 键缺席」与「ok=false」同为红，仅 ok=true 是绿。
status = json.load(open(sys.argv[1], encoding="utf-8"))
for c in status.get("components") or []:
    if c.get("name") == "notifications":
        state = "green" if c.get("ok") is True else "red"
        print("COMPONENT", state, "|", c.get("error") or "")
        sys.exit(0)
print("COMPONENT missing")
EOF
component_state() {
    m wget -q -T 3 -O - --header="Authorization: Bearer $NOT_TOKEN" http://127.0.0.1:8420/v1/system/status \
        >"$TMP/status.json" 2>/dev/null || return 1
    host_python "$TMP/check_component.py" "$TMP/status.json"
}
component_red() {
    component_state | grep -q "COMPONENT red"
}
if poll_until 60 component_red; then
    component_state | sed 's/^COMPONENT //' | while IFS= read -r line; do nl "component: $line"; done
    assert "NOT-N8 NOTIFICATIONS_COMPONENT_RED" 0
else
    assert "NOT-N8 NOTIFICATIONS_COMPONENT_RED" 1 "notifications component never went red"
fi

# ───────── N9: 恢复绿（停用终败端点 → 组件回绿）
nl '=== N9: disabling the failing endpoint turns the component green ==='
component_green() {
    component_state | grep -q "COMPONENT green"
}
disable_ok() {
    fcli notifications endpoint disable dead-relay >/dev/null 2>&1
}
poll_until 30 disable_ok || fatal 'disable dead-relay never accepted'
if poll_until 60 component_green; then
    assert "NOT-N9 DISABLE_TURNS_COMPONENT_GREEN" 0
else
    component_state || true
    assert "NOT-N9 DISABLE_TURNS_COMPONENT_GREEN" 1 "notifications component never recovered"
fi

# ───────── N10: test 载荷（type=test 到达 ops receiver）
nl '=== N10: notifications test sends the type=test payload ==='
test_delivered() {
    fcli notifications test ops >/dev/null 2>&1 && sleep 2 && pull_log "$OPS_PORT" &&
        grep -q '"type":"test"' "$TMP/recv-$OPS_PORT.log" 2>/dev/null
}
if poll_until 90 test_delivered; then
    assert "NOT-N10 TEST_PAYLOAD_DELIVERED" 0
else
    fcli notifications test ops || true
    assert "NOT-N10 TEST_PAYLOAD_DELIVERED" 1 "type=test payload never arrived"
fi

# ───────── N11: 台账读面（ok 行带 response_code）
nl '=== N11: delivery ledger shows response codes ==='
ledger_ok_200() {
    fcli notifications deliveries --endpoint ops --status ok 2>/dev/null |
        grep -q 'response_code=200'
}
if poll_until 60 ledger_ok_200; then
    assert "NOT-N11 LEDGER_SHOWS_RESPONSE_CODE" 0
else
    fcli notifications deliveries --endpoint ops || true
    assert "NOT-N11 LEDGER_SHOWS_RESPONSE_CODE" 1 "ok deliveries never recorded response_code=200"
fi

# ───────── N12: 零回环（设计红线：事件流无 notify.* 事件）
nl '=== N12: zero notify.* events (no self-trigger loop) ==='
no_notify_events() {
    events_grep 'NOTGREPPED' >/dev/null 2>&1
    ! m grep -q 'notify\.' /tmp/not-events.txt 2>/dev/null
}
if poll_until 30 no_notify_events; then
    assert "NOT-N12 ZERO_NOTIFY_EVENTS_NO_LOOP" 0
else
    msh 'grep -c "notify\." /tmp/not-events.txt' || true
    assert "NOT-N12 ZERO_NOTIFY_EVENTS_NO_LOOP" 1 "notify.* events appeared in the stream (self-trigger loop!)"
fi

finish
