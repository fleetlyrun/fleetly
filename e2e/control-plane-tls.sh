#!/bin/sh
# e2e/control-plane-tls.sh — V2-8 控制面 TLS 端到端（E7 同批，W5-S5；设计
# docs/design/2026-09-22-web-terminal.md §3.4 + §3.1/§3.2；notifications.sh
# 同骨架——单 dind、编排自足、镜像钉 digest）：
#
#   证书形态：manual 模式自签证书（宿主 openssl 生成，SAN = DNS:ctrl.test.
#   local + IP:127.0.0.1），staging 进 dind。校验信任面用 Go 的 SSL_CERT_FILE
#   （crypto/x509 标准行为——把自签证书指为信任根，dind 内无需 ca-certificates
#   改造；CLI --tls 拨 ctrl.test.local 需 /etc/hosts 注入解析）。
#
#   TLS-1  HTTP 面 TLS 生效：wget https://127.0.0.1:8420/healthz/liveness
#          （--no-check-certificate）200 + GET / 引导页（native 端点）200。
#   TLS-2  gRPC 面 TLS 生效：CLI --tls-insecure 读（apps list）+ 写（logs
#          backend set jsonl）全通。
#   TLS-3  明文被双面拒绝：wget http://…:8420 失败；CLI 无旗标拨 8421 失败。
#   TLS-4  CLI --tls 校验路径：--addr ctrl.test.local:8421（/etc/hosts 解析）
#          + SSL_CERT_FILE 信任根 → apps list 成功（服务器证书校验通过）。
#   TLS-5  CLI --tls 证书名不匹配拒：--addr wrong.test.local:8421（SAN 外
#          主机名）→ 失败（校验不是摆设）。
#   TLS-6  --tls 与 --tls-insecure 互斥：同传 → 非零退出 + 可行动文案。
#   TLS-7  platform 模式 loud-fail：base_domain 为空启动即报错退出（错误含
#          base_domain 指引）。
#   TLS-8  off 模式回归（今日行为）：明文 liveness 200 + 明文 CLI ping 通。
#
#   诚实范围：Console 静态（/ui/）不在本套件 staging（无 dist 构建），native
#   端点以 GET / 引导页承载 TLS 面验证；exec relay（S6）不在本阶段。
#
# 断言风格与 e2e/notifications.sh 一致（TLS-x: PASS/FAIL 行 + TLS_FAIL 计数
# + finish）。
# usage: e2e/control-plane-tls.sh
# env:
#   CTL_DIND_IMAGE   dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   CTL_SKIP_BUILD   1 = 跳过交叉编译，改用 CTL_BIN_DIR 下的现成二进制
#   CTL_BIN_DIR      CTL_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   CTL_VERSION      注入的版本串（默认 v0.2.0-control-plane-tls-e2e）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；与 e2e/notifications.sh 同源）。
DIND_IMAGE="${CTL_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
CTL_SKIP_BUILD="${CTL_SKIP_BUILD:-0}"
CTL_BIN_DIR="${CTL_BIN_DIR:-}"
CTL_VERSION="${CTL_VERSION:-v0.2.0-control-plane-tls-e2e}"

BR_NET=fleetly-t-br
BR_SUBNET=10.219.0.0/24
DIND=fleetly-t-e2e-dind
TLS_NAME=ctrl.test.local
TLS_NAME_WRONG=wrong.test.local
CERT_FILE=/opt/fleetly/etc/tls-cert.pem
KEY_FILE=/opt/fleetly/etc/tls-key.pem

TLS_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

