#!/bin/sh
# e2e/auth.sh — v0.3 W1 认证面端到端（rbac-teams 设计 docs/design/
# 2026-09-23-rbac-teams.md §2/§8「e2e 新增 auth.sh」；terminal.sh 同骨架——
# 单 dind、编排自足、镜像钉 digest）。前置 S1-S4 已落：注册/登录/会话
# cookie/PAT/CLI auth/首用户=平台管理员+个人队+默认项目+bootstrap token
# 注册即吊销。
#
#   镜像（钉定口径）：dind 钉 digest 与 CI 一致；REST 会话断言需要真
#   curl cookie jar（dind 内 busybox wget 无 cookie 语义、无 curl），故
#   另起一只钉 digest 的 curlimages/curl helper 容器挂同一宿主 bridge，
#   以 jar 文件模拟设备。本套件不部署任何受管应用——dind 内 docker
#   pull 一次都不发生（离线友好，无需 ghcr 凭据）。
#
#   AUTH-1 fresh 起服注册窗口：GET /v1/auth/registration → open=true +
#          has_users=false（无用户窗口恒开）。
#   AUTH-2 首用户注册（REST）：200 + is_platform_admin=true + 会话
#          Set-Cookie；Me 投影个人队 owner；注册同事务副作用的
#          user.registered / team.created / project.created 事件在流
#          （payload 含默认项目 slug `default`——W1 无 projects 读面，
#          事件流即披露面；cron.sh C2 同口径）。
#   AUTH-3 bootstrap token 注册即吊销：注册前 REST list apps（Bearer
#          bootstrap）200 + CLI（gRPC 面）通；注册后 REST 401 + CLI 拒。
#   AUTH-4 注册窗口开关：第二用户注册 → 403 E_REGISTRATION_CLOSED；
#          registration 状态面 open=false/has_users=true；平台管理员
#          PUT open=true 后注册成功且 is_platform_admin=false；切回
#          closed 再拒。
#   AUTH-5 登录/会话生命周期：两设备（两只 cookie jar）登录 → 错口令
#          401 → 设备 A Logout 后 A 401 而 B 200 → 设备 B LogoutAll
#          后 B 401（sessions_revoked ≥1）。
#   AUTH-6 登录限流：同 email 连续错口令 → 第 11 次起 429（10/min 双键
#          email+IP；探针用全新 email 桶 + 请求同源，双键同步耗尽——
#          断言前 settle 65s 让双键全满，12 连发内补充量 <1）。
#   AUTH-7 CLI auth 链（**W1 退化形态**，见下方「诚实范围」）：
#          a 机具令牌被 auth login 拒收（"platform machine token" 文案
#            + 非零退出）；b 机具令牌经 FLEETLY_TOKEN 走 apps list 通
#            （机具凭据的 CLI 正面）；c auth status 对机具令牌如实投影
#            「machine token」态；d 无凭据 status exit 1 + "not logged
#            in"；e auth logout 幂等（无凭据也成功收尾）。
#   AUTH-8 平台管理员用户管理：CreateUser（临时口令一次性返回）→ 新
#          用户登录成功 → DisableUser → 旧会话与再登录双拒 →
#          EnableUser 恢复登录 → ResetPassword → 旧口令 401、重置前
#          会话全灭、新口令 200（重置即全端下线，S1 悬置裁决收口）。
#   AUTH-9 审计锚点：**跳过**（如实注明）。audit_log 直查在 e2e 无先例
#          （dind 内无 sqlite3；引入即新增镜像/apk 网络依赖），W1 亦无
#          审计读面（ListAudit 属 W3）——auth.* 审计行由 internal/api 与
#          internal/state 的 hermetic 单测直读断言（cron.sh C2 同口径）；
#          W3 ListAudit 落地后本套件补 API 面断言。
#
#   诚实范围（单测/e2e 分工）：
#   - AUTH-7 的「用户 PAT 全链」（login 管道收 PAT 落盘 → status Me 投影
#     → logout 清凭据）依赖用户 PAT 的产生面——W1 的
#     TokensService.CreateToken 仍是 admin 全局面（state.TokenWrite 不带
#     user_id = 机具令牌，user PAT 无法经任何 W1 面产生），用户 PAT 自
#     服务随 W2 TokensService 用户化迁移后，本套件补「用户 PAT login
#     落盘全链」正例（解析矩阵/落盘/四态投影已由 CLI 单测钉死）；
#   - 会话 cookie 属性（HttpOnly/SameSite=Lax/Path=/）、XFF 信任语义、
#     并发首注册竞态、无用户窗口竞态：gateway/state 单测钉死（e2e 黑盒
#     面不重复断言头属性）；
#   - 限流的时钟语义（10/min 滑窗恢复曲线）不做精确断言——e2e 只钉
#     「桶内连发 10 次不 429、第 11 次起 429」的确定性截面。
#
# 断言风格与 e2e/terminal.sh 一致（AUTH-x: PASS/FAIL 行 + AUTH_FAIL
# 计数 + finish）。
# usage: e2e/auth.sh
# env:
#   AUTH_DIND_IMAGE  dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   AUTH_SKIP_BUILD  1 = 跳过交叉编译，改用 AUTH_BIN_DIR 下的现成二进制
#   AUTH_BIN_DIR     AUTH_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   AUTH_VERSION     注入的版本串（默认 v0.3.0-auth-e2e）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；与 e2e/terminal.sh 同源。curl helper
#    是本套件唯一新增镜像：REST 会话面需要真 cookie jar 语义）。
DIND_IMAGE="${AUTH_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
AUTH_SKIP_BUILD="${AUTH_SKIP_BUILD:-0}"
AUTH_BIN_DIR="${AUTH_BIN_DIR:-}"
AUTH_VERSION="${AUTH_VERSION:-v0.3.0-auth-e2e}"

