#!/bin/sh
# e2e/rbac.sh — v0.3 W2-S5 RBAC 收官端到端（rbac-teams 设计 §3.2/§3.3/§4.2/
# §8「e2e 新增 rbac.sh」；auth.sh 同骨架——单 dind、编排自足、镜像钉 digest。
# fixture 模式：注册 founder→PAT→CLI/REST；REST 会话/PAT 断言经 curl helper
# 容器，Bearer 认证不依赖 cookie jar 但沿用同一通道）。
#
#   RB-0  邀请注册通道（v0.3 W3-S4，设计 §3.1「未注册→注册即自动 accept」）：
#           a 关窗状态下无 token 注册 → E_REGISTRATION_CLOSED（窗口确已关闭
#             的承重面）；
#           b founder 邀请未注册邮箱（viewer）→ 注册携带 invite_token → 200
#             （豁免注册窗）+ Me 即见受邀队 viewer 角色；
#           c 无效 token 注册 → E_INVITE_INVALID（不泄漏存在性细节）。
#   RB-1  角色矩阵抽检（§3.2）：
#           a 团队 viewer PAT 部署 → 403；
#           b 团队 developer PAT env 明文读 → 403（owner 正面对照 200）；
#           c developer PAT 可部署（真收敛 succeeded）+ 可终端 ticket（200），
#             viewer ticket → 403（terminal=developer+）；
#           d 机具令牌全库：跨团队 apps 全列（acme + solo 个人队）+ 团队读面
#             403（teams.proto：机具两面恒拒）。
#   RB-2  项目覆写四断言（§3.3 队内覆写形 B）：
#           a 团队 developer 于项目 B 降 viewer → 项目 B 部署 403；
#           b 团队 viewer 于项目 B 升 developer → 项目 B 部署 200、项目 A
#             （default）仍 403（覆写单项目作用域）；
#           c owner 恒不覆写：role=owner → 400（词表拒）、对团队 owner 设
#             覆写 → 409（E_PROJECT_MEMBER_OWNER）；
#           d 移出团队联动清覆写：viewd 移出后项目 B 部署 403 + 列表不可见
#             + 详情 404（不可见不泄漏）。
#   RB-3  跨项目库引用拒绝：项目 B 的 app 引用项目 A 的库实例 → 部署入队
#           后 preparing 期失败，error_code=E_DB_PROJECT_MISMATCH（§4.1 E4）。
#   RB-4  MoveApp 改派随迁（平台管理员）：app 从 default 改派 projb →
#           换名重部署（服务名 fleetly-amber-projb-app-m-web 就位、旧名
#           fleetly-amber-default-app-m-web 摘除）+ 权限随迁（devv 在 default
#           可部署、改派后因 projb 覆写 viewer 变 403）。
#   RB-5  列表过滤（§4.2）：第二团队成员只见自己个人队的 app（跨队详情
#           404 不泄漏）；?project= 收窄双向抽检（限定形 team/prj）。
#   RB-6  SearchLogs 越权选择器 → 400：用户凭据裸名选择器拒（三段限定形
#           强制，选择器校验先于日志后端可达性——离线 dind 可断言）。
#
#   诚实范围：SearchLogs 只断言准入面 400（VL 检索正路径归
#   logs-victorialogs.sh 真机闭环）；覆写行管理权（admin 不能改覆写行——
#   §3.3 权限怪圈防线）由 api 单测钉死，e2e 夹具无项目级 admin 成员可构造。
#
# 断言风格与 e2e/auth.sh 一致（RB-x: PASS/FAIL 行 + RBAC_FAIL 计数 + finish）。
# usage: e2e/rbac.sh
# env:
#   RB_DIND_IMAGE   dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   RB_SKIP_BUILD   1 = 跳过交叉编译，改用 RB_BIN_DIR 下的现成二进制
#   RB_BIN_DIR      RB_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   RB_VERSION      注入的版本串（默认 v0.3.0-rbac-e2e）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；与 e2e/auth.sh 同源。curl helper 提供
#    REST 通道；curl 镜像兼作受管 app 的工作负载镜像——离线可拉、alpine
#    busybox sleep 即长驻，部署真收敛不引入私有 ghcr 依赖）。
DIND_IMAGE="${RB_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
RB_SKIP_BUILD="${RB_SKIP_BUILD:-0}"
RB_BIN_DIR="${RB_BIN_DIR:-}"
RB_VERSION="${RB_VERSION:-v0.3.0-rbac-e2e}"

BR_NET=fleetly-rbac-br
BR_SUBNET=10.223.0.0/24
DIND=fleetly-rbac-e2e-dind
CURLER=fleetly-rbac-e2e-curl
DIND_IP=10.223.0.10