tl() { printf '[tls-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { tl "$1: PASS"; }
fail() {
    tl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    TLS_FAIL=$((TLS_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
fatal() { tl "FATAL $*"; exit 1; }
finish() {
    if [ "$TLS_FAIL" -eq 0 ]; then
        tl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    tl "SUITE-DONE failed-asserts=$TLS_FAIL"
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
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_PROJECT="$FOUNDER_PROJECT" -e FLEETLY_TOKEN="$CTL_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
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
        tl "suite RED (rc=$rc) — dumping dind log tail"
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
command -v go >/dev/null 2>&1 || CTL_SKIP_BUILD=1
command -v openssl >/dev/null 2>&1 || fatal 'openssl not on PATH (self-signed fixture cert is generated host-side)'

# ─────────────────────────────────────────────────────────────── 构建
if [ "$CTL_SKIP_BUILD" != '1' ]; then
    tl "cross-compiling linux/amd64 fleetlyd+fleetly ($CTL_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CTL_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CTL_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    CTL_BIN_DIR="$TMP"
else
    CTL_BIN_DIR="${CTL_BIN_DIR:?CTL_SKIP_BUILD=1 requires CTL_BIN_DIR}"
    tl "using prebuilt binaries from $CTL_BIN_DIR"
fi
[ -f "$CTL_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $CTL_BIN_DIR"
[ -f "$CTL_BIN_DIR/fleetly" ] || fatal "fleetly missing in $CTL_BIN_DIR"

# ─────────────────────────────────────── 自签证书（宿主 openssl 生成）
# SAN = DNS:ctrl.test.local（校验正例）+ IP:127.0.0.1（回环直连形态）。
# 显式 -config：不读环境 openssl.cnf（宿主配置各异，Git for Windows 实测
# 有非标扩展段），脚本对环境零假设。
cat >"$TMP/openssl.cnf" <<'EOF'
[req]
distinguished_name = dn
x509_extensions = v3_ext
prompt = no
[dn]
CN = ctrl.test.local
[v3_ext]
subjectAltName = DNS:ctrl.test.local,IP:127.0.0.1
basicConstraints = critical,CA:TRUE
keyUsage = critical,digitalSignature,keyCertSign
EOF
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout "$TMP/tls-key.pem" -out "$TMP/tls-cert.pem" -days 2 \
    -config "$TMP/openssl.cnf" >"$TMP/openssl.log" 2>&1 || {
    cat "$TMP/openssl.log" >&2 || true
    fatal "openssl self-signed cert generation failed (TMP=$TMP)"
}
[ -s "$TMP/tls-cert.pem" ] && [ -s "$TMP/tls-key.pem" ] || fatal 'empty fixture cert/key'

# ─────────────────────────────────────────────────────── dind 编排准备
leftovers=$(docker ps -aq --filter name=fleetly-t-e2e- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    tl "WARN removing leftover tls-e2e containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

docker rm -f "$DIND" >/dev/null 2>&1 || true
docker run -d --name "$DIND" --privileged --hostname mgr \
    --network "$BR_NET" --ip 10.219.0.10 "$DIND_IMAGE" >/dev/null ||
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
tl "dind $DIND ready (engine $(docker exec "$DIND" docker version --format '{{.Server.Version}}' 2>/dev/null))"

tl 'staging binaries + certs + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$CTL_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$CTL_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
docker exec "$DIND" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'
stage "$DIND" "$TMP/tls-cert.pem" "$CERT_FILE"
stage "$DIND" "$TMP/tls-key.pem" "$KEY_FILE"

# /etc/hosts 注入：ctrl.test.local（校验正例）与 wrong.test.local（SAN 外
# 负例）都解析到回环——校验差异只来自证书 SAN，不来自解析。
docker exec "$DIND" sh -c "printf '127.0.0.1 $TLS_NAME\n127.0.0.1 $TLS_NAME_WRONG\n' >> /etc/hosts" || fatal 'hosts injection'

# fleetlyd 配置（manual TLS 模式）：离线 dind 形态——ACME/git 关闭。
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
control_plane:
  tls:
    mode: manual
    cert_file: "/opt/fleetly/etc/tls-cert.pem"
    key_file: "/opt/fleetly/etc/tls-key.pem"
    min_version: tls1.2
logging:
  level: info
EOF
stage "$DIND" "$TMP/config.yaml" /opt/fleetly/etc/config.yaml

docker exec "$DIND" docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/tls-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
CTL_TOKEN=''
CTL_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if msh 'wget -q -T 3 -O /dev/null --no-check-certificate https://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        CTL_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$CTL_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/tls-fleetlyd.log' || true
    fatal 'fleetlyd not live (TLS) within 90s'
}
CTL_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
# ── v0.3 归属管道 fixture（rbac-teams §2.1/§2.3/§3.4）：注册 founder（首
# 用户 = 平台管理员 + 个人队 + 默认项目 default）→ 会话自服务铸用户 PAT
#（admin scope——founder 是平台管理员可达集；CLI 不消费会话 cookie）。
# 首次部署/建库经 FLEETLY_PROJECT=founder/default 显式携带项目归属；fcli
# 统一 env 注入。bootstrap token 已随首用户注册按设计吊销弃用。
CURLER="$DIND-curl"
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" sleep 100000 >/dev/null ||
    fatal "docker run $CURLER"
# 本套件 fleetlyd 以 TLS 模式监听（8420 = HTTPS 面，自签证书 → -k）；
# 明文 http 打 HTTPS 口会拿到 Go 的提示串且 curl 退出码 0——必须 https。
docker exec "$CURLER" curl -s -k -o /dev/null "https://10.219.0.10:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'
docker exec "$CURLER" curl -s -k -c /tmp/jar -X POST "https://10.219.0.10:8420/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"founder@e2e.test","password":"founder-pass-1","display_name":"Founder"}' \
    >/dev/null || fatal 'founder register'
# W2-S5 夹具修正：凭据改铸 **machine 令牌**（平台级凭据 = 资源面 admin
# 等价，rbac-teams §2.3；W2-S4 起平台管理员在资源面被 ResolvePermission
# 短路为只读——founder 的用户 PAT 已不能再承担部署/资源写；机器令牌不绑
# 用户角色，且本套件 FLEETLY_PROJECT 显式携带项目归属）。
CTL_TOKEN=$(docker exec "$CURLER" curl -s -k -b /tmp/jar -X POST "https://10.219.0.10:8420/v1/tokens" \
    -H 'Content-Type: application/json' \
    -d '{"machine":true,"note":"e2e machine token","scopes":["admin"]}' | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$CTL_TOKEN" ] || fatal 'machine token mint failed'
# curl helper 用毕即除（fixture 只承担注册与铸 token；避免钉住 bridge 网络影响后续套件）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
FOUNDER_TEAM=founder
FOUNDER_PRJ=default
FOUNDER_PROJECT="$FOUNDER_TEAM/$FOUNDER_PRJ"
nl 'founder registered (platform admin); machine token minted; project context '"'"'"$FOUNDER_PROJECT"'"'"''

[ -n "$CTL_TOKEN" ] || fatal 'empty bootstrap token'
tl 'fleetlyd live over TLS (liveness 200), bootstrap token read'

# ───────── TLS-1: HTTP 面 TLS 生效（liveness + GET / 引导页 native 端点）
msh 'wget -q -T 3 -O /dev/null --no-check-certificate https://127.0.0.1:8420/healthz/liveness'
assert "TLS-1a HTTPS_LIVENESS_200" $?
msh 'wget -q -T 3 -O /tmp/tls-landing.html --no-check-certificate https://127.0.0.1:8420/ && grep -qi "fleetly" /tmp/tls-landing.html'
assert "TLS-1b HTTPS_LANDING_NATIVE_ENDPOINT" $?

# ───────── TLS-2: gRPC 面 TLS 生效（CLI --tls-insecure 读 + 写）
# 旗标纪律（F5/FZ-11）：conn flags 在动词（子动词）之后、位置参数之前。
tls2_read() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$CTL_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly apps list --tls-insecure >/dev/null 2>&1
}
if poll_until 30 tls2_read; then
    assert "TLS-2a GRPC_INSECURE_TLS_READ_APPS_LIST" 0
else
    assert "TLS-2a GRPC_INSECURE_TLS_READ_APPS_LIST" 1 "apps list over --tls-insecure failed"
fi
tls2_write() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$CTL_TOKEN" \
        "$DIND" /opt/fleetly/bin/fleetly logs backend set --tls-insecure jsonl >/dev/null 2>&1
}
if poll_until 30 tls2_write; then
    assert "TLS-2b GRPC_INSECURE_TLS_WRITE_BACKEND_SET" 0
else
    assert "TLS-2b GRPC_INSECURE_TLS_WRITE_BACKEND_SET" 1 "logs backend set over --tls-insecure failed"
fi

# ───────── TLS-9: REST /v1 回拨路径（W5-S5 缺陷修复回归，2026-09-22
# staging 实证补获：TLS 形态下 gateway 明文回拨使全量 REST /v1 断——CLI
# 直连与 native 端点各自全绿也拦不住这一条，必须显式断言 gateway 往返）。
tls9_rest_ok() {
    msh 'wget -q -T 5 -O /tmp/tls9-body --no-check-certificate https://127.0.0.1:8420/v1/system/ping' 2>/dev/null &&
        m grep -q '"service"' /tmp/tls9-body 2>/dev/null
}
if poll_until 30 tls9_rest_ok; then
    assert "TLS-9 REST_V1_TLS_DIALBACK_OK" 0
else
    m cat /tmp/tls9-body 2>/dev/null || true
    assert "TLS-9 REST_V1_TLS_DIALBACK_OK" 1 "REST /v1 round-trip through the TLS gateway dialback failed"
fi

# ───────── TLS-3: 明文被双面拒绝
msh 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null
[ $? -ne 0 ]
assert "TLS-3a HTTP_PLAINTEXT_REJECTED_ON_8420" $?
tls3_grpc_rejected() {
    if fcli apps list >/dev/null 2>&1; then return 1; fi
    return 0
}
if poll_until 30 tls3_grpc_rejected; then
    assert "TLS-3b GRPC_PLAINTEXT_DIAL_REJECTED_ON_8421" 0
else
    assert "TLS-3b GRPC_PLAINTEXT_DIAL_REJECTED_ON_8421" 1 "plaintext CLI dial unexpectedly succeeded against a TLS port"
fi

# ───────── TLS-4: CLI --tls 校验路径（SAN 主机名 + SSL_CERT_FILE 信任根）
tls4_verified() {
    docker exec -e FLEETLY_TOKEN="$CTL_TOKEN" -e SSL_CERT_FILE="$CERT_FILE" \
        "$DIND" /opt/fleetly/bin/fleetly apps list --addr "$TLS_NAME:8421" --tls >/dev/null 2>&1
}
if poll_until 30 tls4_verified; then
    assert "TLS-4a CLI_TLS_VERIFIED_VIA_SAN_NAME" 0
else
    assert "TLS-4a CLI_TLS_VERIFIED_VIA_SAN_NAME" 1 "--tls verification against ctrl.test.local failed"
fi

# ───────── TLS-5: CLI --tls 证书名不匹配拒（SAN 外主机名）
tls5_mismatch() {
    if docker exec -e FLEETLY_TOKEN="$CTL_TOKEN" -e SSL_CERT_FILE="$CERT_FILE" \
        "$DIND" /opt/fleetly/bin/fleetly apps list --addr "$TLS_NAME_WRONG:8421" --tls >/dev/null 2>&1; then
        return 1
    fi
    return 0
}
if poll_until 30 tls5_mismatch; then
    assert "TLS-5 CLI_TLS_NAME_MISMATCH_REJECTED" 0
else
    assert "TLS-5 CLI_TLS_NAME_MISMATCH_REJECTED" 1 "--tls accepted a hostname outside the certificate SAN"
fi

# ───────── TLS-6: --tls 与 --tls-insecure 互斥（客户端旗标校验）
tls6_out=$(docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$CTL_TOKEN" \
    "$DIND" /opt/fleetly/bin/fleetly apps list --tls --tls-insecure 2>&1)
tls6_rc=$?
[ $tls6_rc -ne 0 ]
assert "TLS-6a TLS_FLAGS_MUTUALLY_EXCLUSIVE_NONZERO" $?
printf '%s' "$tls6_out" | grep -qi "mutually exclusive"
assert "TLS-6b TLS_FLAGS_MUTUAL_EXCLUSION_MESSAGE" $?

# ───────── TLS-7: platform 模式 loud-fail（base_domain 为空启动报错）
cat >"$TMP/config-platform-bad.yaml" <<'EOF'
addr: "127.0.0.1:18420"
grpc:
  addr: "127.0.0.1:18421"
state:
  db_path: "/var/lib/fleetly/fleetly-badmode.db"
secrets:
  key_path: "/var/lib/fleetly/fleetly-badmode.key"
ingress:
  token_file: "/var/lib/fleetly/fleetly-badmode-ingress.token"
  cert_dir: "/var/lib/fleetly/fleetly-badmode-certs"
  acme:
    enabled: false
git:
  enabled: false
control_plane:
  tls:
    mode: platform
logging:
  level: info
EOF
stage "$DIND" "$TMP/config-platform-bad.yaml" /opt/fleetly/etc/config-platform-bad.yaml
msh 'timeout 60 /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config-platform-bad.yaml > /tmp/tls-badmode.log 2>&1; echo $? > /tmp/tls-badmode.rc'
BAD_RC=$(m sh -c 'cat /tmp/tls-badmode.rc')
[ "$BAD_RC" != "0" ]
assert "TLS-7a PLATFORM_MODE_EMPTY_BASE_DOMAIN_LOUDFAIL" $?
m grep -qi "base_domain" /tmp/tls-badmode.log
assert "TLS-7b PLATFORM_LOUDFAIL_NAMES_BASE_DOMAIN" $?

# ───────── TLS-8: off 模式回归（今日明文行为逐字可回）
msh 'kill $(cat /var/run/fleetlyd.pid) 2>/dev/null; true'
# 等 TLS 实例退干净（进程消失后再起 off 形态——同端口复用）。
i=0
while [ "$i" -lt 30 ]; do
    m sh -c 'kill -0 $(cat /var/run/fleetlyd.pid) 2>/dev/null' 2>/dev/null || break
    i=$((i + 2))
    sleep 2
done
cat >"$TMP/config-off.yaml" <<'EOF'
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
git:
  enabled: false
control_plane:
  tls:
    mode: off
logging:
  level: info
EOF
stage "$DIND" "$TMP/config-off.yaml" /opt/fleetly/etc/config-off.yaml
docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config-off.yaml > /tmp/tls-off-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
off_live=0
i=0
while [ "$i" -lt 90 ]; do
    if msh 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        off_live=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$off_live" -eq 1 ]
assert "TLS-8a OFF_MODE_PLAINTEXT_LIVENESS_200" $?
tls8_cli() {
    fcli apps list >/dev/null 2>&1
}
if poll_until 30 tls8_cli; then
    assert "TLS-8b OFF_MODE_PLAINTEXT_CLI_READ" 0
else
    msh 'tail -20 /tmp/tls-off-fleetlyd.log' || true
    assert "TLS-8b OFF_MODE_PLAINTEXT_CLI_READ" 1 "plaintext CLI apps list failed in off mode"
fi

finish