BR_NET=fleetly-auth-br
BR_SUBNET=10.222.0.0/24
DIND=fleetly-auth-e2e-dind
CURLER=fleetly-auth-e2e-curl
DIND_IP=10.222.0.10

# 身份口径（fresh 安装演练形态）：founder = 首用户（平台管理员）；mate =
# 开关放行注册的第二用户；rl-probe = 限流探针专用 email（全新桶）；
# worker = CreateUser 通道（平台管理员加人）。口令 ≥8 字符
#（protovalidate 下界）；display_name 空 = 缺省取 email 本地部分——
# founder 的个人队 slug 即 "founder"（nextTeamSlugTx 单词制归一）。
FOUNDER_EMAIL=founder@auth-e2e.test
FOUNDER_PASS=founder-pass-1
MATE_EMAIL=mate@auth-e2e.test
MATE_PASS=mate-pass-1
RL_EMAIL=rl-probe@auth-e2e.test
WORKER_EMAIL=worker@auth-e2e.test

AUTH_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

al() { printf '[auth-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { al "$1: PASS"; }
fail() {
    al "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    AUTH_FAIL=$((AUTH_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
fatal() { al "FATAL $*"; exit 1; }
finish() {
    if [ "$AUTH_FAIL" -eq 0 ]; then
        al "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    al "SUITE-DONE failed-asserts=$AUTH_FAIL"
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
# bcli <args...> — dind 内 CLI，gRPC 面 + bootstrap token（注册前面）。
bcli() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$BOOTSTRAP_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# mcli <args...> — dind 内 CLI，gRPC 面 + 平台机具令牌（注册后 admin 面；
# CLI 不消费会话 cookie——auth.go 头注，会话是浏览器面凭据）。
mcli() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$MACHINE_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# acli <args...> — dind 内 auth 动词链：HOME 隔离（config 落盘面不串台）、
# 不注入 FLEETLY_TOKEN（无凭据态的判定面）；-i 供 login 管道收 token
#（auth.go readPastedToken 的非 TTY 分支）。
acli() {
    docker exec -i -e FLEETLY_ADDR=127.0.0.1:8421 -e HOME=/root/auth-cli-home \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# acli_env <args...> — 同 acli，另注入机具令牌（status 的 env 凭据态）。
acli_env() {
    docker exec -i -e FLEETLY_ADDR=127.0.0.1:8421 -e HOME=/root/auth-cli-home \
        -e FLEETLY_TOKEN="$MACHINE_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
}
# events_grep <pattern> — 事件流快照检索（机具令牌 read 面；watch 快照即
# 「迄今全部事件」，busybox timeout 掐断 follow 流；快照文件在 dind 内。
# 默认人读形态只有 seq/time/name/subject 五列，无 payload）。
events_grep() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$MACHINE_TOKEN" \
        "$DIND" sh -c 'timeout 4 /opt/fleetly/bin/fleetly events watch --since-seq 0 > /tmp/auth-events.txt 2>/dev/null; exit 0'
    m grep -q "$1" /tmp/auth-events.txt
}
# events_grep_json <pattern> — JSONL 帧形态快照（--json 含 payload 字段——
# AUTH-2f 断默认项目 slug 用；帧 = protojson 单行 JSON）。
events_grep_json() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$MACHINE_TOKEN" \
        "$DIND" sh -c 'timeout 4 /opt/fleetly/bin/fleetly events watch --since-seq 0 --json > /tmp/auth-events.jsonl 2>/dev/null; exit 0'
    m grep -q "$1" /tmp/auth-events.jsonl
}
# req <jar-base> <method> <path> <json-body-or-''> [extra-curl-args...] —
# 经 curl helper 容器打 dind REST 面（8420，0.0.0.0 监听；jar 恒读写——
# 注册/登录下发 Set-Cookie，后续调用自动携带）。无返回值：HTTP 状态码落
# 全局 RC、响应体落全局 BODY（直呼不用 $() —— 子_shell 会吞赋值）。
req() {
    jarb=$1
    rmethod=$2
    rpath=$3
    rbody=$4
    shift 4
    set -- -s -o /tmp/req-body.json -w '%{http_code}' -X "$rmethod" \
        -H 'Content-Type: application/json' \
        -b "/tmp/$jarb.jar" -c "/tmp/$jarb.jar" "$@"
    [ -n "$rbody" ] && set -- "$@" -d "$rbody"
    RC=$(docker exec "$CURLER" curl "$@" "http://$DIND_IP:8420$rpath")
    BODY=$(docker exec "$CURLER" cat /tmp/req-body.json 2>/dev/null)
    BODY=${BODY:-'(empty body)'}
}
# sess_of <jar-base> — jar 里的会话值（Netscape 形态第 6/7 列 = name/
# value）；空 = 该设备无会话。
sess_of() {
    docker exec "$CURLER" awk '$6 == "fleetly_session" {print $7}' "/tmp/$1.jar" 2>/dev/null | tail -n 1
}
# settle_buckets <seconds> — 限流沉降（两层 401 防护的复位时间：authservice
# email+IP 双键桶 10/min 补充、全满需 60s；gateway per-IP 固定窗自窗起点
# 1 分钟过期。声明式等待，让后续断言的连发计数只取决于本套件自己的消耗）。
settle_buckets() {
    al "settling auth rate-limit buckets for ${1}s (dual-key refill 10/min)"
    sleep "$1"
}

cleanup() {
    docker rm -f "$CURLER" >/dev/null 2>&1 || true
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
        al "suite RED (rc=$rc) — dumping fleetlyd log tail"
        docker exec "$DIND" sh -c 'tail -60 /tmp/auth-fleetlyd.log 2>/dev/null' || true
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
command -v go >/dev/null 2>&1 || AUTH_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$AUTH_SKIP_BUILD" != '1' ]; then
    al "cross-compiling linux/amd64 fleetlyd+fleetly ($AUTH_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$AUTH_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$AUTH_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    AUTH_BIN_DIR="$TMP"
else
    AUTH_BIN_DIR="${AUTH_BIN_DIR:?AUTH_SKIP_BUILD=1 requires AUTH_BIN_DIR}"
    al "using prebuilt binaries from $AUTH_BIN_DIR"
fi
[ -f "$AUTH_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $AUTH_BIN_DIR"
[ -f "$AUTH_BIN_DIR/fleetly" ] || fatal "fleetly missing in $AUTH_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
leftovers=$(docker ps -aq --filter name=fleetly-auth-e2e- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    al "WARN removing leftover auth-e2e containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

docker rm -f "$DIND" >/dev/null 2>&1 || true
docker run -d --name "$DIND" --privileged --hostname mgr \
    --network "$BR_NET" --ip "$DIND_IP" "$DIND_IMAGE" >/dev/null ||
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
al "dind $DIND ready (engine $(docker exec "$DIND" docker version --format '{{.Server.Version}}' 2>/dev/null))"

al 'staging binaries + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$AUTH_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$AUTH_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
docker exec "$DIND" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'

# fleetlyd 配置（离线 dind 形态——ACME/git 关闭；无受管应用部署，引擎面
# 只承担 fleetlyd 的启动前提，与既有套件同构）。
cat >"$TMP/config.yaml" <<'EOF'
addr: "0.0.0.0:8420"
grpc:
  addr: "0.0.0.0:8421"
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

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/auth-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
CTL_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if msh 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        CTL_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$CTL_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/auth-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
# bootstrap token 从落盘文件取（internal/runtime/bootstrap_token.go B5：
# token 本体不进日志，写 <数据根>/bootstrap-token 0600 文件）。
BOOTSTRAP_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
[ -n "$BOOTSTRAP_TOKEN" ] || fatal 'empty bootstrap token'
al 'fleetlyd live (plaintext faces), bootstrap token read'

# curl helper（同 bridge——REST 面直达 dind 的 8420；jar 文件常驻容器内）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" \
    sleep 100000 >/dev/null || fatal "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://$DIND_IP:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'

# ───────── AUTH-1: fresh 注册窗口（无用户窗口恒开）
# gateway JSONPb EmitUnpopulated=false：零值字段不输出——false 断言一律用
# 「true 字样缺席」承载（open/has_users/is_platform_admin 同款纪律）。
req anon GET /v1/auth/registration ''
assert "AUTH-1a FRESH_REGISTRATION_STATE_OPEN" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"open": ?true' && echo 0 || echo 1) \
    "GET /v1/auth/registration = $RC body: $BODY"
assert "AUTH-1b FRESH_REGISTRATION_HAS_USERS_FALSE" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -qE '"has_users": ?true' && echo 0 || echo 1) \
    "GET /v1/auth/registration = $RC body: $BODY (has_users omitted = false)"

# ───────── AUTH-3a/3b: bootstrap token 注册前可用（REST Bearer + CLI gRPC）
req anon GET /v1/apps '' -H "Authorization: Bearer $BOOTSTRAP_TOKEN"
assert "AUTH-3a BOOTSTRAP_REST_APPS_LIST_200_PRE_REGISTRATION" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "GET /v1/apps with bootstrap bearer = $RC body: $BODY"
if bcli apps list >/dev/null 2>&1; then
    assert "AUTH-3b BOOTSTRAP_CLI_APPS_LIST_OK_PRE_REGISTRATION" 0
else
    assert "AUTH-3b BOOTSTRAP_CLI_APPS_LIST_OK_PRE_REGISTRATION" 1 "bootstrap CLI apps list failed pre-registration"
fi

# ───────── AUTH-2: 首用户注册（REST）→ 平台管理员 + 个人队 owner + 默认项目
req founder POST /v1/auth/register "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"$FOUNDER_PASS\",\"display_name\":\"Founder\"}"
assert "AUTH-2a FIRST_USER_REGISTER_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "POST /v1/auth/register = $RC body: $BODY"
assert "AUTH-2b FIRST_USER_IS_PLATFORM_ADMIN" $(printf '%s' "$BODY" | grep -qE '"is_platform_admin": ?true' && echo 0 || echo 1) \
    "register body: $BODY"
assert "AUTH-2c FIRST_USER_SESSION_COOKIE_SET" $([ -n "$(sess_of founder)" ] && echo 0 || echo 1) \
    "no fleetly_session captured from the register Set-Cookie"
req founder GET /v1/auth/me ''
assert "AUTH-2d ME_PERSONAL_TEAM_OWNER" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"team_slug": ?"founder"' &&
    printf '%s' "$BODY" | grep -qE '"role": ?"owner"' && echo 0 || echo 1) \
    "me = $RC body: $BODY"

# 注册后的 admin 机具凭据：平台管理员的会话 cookie 建（TokensService W1
# 仍是 admin 全局面 → user NULL 机具令牌），供 CLI/events 面（CLI 不消费
# 会话 cookie——auth.go 头注）。
req founder POST /v1/tokens '{"note":"e2e-auth-machine","scopes":["admin"]}'
MACHINE_TOKEN=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$MACHINE_TOKEN" ] || fatal 'machine token creation failed'
al 'admin machine token minted via founder session (REST /v1/tokens)'

# 注册同事务副作用事件（W1 无 projects 读面——事件流即披露面；默认项目
# 的 slug `default` 在 project.created 事件 payload 里，需 --json 帧）。
if poll_until 30 events_grep user.registered; then
    assert "AUTH-2e USER_REGISTERED_EVENT" 0
else
    assert "AUTH-2e USER_REGISTERED_EVENT" 1 "user.registered missing from the event stream"
fi
events_grep_json team.created || true
PROJ_LINE=$(m grep 'project.created' /tmp/auth-events.jsonl 2>/dev/null | grep 'default' | head -1)
if m grep -q 'team.created' /tmp/auth-events.jsonl 2>/dev/null && [ -n "$PROJ_LINE" ]; then
    assert "AUTH-2f TEAM_AND_DEFAULT_PROJECT_EVENTS" 0
else
    assert "AUTH-2f TEAM_AND_DEFAULT_PROJECT_EVENTS" 1 \
        "team.created missing, or project.created payload without default slug (line=$PROJ_LINE)"
fi

# ───────── AUTH-3c/3d: bootstrap token 注册即吊销（零残留后门）
req anon GET /v1/apps '' -H "Authorization: Bearer $BOOTSTRAP_TOKEN"
assert "AUTH-3c BOOTSTRAP_REST_401_POST_REGISTRATION" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "GET /v1/apps with bootstrap bearer after registration = $RC body: $BODY"
if bcli apps list >/dev/null 2>&1; then
    assert "AUTH-3d BOOTSTRAP_CLI_REJECTED_POST_REGISTRATION" 1 "bootstrap token still accepted after first registration"
else
    assert "AUTH-3d BOOTSTRAP_CLI_REJECTED_POST_REGISTRATION" 0
fi

# ───────── AUTH-4: 注册窗口开关（缺省 closed → open → closed）
req anon POST /v1/auth/register "{\"email\":\"$MATE_EMAIL\",\"password\":\"$MATE_PASS\"}"
assert "AUTH-4a SECOND_USER_REGISTER_403_CLOSED" $([ "$RC" = 403 ] &&
    printf '%s' "$BODY" | grep -qE '"code": ?"E_REGISTRATION_CLOSED"' && echo 0 || echo 1) \
    "second register = $RC body: $BODY"
req anon GET /v1/auth/registration ''
assert "AUTH-4b STATE_CLOSED_WITH_USERS" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -qE '"open": ?true' &&
    printf '%s' "$BODY" | grep -qE '"has_users": ?true' && echo 0 || echo 1) \
    "registration state = $RC body: $BODY (open omitted = false)"
req founder PUT /v1/auth/registration '{"open":true}'
assert "AUTH-4c ADMIN_SET_REGISTRATION_OPEN" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"open": ?true' && echo 0 || echo 1) \
    "PUT /v1/auth/registration = $RC body: $BODY"
req mate POST /v1/auth/register "{\"email\":\"$MATE_EMAIL\",\"password\":\"$MATE_PASS\"}"
assert "AUTH-4d OPEN_WINDOW_SECOND_USER_200_NON_ADMIN" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -qE '"is_platform_admin": ?true' && echo 0 || echo 1) \
    "open-window register = $RC body: $BODY (is_platform_admin omitted = false)"
req founder PUT /v1/auth/registration '{"open":false}'
assert "AUTH-4e ADMIN_SET_REGISTRATION_CLOSED" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -qE '"open": ?true' && echo 0 || echo 1) \
    "PUT /v1/auth/registration = $RC body: $BODY (open omitted = false)"