# ── 身份口径：founder = 首用户（平台管理员 + 个人队 founder/default）；
#    acme 团队成员 deva=developer、viewb=viewer、devv=developer、
#    viewd=viewer（覆写断言主角）；solo = 第二团队（个人队）成员。
#    团队 slug 用 amber（acme 是 E_TEAM_SLUG_RESERVED 保留字——撞 ACME
#    挑战路由键族，见 internal/naming reservedTeamSlugReasons）。
FOUNDER_EMAIL=founder@rbac-e2e.test
FOUNDER_PASS=founder-pass-1
DEVA_EMAIL=deva@rbac-e2e.test
DEVA_PASS=deva-pass-11
VIEWB_EMAIL=viewb@rbac-e2e.test
VIEWB_PASS=viewb-pass-1
DEVV_EMAIL=devv@rbac-e2e.test
DEVV_PASS=devv-pass-11
VIEWD_EMAIL=viewd@rbac-e2e.test
VIEWD_PASS=viewd-pass-1
SOLO_EMAIL=solo@rbac-e2e.test
SOLO_PASS=solo-pass-11
ADMINK_EMAIL=admink@rbac-e2e.test
ADMINK_PASS=admink-pass1
INVNEW_EMAIL=invnew@rbac-e2e.test
INVNEW_PASS=invnew-pass-1

TEAM=amber
PROJ_A=proja
PROJ_B=projb
APP_A=app-a
APP_B=app-b
APP_M=app-m
APP_X=app-x
SOLO_APP=solo-app
SVC=web

RBAC_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