req anon POST /v1/auth/register '{"email":"late@auth-e2e.test","password":"late-pass-1"}'
assert "AUTH-4f RECLOSED_WINDOW_REJECTED" $([ "$RC" = 403 ] &&
    printf '%s' "$BODY" | grep -qE '"code": ?"E_REGISTRATION_CLOSED"' && echo 0 || echo 1) \
    "post-close register = $RC body: $BODY"

# ───────── AUTH-5: 登录 / 错口令 / 会话生命周期（两只 jar = 两设备）
req devA POST /v1/auth/login "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"$FOUNDER_PASS\"}"
assert "AUTH-5a LOGIN_DEVICE_A_200" $([ "$RC" = 200 ] && [ -n "$(sess_of devA)" ] && echo 0 || echo 1) \
    "device A login = $RC body: $BODY"
req devB POST /v1/auth/login "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"$FOUNDER_PASS\"}"
assert "AUTH-5b LOGIN_DEVICE_B_200" $([ "$RC" = 200 ] && [ -n "$(sess_of devB)" ] && echo 0 || echo 1) \
    "device B login = $RC body: $BODY"
req devA POST /v1/auth/login "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"wrong-pass-9\"}"
assert "AUTH-5c WRONG_PASSWORD_401" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "wrong-password login = $RC body: $BODY"
req devA POST /v1/auth/logout ''
assert "AUTH-5d LOGOUT_200" $([ "$RC" = 200 ] && echo 0 || echo 1) "device A logout = $RC body: $BODY"
req devA GET /v1/auth/me ''
assert "AUTH-5e LOGGED_OUT_SESSION_DEAD" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "device A me after logout = $RC"
req devB GET /v1/auth/me ''
assert "AUTH-5f OTHER_DEVICE_SESSION_ALIVE" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "device B me after A logout = $RC"
req devB POST /v1/auth/logout-all ''
assert "AUTH-5g LOGOUT_ALL_OK" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"sessions_revoked": ?"?[1-9]' && echo 0 || echo 1) \
    "logout-all = $RC body: $BODY"
req devB GET /v1/auth/me ''
assert "AUTH-5h LOGOUT_ALL_REVOKES_ALL_DEVICES" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "device B me after logout-all = $RC"

# ───────── AUTH-6: 登录限流（同 email 连发 → 第 11 次起 429）
# 分层口径（考古 internal/runtime/gateway.go A3 + internal/api/authservice.go）：
# REST 面有两层 401 防护——gateway 的 per-IP 固定窗失败桶（1 分钟窗内第 10
# 次 401 后该 IP 后续请求直接 429 短路，不再触达后端）与 authservice 的
# email+IP 双键令牌桶（10/min）。探针从 REST 打入，两层叠加的可观测截面即
# 「前 10 次 401、第 11 次起 429」——本断言钉的是这个黑盒行为（第 11 次
# 起的 429 来自 gateway 层短路；双键桶的独立截面由 api 单测承载）。
# settle 65s：双键桶全满需 60s，gateway 固定窗按窗起点 1 分钟过期。
settle_buckets 65
RL_CODES=''
n=1
while [ "$n" -le 12 ]; do
    req anon POST /v1/auth/login "{\"email\":\"$RL_EMAIL\",\"password\":\"wrong-pass-$n\"}"
    RL_CODES="$RL_CODES $RC"
    n=$((n + 1))