rl() { printf '[rbac-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { rl "$1: PASS"; }
fail() {
    rl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    RBAC_FAIL=$((RBAC_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
fatal() { rl "FATAL $*"; exit 1; }
finish() {
    if [ "$RBAC_FAIL" -eq 0 ]; then
        rl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    rl "SUITE-DONE failed-asserts=$RBAC_FAIL"
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

# req <jar-base> <method> <path> <json-body-or-''> [extra-curl-args...] —
# 经 curl helper 容器打 dind REST 面（8420）。jar 恒读写（会话面沿用
# auth.sh 口径；PAT 面走 extra args 的 Bearer 头）。HTTP 状态码落全局 RC、
# 响应体落全局 BODY（直呼不用 $() —— 子_shell 会吞赋值）。
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

# bearer <pat> — 已内联为 req 的 extra args（-H "Authorization: Bearer …"），
# 不再单列 helper。

# deploy_compose <jar-base> <pat> <app> <project-ref> <services-yaml> —
# REST Deploy（compose 字节 base64 上行，proto bytes 契约）。顶层 name:
# 由本 helper 注入（= app 名，compose 应用名与请求 app 一致的 A1 契约）。
deploy_compose() {
    jarb=$1
    pat=$2
    app=$3
    proj=$4
    services=$5
    compose="name: $app
$services"
    b64=$(printf '%s' "$compose" | base64 | tr -d '\n')
    # shellcheck disable=SC2086
    req "$jarb" POST "/v1/apps/$app/deployments" \
        "{\"app\":\"$app\",\"project\":\"$proj\",\"compose\":\"$b64\"}" \
        -H "Authorization: Bearer $pat"
}

# 受管 app 负载镜像：alpine 3.20 钉 digest（公共 docker hub；terminal.sh
# 同款 compose 形态——command sleep 长驻 + healthcheck，受控子集内字段）。
APP_IMAGE='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'

COMPOSE_WEB="services:
  $SVC:
    image: $APP_IMAGE
    command: [\"sh\", \"-c\", \"sleep 100000\"]
    healthcheck:
      test: [\"CMD\", \"true\"]
      interval: 2s
      timeout: 1s
      retries: 100
"

COMPOSE_DBREF="services:
  $SVC:
    image: $APP_IMAGE
    command: [\"sh\", \"-c\", \"sleep 100000\"]
    labels:
      fleetly.databases: \"db-a\"
"

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
        rl "suite RED (rc=$rc) — dumping fleetlyd log tail"
        docker exec "$DIND" sh -c 'tail -60 /tmp/rbac-fleetlyd.log 2>/dev/null' || true
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
command -v go >/dev/null 2>&1 || RB_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$RB_SKIP_BUILD" != '1' ]; then
    rl "cross-compiling linux/amd64 fleetlyd+fleetly ($RB_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$RB_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$RB_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    RB_BIN_DIR="$TMP"
else
    RB_BIN_DIR="${RB_BIN_DIR:?RB_SKIP_BUILD=1 requires RB_BIN_DIR}"
    rl "using prebuilt binaries from $RB_BIN_DIR"
fi
[ -f "$RB_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $RB_BIN_DIR"
[ -f "$RB_BIN_DIR/fleetly" ] || fatal "fleetly missing in $RB_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
leftovers=$(docker ps -aq --filter name=fleetly-rbac-e2e- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    rl "WARN removing leftover rbac-e2e containers: $leftovers"
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
rl "dind $DIND ready (engine $(docker exec "$DIND" docker version --format '{{.Server.Version}}' 2>/dev/null))"

rl 'staging binaries + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$RB_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$RB_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
docker exec "$DIND" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'

# fleetlyd 配置（离线 dind 形态——ACME/git 关闭；terminal 显式启用供 ticket
# 受理面；部署真收敛经 curl 镜像）。
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
terminal:
  enabled: true
logging:
  level: info
EOF
stage "$DIND" "$TMP/config.yaml" /opt/fleetly/etc/config.yaml

docker exec "$DIND" docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'

# 负载镜像预热（terminal.sh/databases.sh 同惯例）：显式拉取并把失败变成
# loud fatal——受管 app 部署的镜像拉取依赖外网，瞬时限流在部署受理路径上
# 表现为 E_IMAGE_PULL_FAILED 终态（引擎不对终态部署重试），预热把这一
# 脆弱点收敛到编排前置。
rl "pre-pulling workload image ($APP_IMAGE)"
docker exec "$DIND" docker pull "$APP_IMAGE" >/dev/null 2>&1 ||
    fatal "pull $APP_IMAGE (inside dind) failed — check outbound registry access"

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/rbac-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
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
    msh 'tail -40 /tmp/rbac-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
rl 'fleetlyd live (plaintext faces)'

# curl helper（同 bridge——REST 面直达 dind 的 8420）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" \
    sleep 100000 >/dev/null || fatal "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://$DIND_IP:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'

# ───────────────────────────────────────────────── fixture：身份与团队
# founder 注册（首用户 = 平台管理员 + 个人队 founder/default）。注意 S4
# 冻结语义：平台管理员在资源面被 ResolvePermission 短路为只读（不代团队
# 写，职责分离）——本套件的部署/建库等资源写全部由团队成员 PAT 承担，
# founder 只承担团队面写（建队/邀请/项目/覆写）与 MoveApp（平台管理员面）。
req founder POST /v1/auth/register "{\"email\":\"$FOUNDER_EMAIL\",\"password\":\"$FOUNDER_PASS\",\"display_name\":\"Founder\"}"
[ "$RC" = 200 ] || fatal "founder register failed: $RC $BODY"
FOUNDER_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$FOUNDER_ID" ] || fatal 'founder id missing'

# founder PAT（owner 全量可达 = admin scope）。
req founder POST /v1/tokens '{"note":"founder pat","scopes":["admin"]}'
FOUNDER_PAT=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$FOUNDER_PAT" ] || fatal 'founder PAT creation failed'

# 机具令牌（平台级凭据 = founder 会话 + machine 旗标显式签发，设计 §2.3）。
req founder POST /v1/tokens '{"machine":true,"note":"rbac machine","scopes":["admin"]}'
MACHINE_TOKEN=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$MACHINE_TOKEN" ] || fatal 'machine token creation failed'

# 注册窗口开放 → 六个用户注册（各自个人队随注册建立）→ 收窗。
req founder PUT /v1/auth/registration '{"open":true}'
[ "$RC" = 200 ] || fatal "open registration failed: $RC $BODY"
for u in "deva:$DEVA_EMAIL:$DEVA_PASS" "viewb:$VIEWB_EMAIL:$VIEWB_PASS" \
    "devv:$DEVV_EMAIL:$DEVV_PASS" "viewd:$VIEWD_EMAIL:$VIEWD_PASS" \
    "admink:$ADMINK_EMAIL:$ADMINK_PASS" \
    "solo:$SOLO_EMAIL:$SOLO_PASS"; do
    name=${u%%:*}
    rest=${u#*:}
    email=${rest%%:*}
    passw=${rest#*:}
    # shellcheck disable=SC2154
    req "$name" POST /v1/auth/register "{\"email\":\"$email\",\"password\":\"$passw\"}"
    [ "$RC" = 200 ] || fatal "register $email failed: $RC $BODY"
done
req founder PUT /v1/auth/registration '{"open":false}'
[ "$RC" = 200 ] || fatal "close registration failed: $RC $BODY"
rl 'fixture users registered (deva/viewb/devv/viewd/admink/solo)'

# 用户 id（Me 投影第一列 id）。
req deva GET /v1/auth/me ''
DEVA_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
req viewb GET /v1/auth/me ''
VIEWB_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
req devv GET /v1/auth/me ''
DEVV_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
req viewd GET /v1/auth/me ''
VIEWD_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$DEVA_ID" ] && [ -n "$VIEWB_ID" ] && [ -n "$DEVV_ID" ] && [ -n "$VIEWD_ID" ] ||
    fatal "member ids missing: deva=$DEVA_ID viewb=$VIEWB_ID devv=$DEVV_ID viewd=$VIEWD_ID"

# acme 团队 + 邀请（角色 ≤ 邀请者 owner）+ 各自 accept。
req founder POST /v1/teams "{\"slug\":\"$TEAM\",\"name\":\"Amber\"}"
[ "$RC" = 200 ] || fatal "create team failed: $RC $BODY"
TEAM_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$TEAM_ID" ] || fatal 'team id missing'

accept_invite() { # <jar-base> <email> <role>
    # 注意：req 内部 jarb 是全局——被邀请人 jar 名存入独立变量，避免被
    # 中间的 req founder 调用覆写（否则 founder 会拿自己的会话 accept——
    # 已是成员路径 200 回读，静默吞掉成员插入）。
    accepter=$1
    email=$2
    role=$3
    req founder POST "/v1/teams/$TEAM_ID/invites" "{\"email\":\"$email\",\"role\":\"$role\"}"
    [ "$RC" = 200 ] || fatal "invite $email failed: $RC $BODY"
    tok=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
    [ -n "$tok" ] || fatal "invite token missing for $email"
    req "$accepter" POST /v1/auth/invite:accept "{\"token\":\"$tok\"}"
    [ "$RC" = 200 ] || fatal "accept invite $email failed: $RC $BODY"
    printf '%s' "$BODY" | grep -qE "\"role\": ?\"$role\"" ||
        fatal "accept invite $email role mismatch: $BODY"
}
accept_invite deva "$DEVA_EMAIL" developer
accept_invite viewb "$VIEWB_EMAIL" viewer
accept_invite devv "$DEVV_EMAIL" developer
accept_invite viewd "$VIEWD_EMAIL" viewer
accept_invite admink "$ADMINK_EMAIL" admin
rl 'amber team members joined via invites (deva=dev viewb=view devv=dev viewd=view admink=admin)'

# ───────── RB-0: 邀请注册通道（v0.3 W3-S4，设计 §3.1）：注册窗在上方已
#            显式收窗（closed），被邀请的未注册用户经 invite_token 注册。
# a 无 token 注册 → E_REGISTRATION_CLOSED（此时窗口确已关闭的承重面）。
req anon POST /v1/auth/register "{\"email\":\"$INVNEW_EMAIL\",\"password\":\"$INVNEW_PASS\"}"
assert "RB-0a WINDOW_CLOSED_NO_TOKEN_403" $([ "$RC" = 403 ] &&
    printf '%s' "$BODY" | grep -qE '"code": ?"E_REGISTRATION_CLOSED"' && echo 0 || echo 1) \
    "closed-window no-token register = $RC body: $BODY"

# b founder 邀请未注册邮箱 → 携带 invite_token 注册 → 200（豁免注册窗），
#   且 Me 即见受邀队与受邀角色（注册即自动 accept，无需二次 accept 调用）。
req founder POST "/v1/teams/$TEAM_ID/invites" "{\"email\":\"$INVNEW_EMAIL\",\"role\":\"viewer\"}"
[ "$RC" = 200 ] || fatal "invite for unregistered user failed: $RC $BODY"
INVNEW_TOKEN=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$INVNEW_TOKEN" ] || fatal 'invite token missing for unregistered user'
req invnew POST /v1/auth/register "{\"email\":\"$INVNEW_EMAIL\",\"password\":\"$INVNEW_PASS\",\"invite_token\":\"$INVNEW_TOKEN\"}"
assert "RB-0b-1 INVITE_REGISTER_WINDOW_EXEMPT_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "invite registration with closed window = $RC body: $BODY"
req invnew GET /v1/auth/me ''
assert "RB-0b-2 INVITE_REGISTER_AUTO_ACCEPT_ROLE" $(printf '%s' "$BODY" | grep -q "\"$TEAM_ID\"" &&
    printf '%s' "$BODY" | grep -qE '"role": ?"viewer"' && echo 0 || echo 1) \
    "Me after invite registration = $BODY (want membership in $TEAM_ID with viewer role)"

# c 无效 token 注册 → E_INVITE_INVALID（一次性凭据不泄漏存在性细节；
#   注册表 HTTP 状态随信封承载）。
req anon POST /v1/auth/register "{\"email\":\"invbogus@rbac-e2e.test\",\"password\":\"bogus-pass-1\",\"invite_token\":\"bogus-token-value\"}"
assert "RB-0c INVALID_INVITE_TOKEN_REJECTED" $([ "$RC" = 409 ] &&
    printf '%s' "$BODY" | grep -qE '"code": ?"E_INVITE_INVALID"' && echo 0 || echo 1) \
    "invalid invite token register = $RC body: $BODY"

# 项目 proja / projb（owner 专属写面——fixture 正面；default 项目只随注册
# 建在个人队，团队项目一律显式创建）。
req founder POST /v1/projects "{\"team_id\":\"$TEAM_ID\",\"slug\":\"$PROJ_A\",\"name\":\"Project A\"}"
[ "$RC" = 200 ] || fatal "create project A failed: $RC $BODY"
req founder POST /v1/projects "{\"team_id\":\"$TEAM_ID\",\"slug\":\"$PROJ_B\",\"name\":\"Project B\"}"
[ "$RC" = 200 ] || fatal "create project failed: $RC $BODY"
PROJ_B_ID=$(printf '%s' "$BODY" | grep -oE '"id": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$PROJ_B_ID" ] || fatal 'project id missing'

# 用户 PAT（自服务；scopes ⊆ 角色可达集）。PAT_VALUE 由 mint_pat 落全局。
PAT_VALUE=''
mint_pat() { # <jar-base> <note> <scopes-json>
    req "$1" POST /v1/tokens "{\"note\":\"$2\",\"scopes\":$3}"
    [ "$RC" = 200 ] || fatal "PAT for $1 failed: $RC $BODY"
    PAT_VALUE=$(printf '%s' "$BODY" | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
    [ -n "$PAT_VALUE" ] || fatal "PAT token missing for $1"
}
mint_pat deva "deva pat" '["read","deploy","terminal"]'
DEVA_PAT=$PAT_VALUE
mint_pat viewb "viewb pat" '["read"]'
VIEWB_PAT=$PAT_VALUE
mint_pat devv "devv pat" '["read","deploy","terminal"]'
DEVV_PAT=$PAT_VALUE
# viewd 的 PAT 在 RB-2b 覆写升级之后铸造（PAT 声明 ⊆ 铸造时可达集——
# viewer 期声明 deploy 会被 400 守卫拒；覆写 developer 生效后可达集才含
# deploy，且 min(声明, 可达) 语义下声明面无法事后扩权）。
mint_pat admink "admink pat" '["admin"]'
ADMINK_PAT=$PAT_VALUE
mint_pat solo "solo pat" '["admin"]'
SOLO_PAT=$PAT_VALUE
rl 'user PATs minted (self-service face)'

# ───────── RB-1a: 团队 viewer PAT 部署 → 403（角色门——请求 project 即
#            准入解析面，app 行尚不存在同样拒）
deploy_compose viewb "$VIEWB_PAT" "$APP_A" "$TEAM/$PROJ_A" "$COMPOSE_WEB"
assert "RB-1a VIEWER_PAT_DEPLOY_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "viewer deploy = $RC body: $BODY"

# ───────── fixture+RB-1c：developer 可部署（真收敛）——app-a 由 deva 首发且
#            收敛 succeeded（同时是 RB-1c-1/2 的正面）。
deploy_compose deva "$DEVA_PAT" "$APP_A" "$TEAM/$PROJ_A" "$COMPOSE_WEB"
assert "RB-1c-1 DEVELOPER_DEPLOY_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q 'deployment_id' && echo 0 || echo 1) \
    "developer deploy = $RC body: $BODY"
deva_deploy_ok() {
    req deva GET "/v1/apps/$APP_A/deployments?limit=5" '' -H "Authorization: Bearer $DEVA_PAT"
    printf '%s' "$BODY" | grep -qE '"status": ?"succeeded"'
}
if poll_until 300 deva_deploy_ok; then
    assert "RB-1c-2 DEVELOPER_DEPLOY_SUCCEEDED" 0
    rl "app-a deployed and succeeded in $TEAM/$PROJ_A"
else
    deva_deploy_ok || true
    rl "deployments snapshot: $BODY"
    assert "RB-1c-2 DEVELOPER_DEPLOY_SUCCEEDED" 1 "developer first deployment never succeeded"
fi

# ───────── RB-1b: developer env 写可、env 明文读 403；admin 明文读 200；
#            平台管理员资源写 403（S4 只读短路，职责分离）
req deva PUT "/v1/apps/$APP_A/env/FOO" '{"value":"admin-only-value"}' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-1b-0 DEVELOPER_ENV_WRITE_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "developer env write = $RC body: $BODY"
req deva GET "/v1/apps/$APP_A/env/FOO" '' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-1b-1 DEVELOPER_ENV_PLAINTEXT_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "developer env plaintext = $RC body: $BODY"
req admink GET "/v1/apps/$APP_A/env/FOO" '' -H "Authorization: Bearer $ADMINK_PAT"
assert "RB-1b-2 ADMIN_ENV_PLAINTEXT_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q 'admin-only-value' && echo 0 || echo 1) \
    "admin env plaintext = $RC body: $BODY"
req founder PUT "/v1/apps/$APP_A/env/FOO" '{"value":"nope"}' -H "Authorization: Bearer $FOUNDER_PAT"
assert "RB-1b-3 PLATFORM_ADMIN_ENV_WRITE_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "platform admin env write = $RC body: $BODY"

# ───────── RB-1c-3/4: developer 可终端 ticket；viewer 403（terminal=developer+）
req deva POST /v1/terminal/tickets "{\"app\":\"$APP_A\",\"service\":\"$SVC\"}" -H "Authorization: Bearer $DEVA_PAT"
assert "RB-1c-3 DEVELOPER_TERMINAL_TICKET_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q 'ticket' && echo 0 || echo 1) \
    "developer ticket = $RC body: $BODY"
req viewb POST /v1/terminal/tickets "{\"app\":\"$APP_A\",\"service\":\"$SVC\"}" -H "Authorization: Bearer $VIEWB_PAT"
assert "RB-1c-4 VIEWER_TERMINAL_TICKET_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "viewer ticket = $RC body: $BODY"

# ───────── fixture：solo 个人队 app（跨团队对比面）+ app-m（RB-4 主角）
deploy_compose solo "$SOLO_PAT" "$SOLO_APP" "solo/default" "$COMPOSE_WEB"
[ "$RC" = 200 ] || fatal "solo-app deploy failed: $RC $BODY"
solo_deploy_ok() {
    req solo GET "/v1/apps/$SOLO_APP/deployments?limit=5" '' -H "Authorization: Bearer $SOLO_PAT"
    printf '%s' "$BODY" | grep -qE '"status": ?"succeeded"'
}
poll_until 300 solo_deploy_ok || fatal 'solo-app deployment never succeeded'

deploy_compose devv "$DEVV_PAT" "$APP_M" "$TEAM/$PROJ_A" "$COMPOSE_WEB"
assert "RB-4-0 DEVV_PROJA_DEPLOY_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "devv deploy app-m in $PROJ_A = $RC body: $BODY"
appm_deploy_ok() {
    req devv GET "/v1/apps/$APP_M/deployments?limit=5" '' -H "Authorization: Bearer $DEVV_PAT"
    printf '%s' "$BODY" | grep -qE '"status": ?"succeeded"'
}
poll_until 300 appm_deploy_ok || fatal 'app-m deployment never succeeded (move prerequisite)'

# ───────── RB-1d: 机具令牌全库（跨团队 apps 全列）+ 团队读面恒 403
req anon GET /v1/apps '' -H "Authorization: Bearer $MACHINE_TOKEN"
assert "RB-1d-1 MACHINE_APPS_LIST_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q "\"$APP_A\"" &&
    printf '%s' "$BODY" | grep -q "\"$SOLO_APP\"" && echo 0 || echo 1) \
    "machine apps list = $RC body: $BODY"
req anon GET /v1/teams '' -H "Authorization: Bearer $MACHINE_TOKEN"
assert "RB-1d-2 MACHINE_TEAMS_READ_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "machine teams list = $RC body: $BODY"

# ───────── fixture：app-b（amber/projb）由 deva 部署并收敛
deploy_compose deva "$DEVA_PAT" "$APP_B" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
[ "$RC" = 200 ] || fatal "app-b deploy failed: $RC $BODY"
appb_deploy_ok() {
    req deva GET "/v1/apps/$APP_B/deployments?limit=5" '' -H "Authorization: Bearer $DEVA_PAT"
    printf '%s' "$BODY" | grep -qE '"status": ?"succeeded"'
}
poll_until 300 appb_deploy_ok || fatal 'app-b deployment never succeeded'
deploy_compose viewb "$VIEWB_PAT" "$APP_B" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
assert "RB-2-0 VIEWER_PROJB_DEPLOY_403_BASELINE" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "viewer projb deploy = $RC body: $BODY"

# ───────── RB-3: 跨项目库引用拒绝（E_DB_PROJECT_MISMATCH）
# db-a 落 amber/proja（admink/admin 建库面）；app-x 落 amber/projb 引用它。
req admink POST /v1/databases \
    "{\"name\":\"db-a\",\"template\":\"postgres-16\",\"project\":\"$TEAM/$PROJ_A\"}"
assert "RB-3-0 DATABASE_CREATE_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "database create = $RC body: $BODY"
deploy_compose deva "$DEVA_PAT" "$APP_X" "$TEAM/$PROJ_B" "$COMPOSE_DBREF"
assert "RB-3-1 DBREF_DEPLOY_ACCEPTED" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "cross-project dbref deploy = $RC body: $BODY"
appx_failed() {
    req deva GET "/v1/apps/$APP_X/deployments?limit=5" '' -H "Authorization: Bearer $DEVA_PAT"
    printf '%s' "$BODY" | grep -qE '"error_code": ?"E_DB_PROJECT_MISMATCH"'
}
if poll_until 120 appx_failed; then
    assert "RB-3-2 DB_PROJECT_MISMATCH_RAISED" 0
else
    appx_failed || true
    assert "RB-3-2 DB_PROJECT_MISMATCH_RAISED" 1 \
        "deployment never failed with E_DB_PROJECT_MISMATCH (snapshot: $BODY)"
fi

# ───────── RB-2a: 团队 developer 于项目 B 降 viewer → 项目 B 部署 403
req founder POST "/v1/projects/$PROJ_B_ID/members:set-role" "{\"user_id\":\"$DEVV_ID\",\"role\":\"viewer\"}"
assert "RB-2a OVERRIDE_SET_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"role": ?"viewer"' && echo 0 || echo 1) \
    "override set = $RC body: $BODY"
deploy_compose devv "$DEVV_PAT" "$APP_B" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
assert "RB-2a-2 DEVV_OVERRIDDEN_VIEWER_PROJB_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "devv proj-b deploy after downgrade = $RC body: $BODY"

# ───────── RB-2b: 团队 viewer 于项目 B 升 developer → 项目 B 200 / proja 403
req founder POST "/v1/projects/$PROJ_B_ID/members:set-role" "{\"user_id\":\"$VIEWD_ID\",\"role\":\"developer\"}"
assert "RB-2b OVERRIDE_UP_200" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -qE '"role": ?"developer"' && echo 0 || echo 1) \
    "override up = $RC body: $BODY"
# viewd 的 PAT 此时才铸（声明 ⊆ 覆写升级后的可达集；见夹具段注释）。
mint_pat viewd "viewd pat" '["read","deploy"]'
VIEWD_PAT=$PAT_VALUE
deploy_compose viewd "$VIEWD_PAT" "$APP_B" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
assert "RB-2b-2 VIEWD_OVERRIDDEN_DEVELOPER_PROJB_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "viewd projb deploy after upgrade = $RC body: $BODY"
deploy_compose viewd "$VIEWD_PAT" "$APP_A" "$TEAM/$PROJ_A" "$COMPOSE_WEB"
assert "RB-2b-3 VIEWD_PROJA_STILL_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "viewd proja deploy (single-project override scope) = $RC body: $BODY"

# ───────── RB-2c: owner 恒不覆写（词表 400 + 对 owner 设行 409）
req founder POST "/v1/projects/$PROJ_B_ID/members:set-role" "{\"user_id\":\"$VIEWD_ID\",\"role\":\"owner\"}"
assert "RB-2c-1 OVERRIDE_ROLE_OWNER_400" $([ "$RC" = 400 ] && echo 0 || echo 1) \
    "override role=owner = $RC body: $BODY"
req founder POST "/v1/projects/$PROJ_B_ID/members:set-role" "{\"user_id\":\"$FOUNDER_ID\",\"role\":\"developer\"}"
assert "RB-2c-2 OVERRIDE_TARGET_OWNER_REJECTED" $([ "$RC" = 409 ] || [ "$RC" = 403 ] && echo 0 || echo 1) \
    "override targeting an owner = $RC body: $BODY"

# ───────── RB-2d: 移出团队联动清覆写 → 部署 403 / 列表不可见 / 详情 404
req founder DELETE "/v1/teams/$TEAM_ID/members/$VIEWD_ID" ''
assert "RB-2d-1 REMOVE_MEMBER_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "remove viewd = $RC body: $BODY"
deploy_compose viewd "$VIEWD_PAT" "$APP_B" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
assert "RB-2d-2 REMOVED_MEMBER_DEPLOY_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "removed member deploy = $RC body: $BODY"
req viewd GET /v1/apps '' -H "Authorization: Bearer $VIEWD_PAT"
assert "RB-2d-3 REMOVED_MEMBER_LIST_NO_TEAM_APPS" $([ "$RC" = 200 ] &&
    ! printf '%s' "$BODY" | grep -q "\"$APP_B\"" && echo 0 || echo 1) \
    "removed member apps list = $RC body: $BODY"
req viewd GET "/v1/apps/$APP_B" '' -H "Authorization: Bearer $VIEWD_PAT"
assert "RB-2d-4 REMOVED_MEMBER_DETAIL_404" $([ "$RC" = 404 ] && echo 0 || echo 1) \
    "removed member app detail = $RC body: $BODY"

# ───────── RB-4: MoveApp 改派（换名重部署 + 权限随迁）
req founder POST "/v1/projects/$PROJ_B_ID:move-app" "{\"app\":\"$APP_M\"}" -H "Authorization: Bearer $FOUNDER_PAT"
assert "RB-4-1 MOVEAPP_200" $([ "$RC" = 200 ] && echo 0 || echo 1) \
    "move app = $RC body: $BODY"
msh "docker service ls --format '{{.Name}}' > /tmp/rbac-services.txt" || true
NEW_SVC="fleetly-$TEAM-$PROJ_B-$APP_M-$SVC"
OLD_SVC="fleetly-$TEAM-$PROJ_A-$APP_M-$SVC"
m grep -qx "$NEW_SVC" /tmp/rbac-services.txt
assert "RB-4-2 NEW_NAMED_SERVICE_PRESENT" $([ $? -eq 0 ] && echo 0 || echo 1) \
    "service list: $(docker exec "$DIND" cat /tmp/rbac-services.txt 2>/dev/null | tr '\n' ' ')"
m grep -qx "$OLD_SVC" /tmp/rbac-services.txt
assert "RB-4-3 OLD_NAMED_SERVICE_SWEPT" $([ $? -ne 0 ] && echo 0 || echo 1) \
    "old service still present: $OLD_SVC"
# 权限随迁：devv（default=developer 可部署 → proj-b 覆写 viewer 后 403）。
deploy_compose devv "$DEVV_PAT" "$APP_M" "$TEAM/$PROJ_B" "$COMPOSE_WEB"
assert "RB-4-4 DEVV_PROJB_AFTER_MOVE_403" $([ "$RC" = 403 ] && echo 0 || echo 1) \
    "devv deploy after move = $RC body: $BODY"

# ───────── RB-5: 列表过滤（?project= 收窄 + 跨队隔离）
req deva GET "/v1/apps?project=$TEAM/$PROJ_B" '' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-5-1 PROJECT_FILTER_PROJB" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q "\"$APP_B\"" &&
    ! printf '%s' "$BODY" | grep -q "\"$APP_A\"" && echo 0 || echo 1) \
    "apps?project=proj-b = $RC body: $BODY"
req deva GET "/v1/apps?project=$TEAM/$PROJ_A" '' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-5-2 PROJECT_FILTER_DEFAULT" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q "\"$APP_A\"" &&
    ! printf '%s' "$BODY" | grep -q "\"$APP_B\"" && echo 0 || echo 1) \
    "apps?project=default = $RC body: $BODY"
req solo GET /v1/apps '' -H "Authorization: Bearer $SOLO_PAT"
assert "RB-5-3 SECOND_TEAM_NO_FOREIGN_APPS" $([ "$RC" = 200 ] &&
    printf '%s' "$BODY" | grep -q "\"$SOLO_APP\"" &&
    ! printf '%s' "$BODY" | grep -q "\"$APP_A\"" &&
    ! printf '%s' "$BODY" | grep -q "\"$APP_B\"" && echo 0 || echo 1) \
    "solo apps list = $RC body: $BODY"
req deva GET "/v1/apps/$SOLO_APP" '' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-5-4 CROSS_TEAM_DETAIL_404" $([ "$RC" = 404 ] && echo 0 || echo 1) \
    "cross-team app detail = $RC body: $BODY"

# ───────── RB-6: SearchLogs 越权选择器 → 400（三段限定形强制）
req deva GET "/v1/apps/$APP_A/logs/search" '' -H "Authorization: Bearer $DEVA_PAT"
assert "RB-6-1 SEARCHLOGS_BARE_SELECTOR_400" $([ "$RC" = 400 ] && echo 0 || echo 1) \
    "search bare selector = $RC body: $BODY"
req viewb GET "/v1/apps/$APP_A/logs/search?keyword=x" '' -H "Authorization: Bearer $VIEWB_PAT"
assert "RB-6-2 SEARCHLOGS_VIEWER_400" $([ "$RC" = 400 ] && echo 0 || echo 1) \
    "viewer search bare selector = $RC body: $BODY"

finish