done
al "rate-limit probe codes:$RL_CODES"
# 尾行补换行：wc -l 只数完整行，漏尾行会把 12 误判成 11。
N_CODES=$(printf '%s\n' "$RL_CODES" | tr ' ' '\n' | sed '/^$/d' | wc -l | tr -d ' ')
NON401=$(printf '%s\n' "$RL_CODES" | tr ' ' '\n' | sed '/^$/d' | head -10 | grep -vc '^401$')
assert "AUTH-6a FIRST_TEN_WITHIN_BURST_401" $([ "$N_CODES" = "12" ] && [ "$NON401" = "0" ] && echo 0 || echo 1) \
    "codes:$RL_CODES (want 12 probes, first 10 all 401)"
NON429=$(printf '%s\n' "$RL_CODES" | tr ' ' '\n' | sed '/^$/d' | tail -2 | grep -vc '^429$')
assert "AUTH-6b ELEVENTH_ONWARDS_429" $([ "$N_CODES" = "12" ] && [ "$NON429" = "0" ] && echo 0 || echo 1) \
    "codes:$RL_CODES (want 11th+12th = 429)"

# ───────── AUTH-7: CLI auth 链（W1 退化形态——机具令牌拒收 + 机具凭据
#           CLI 正面 + 无凭据/幂等面；用户 PAT 全链随 W2 TokensService
#           用户化补全，见头注「诚实范围」）
AUTH7A_OUT=$(printf '%s' "$MACHINE_TOKEN" | acli auth login 2>&1)
assert "AUTH-7a MACHINE_TOKEN_LOGIN_REJECTED_WITH_MESSAGE" $(
    printf '%s' "$AUTH7A_OUT" | grep -qi 'platform machine token' && echo 0 || echo 1) \
    "auth login output: $(printf '%s' "$AUTH7A_OUT" | tail -1)"
AUTH7B_RC=0
printf '%s' "$MACHINE_TOKEN" | acli auth login --json >/dev/null 2>&1 || AUTH7B_RC=$?
assert "AUTH-7b MACHINE_TOKEN_LOGIN_NONZERO_EXIT" $([ "$AUTH7B_RC" -ne 0 ] && echo 0 || echo 1) \
    "auth login with machine token exited $AUTH7B_RC"
if mcli apps list >/dev/null 2>&1; then
    assert "AUTH-7c MACHINE_TOKEN_CLI_APPS_LIST_OK" 0
else
    assert "AUTH-7c MACHINE_TOKEN_CLI_APPS_LIST_OK" 1 "machine token via FLEETLY_TOKEN failed apps list"
fi
AUTH7D_OUT=$(acli_env auth status 2>&1)
assert "AUTH-7d STATUS_MACHINE_TOKEN_HONEST_PROJECTION" $(
    printf '%s' "$AUTH7D_OUT" | grep -qi 'platform machine token' && echo 0 || echo 1) \
    "auth status output: $(printf '%s' "$AUTH7D_OUT" | tail -1)"
AUTH7E_RC=0
AUTH7E_OUT=$(acli auth status 2>&1) || AUTH7E_RC=$?
assert "AUTH-7e STATUS_NO_CREDENTIAL_EXIT1" $([ "$AUTH7E_RC" = 1 ] &&
    printf '%s' "$AUTH7E_OUT" | grep -qi 'not logged in' && echo 0 || echo 1) \
    "no-credential status rc=$AUTH7E_RC output: $(printf '%s' "$AUTH7E_OUT" | tail -1)"
AUTH7F_RC=0
AUTH7F_OUT=$(acli auth logout 2>&1) || AUTH7F_RC=$?
assert "AUTH-7f LOGOUT_IDEMPOTENT_OK" $([ "$AUTH7F_RC" = 0 ] &&
    printf '%s' "$AUTH7F_OUT" | grep -qi 'already logged out' && echo 0 || echo 1) \
    "logout rc=$AUTH7F_RC output: $(printf '%s' "$AUTH7F_OUT" | tail -1)"

# ───────── AUTH-8: 平台管理员用户管理（CreateUser/Disable/Enable/Reset）
# settle 70s：AUTH-6 的 12 连发触发了 gateway per-IP 固定窗失败桶（窗起点
# = 首个 401，1 分钟内该 IP 一律 429 短路）——沉降必须盖过整窗，管理员
# 重登录才不会被 429。
settle_buckets 70
# AUTH-5g 的 LogoutAll 吊销了 founder 全部会话（含注册 jar——全端下线的
# 真实语义）——管理员重新登录恢复平台管理员凭据（fixture 前置，非断言）。
req founder POST /v1/auth/login "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"$FOUNDER_PASS\"}"
[ "$RC" = 200 ] || fatal "founder re-login failed (AUTH-8 pre-condition): $RC $BODY"
req founder POST /v1/users "{\"email\":\"$WORKER_EMAIL\",\"display_name\":\"Worker\"}"
WORKER_PASS=$(printf '%s' "$BODY" | grep -oE '"temporary_password": ?"[^"]*"' | head -1 | cut -d'"' -f4)
WORKER_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
assert "AUTH-8a CREATE_USER_ONE_TIME_PASSWORD" $([ "$RC" = 200 ] &&
    [ -n "$WORKER_PASS" ] && [ "${#WORKER_PASS}" -ge 16 ] && [ -n "$WORKER_ID" ] && echo 0 || echo 1) \
    "create user = $RC body: $BODY"
req workerW POST /v1/auth/login "{\"email\":\"$WORKER_EMAIL\",\"password\":\"$WORKER_PASS\"}"
assert "AUTH-8b NEW_USER_LOGIN_OK" $([ "$RC" = 200 ] && [ -n "$(sess_of workerW)" ] && echo 0 || echo 1) \
    "worker login = $RC body: $BODY"
req founder POST "/v1/users/$WORKER_ID/disable" ''
assert "AUTH-8c DISABLE_USER_OK" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"disabled_at": ?"' && echo 0 || echo 1) \
    "disable = $RC body: $BODY"
req workerW GET /v1/auth/me ''
assert "AUTH-8d DISABLED_USER_SESSION_REVOKED" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "worker me after disable = $RC"
req anon POST /v1/auth/login "{\"email\":\"$WORKER_EMAIL\",\"password\":\"$WORKER_PASS\"}"
assert "AUTH-8e DISABLED_USER_LOGIN_REJECTED" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "worker login after disable = $RC body: $BODY"
req founder POST "/v1/users/$WORKER_ID/enable" ''
assert "AUTH-8f ENABLE_USER_OK" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -qE '"disabled_at": ?"[^"]+"' && echo 0 || echo 1) \
    "enable = $RC body: $BODY"
req workerW POST /v1/auth/login "{\"email\":\"$WORKER_EMAIL\",\"password\":\"$WORKER_PASS\"}"
assert "AUTH-8g REENABLED_USER_LOGIN_OK" $([ "$RC" = 200 ] && [ -n "$(sess_of workerW)" ] && echo 0 || echo 1) \
    "worker login after enable = $RC body: $BODY"
req founder POST "/v1/users/$WORKER_ID/password:reset" ''
NEW_PASS=$(printf '%s' "$BODY" | grep -oE '"temporary_password": ?"[^"]*"' | head -1 | cut -d'"' -f4)
assert "AUTH-8h RESET_PASSWORD_ONE_TIME" $([ "$RC" = 200 ] &&
    [ -n "$NEW_PASS" ] && [ "$NEW_PASS" != "$WORKER_PASS" ] && echo 0 || echo 1) \
    "password reset = $RC body: $BODY"
req workerW GET /v1/auth/me ''
assert "AUTH-8i RESET_REVOKES_ALL_SESSIONS" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "worker me after reset = $RC"
req anon POST /v1/auth/login "{\"email\":\"$WORKER_EMAIL\",\"password\":\"$WORKER_PASS\"}"
assert "AUTH-8j OLD_PASSWORD_DEAD_AFTER_RESET" $([ "$RC" = 401 ] && echo 0 || echo 1) \
    "old-password login after reset = $RC body: $BODY"
req anon POST /v1/auth/login "{\"email\":\"$WORKER_EMAIL\",\"password\":\"$NEW_PASS\"}"
assert "AUTH-8k NEW_PASSWORD_LOGIN_OK" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "new-password login after reset = $RC body: $BODY"

# AUTH-9：审计锚点——跳过（头注「诚实范围」；auth.* 审计行由 hermetic
# 单测直读断言，W3 ListAudit 落地后本套件补 API 面断言）。
al 'AUTH-9 SKIPPED — audit rows have no read face in W1 and no sqlite precedent in e2e; covered by hermetic unit tests (see header)'

finish
