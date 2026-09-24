#!/bin/sh
# e2e/nightly/conformance-builder.sh — T2.24 Builder conformance（交付 §2.2
# nightly「conformance 套件对真实组件」的 Builder 部分）宿主编排。
#
# 模式照抄 deploy/run-upgrade-test.sh：交叉编译 fleetlyd+fleetly+探针 → 起
# docker:29.8.1-dind 特权容器 → exec+stdin 注入（禁用 docker cp——Engine
# 29.x 宿主→特权 dind 会 exit 0 但文件不落盘，e2e/README.md 已知问题）→
# 容器内跑生成的 cb-inner.sh → 失败 dump dind 日志 → 清理。可在 GitHub
# Actions（ubuntu-latest）与本地 Git Bash（Windows）/ Linux shell 复跑。
#
# ObjectStore conformance 不在本脚本：ObjectStore 组件 v0.1 未落地
# （minio-go 零使用、无真实消费者），裁决记 N/A，等 v0.2 备份上传统一落地。
#
# 场景（验收标准 5 / 交付 §4 M1）：
#   A  Dockerfile 驱动构建：scratch+静态探针二进制（零 registry 依赖）→
#      构建成功 + 镜像 digest（sha256:）+ 镜像落本机 + 部署 succeeded +
#      derived_state=running + 入口路由 200。
#   B  无 Dockerfile（railpack 检测路径）：go.mod 探针上下文 → 至少断言
#      裁决正确（driver=railpack + 终态证据；构建本身受外网/工具链下载
#      影响，成功或 E_BUILD_FAILED 都是合法终态——结论进 job summary）。
#   C  坏 Dockerfile（COPY 不存在的文件）→ fleetly build rc!=0 + builds 行
#      failed/E_BUILD_FAILED + deploy rc!=0 + 部署行 failed/E_BUILD_FAILED。
#
# v0.3 归属管道 fixture（rbac-teams §2.1/§2.3/§3.4；W2-S3 收尾 2026-09-24
# 迁入，照抄 e2e/cron.sh / e2e/databases.sh / e2e/multinode-rehearsal.sh
# 同名 fixture）：bootstrap token 弃用（v0.3 起 token 落盘
# /var/lib/fleetly/bootstrap-token、日志不再打印，且首用户注册后即按设计
# 吊销）——daemon 起服（cb-boot.sh）后由宿主经 curl helper 容器（dind 内
# 无 curl；CURL_IMAGE 钉 digest 见样板）注册 founder（首用户 = 平台管理员
# + 个人队 + 默认项目 default）并铸用户 PAT（admin scope——founder 是平台
# 管理员可达集），再经 exec+stdin 送回 dind /tmp/cb-token；内层 cb-inner.sh
# 的 cli() 统一携带 FLEETLY_TOKEN=<PAT> FLEETLY_PROJECT=founder/default
#（部署/构建面必须显式项目归属）。为此 dind 迁出默认 bridge，钉在宿主私网
# 10.221.0.0/24（fleetly-cb-br；其余 e2e 套件已占 213-220/222 网段）。
#
# usage: conformance-builder.sh
# env:
#   DIND_IMAGE   dind 镜像（默认钉 digest，台账 #1，与 CI/引擎门禁一致——
#                docs/runbooks/image-prepull.md；tag 保留可读性，digest 为准）
#   DIND_NAME    容器名（默认 fleetly-conformance-builder）
#   CB_SKIP_BUILD 1 = 跳过交叉编译，改用 CB_BIN_DIR 指定的二进制目录
#   CB_BIN_DIR   CB_SKIP_BUILD=1 时的二进制目录（需含 fleetlyd 与 fleetly）
#   CB_VERSION   注入的版本串（默认 v0.1.0-cb）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
DIND_NAME="${DIND_NAME:-fleetly-conformance-builder}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
CB_BR_NET=fleetly-cb-br
CB_BR_SUBNET=10.221.0.0/24
DIND_IP=10.221.0.10
CB_SKIP_BUILD="${CB_SKIP_BUILD:-0}"
CB_VERSION="${CB_VERSION:-v0.1.0-cb}"

log() { printf '[conformance %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}
cleanup() {
    docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
    docker rm -f "$DIND_NAME-curl" >/dev/null 2>&1 || true
    docker network rm "$CB_BR_NET" >/dev/null 2>&1 || true
}
trap 'rm -rf "$TMP"; cleanup' EXIT

# posix_path <p> — 反斜杠路径归一为正斜杠（MSYS/Windows 宿主防御，
# run-upgrade-test.sh 同款）。
posix_path() {
    printf '%s' "$1" | tr '\\' '/'
}

SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/../.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——cygpath -m 归一为 C:/… 混合形态
# （sh/go/docker 三方都认）；非 Cygwin 宿主原样归一。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || die 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || die 'docker not on PATH'
command -v go >/dev/null 2>&1 || CB_SKIP_BUILD=1

# stage <remote-path> <local-file> — exec+stdin 直传 + 两侧 sha256 校验 +
# CR 剥离（文本脚本与源码文件；二进制绝不 sed——ELF 字节对被破坏曾致
# segfault）。
stage() {
    _r=$1
    _f=$2
    docker exec -i "$DIND_NAME" sh -c "cat > '$_r'" <"$_f" ||
        die "staging $_r"
    _h=$(sha256sum <"$_f" 2>/dev/null)
    _h=${_h%% *}
    _g=$(docker exec "$DIND_NAME" sh -c "sha256sum '$_r'" 2>/dev/null)
    _g=${_g%% *}
    if [ -z "$_h" ] || [ "$_h" != "$_g" ]; then
        die "sha256 mismatch/absent for $_r: host=$_h dind=$_g"
    fi
    case "$_r" in
    *.sh | *Dockerfile | *.yaml | *.mod | *.go)
        docker exec "$DIND_NAME" sed -i 's/\r$//' "$_r" ||
            die "strip CR from $_r"
        ;;
    esac
}

# ------------------------------------------------------------------ 构建
log "repo root: $ROOT  tmp: $TMP"
if [ "$CB_SKIP_BUILD" != '1' ]; then
    log "cross-compiling linux/amd64 fleetlyd+fleetly+probe (version $CB_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CB_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CB_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w" \
                -o "$TMP/hello" ./deploy/testdata/probeapp
    ) || die 'go build failed'
    CB_BIN_DIR="$TMP"
else
    CB_BIN_DIR="${CB_BIN_DIR:?CB_SKIP_BUILD=1 requires CB_BIN_DIR}"
    log "using prebuilt binaries from $CB_BIN_DIR"
fi
# 宿主侧只验存在性（Windows 宿主对 Linux ELF 的 -x 恒假——容器内 chmod 兜底）。
for _f in "$CB_BIN_DIR/fleetlyd" "$CB_BIN_DIR/fleetly" "$CB_BIN_DIR/hello"; do
    [ -f "$_f" ] || die "missing: $_f"
done
[ -f "$ROOT/deploy/install.sh" ] || die 'deploy/install.sh missing'
[ -f "$ROOT/e2e/nightly/lib.sh" ] || die 'e2e/nightly/lib.sh missing'

# ---------------------------------------------------------------- 场景素材
# 布局（compose build.context 相对 compose 文件目录解析）：
#   apps/a/{fleetly.yaml, src/{Dockerfile,hello}}   场景 A：Dockerfile 驱动
#   apps/b/{fleetly.yaml, src/{go.mod,main.go}}     场景 B：railpack 检测
#   apps/c/{fleetly.yaml, src/Dockerfile}           场景 C：坏 Dockerfile
APPS="$TMP/apps"
mkdir -p "$APPS/a/src" "$APPS/b/src" "$APPS/c/src" || die 'mkdir apps'
cp "$CB_BIN_DIR/hello" "$APPS/a/src/hello" || die 'cp hello'

cat >"$APPS/a/src/Dockerfile" <<'EOF'
# 场景 A：scratch + 静态探针二进制，零 registry 依赖（buildkit 内建
# dockerfile 前端可解，无需拉基础镜像）。--chmod=0755：COPY 保留源文件
# 权限位，scratch 上无法 RUN chmod——执行位必须在 COPY 时显式给足。
FROM scratch
COPY --chmod=0755 hello /probe
ENTRYPOINT ["/probe", "serve"]
EOF

cat >"$APPS/a/fleetly.yaml" <<'EOF'
name: confa
services:
  web:
    build:
      context: src
      dockerfile: Dockerfile
    expose: ["8080"]
    healthcheck:
      # 受控子集门禁要求 test 以 CMD/CMD-SHELL/NONE 起头（E_COMPOSE_UNSUPPORTED）。
      test: ["CMD", "/probe", "hc"]
    labels:
      fleetly.domains: "confa.test.local"
EOF

cat >"$APPS/b/src/go.mod" <<'EOF'
module hello

go 1.21
EOF

cat >"$APPS/b/src/main.go" <<'EOF'
// 场景 B：railpack 可检测的 Go 上下文（go.mod 探针；stdlib-only，构建
// 不需要模块代理，但需要构建工具链下载——外网依赖，见脚本头注释）。
package main

import (
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from railpack build"))
	})
	_ = http.ListenAndServe(":8080", nil)
}
EOF

cat >"$APPS/b/fleetly.yaml" <<'EOF'
name: confb
services:
  web:
    # 无 dockerfile 键 → railpack 驱动（internal/build DriverFor 裁决）。
    build:
      context: src
    expose: ["8080"]
EOF

cat >"$APPS/c/src/Dockerfile" <<'EOF'
# 场景 C：坏 Dockerfile——COPY 源文件不存在，buildkit 确定性失败。
FROM scratch
COPY missing-artifact.bin /missing-artifact.bin
EOF

cat >"$APPS/c/fleetly.yaml" <<'EOF'
name: confc
services:
  web:
    build:
      context: src
      dockerfile: Dockerfile
    expose: ["8080"]
EOF

# ------------------------------------------------------- 内层断言脚本生成
# 引号 heredoc：不做宿主展开，内层自足；busybox ash 兼容（无 local/[[ ]]）。
cat >"$TMP/cb-boot.sh" <<'BOOT_EOF'
#!/bin/sh
# cb-boot.sh — Builder conformance 起服段（dind 内执行；由宿主编排生成注入）：
# P0 预检 → 安装 → 预拉 ingress 依赖镜像 → 配置 → 起 daemon → liveness 门。
# 断言面（Traefik/buildkit 门 + 场景 A/B/C + finish）在 cb-inner.sh——起服
# 成功后宿主先跑 v0.3 归属管道 fixture（curl helper 注册 founder + 铸用户
# PAT，经 exec+stdin 送回 dind /tmp/cb-token），再执行断言段（见宿主头注）。
set -u
. /tmp/lib.sh

CB_STAGE=/tmp/conformance
INSTALL_SH="$CB_STAGE/install.sh"
APPS="$CB_STAGE/apps"
DLOG=/tmp/cb-fleetlyd.log
PID_FILE=/var/run/fleetlyd.pid
HTTP=http://127.0.0.1:8420

have() { command -v "$1" >/dev/null 2>&1; }

http_code() { # <url> [host] — dind 无 curl，busybox wget -S 状态行走 stderr
    if have curl; then
        if [ -n "${2:-}" ]; then
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: $2" "$1" 2>/dev/null || printf '000'
        else
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1" 2>/dev/null || printf '000'
        fi
    elif have wget; then
        if [ -n "${2:-}" ]; then
            wget -q -S -T 5 -O /dev/null --header "Host: $2" "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        else
            wget -q -S -T 5 -O /dev/null "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        fi
    else
        printf '000'
    fi
}

wait_liveness() { # <budget-s>
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(http_code "$HTTP/healthz/liveness")" = '200' ] && return 0
        sleep 2
    done
    return 1
}

nl "=== Builder conformance boot (stage=$CB_STAGE) ==="

# ------------------------------------------------------------ P0 preflight
for _f in "$INSTALL_SH" "$CB_STAGE/fleetlyd" "$CB_STAGE/fleetly" \
    "$APPS/a/fleetly.yaml" "$APPS/a/src/Dockerfile" "$APPS/a/src/hello" \
    "$APPS/b/fleetly.yaml" "$APPS/b/src/go.mod" "$APPS/b/src/main.go" \
    "$APPS/c/fleetly.yaml" "$APPS/c/src/Dockerfile"; do
    [ -s "$_f" ] || fatal "staged file missing: $_f"
done

# ------------------------------------------------------------ P1 安装+起服
sh "$INSTALL_SH" --bin-dir "$CB_STAGE" --no-systemd >"$CB_STAGE/install.log" 2>&1
RC=$?
assert "CB-P1-install-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    cat "$CB_STAGE/install.log" || true
    finish
fi

# 预拉 ingress 依赖镜像（cert seed 容器与 Traefik 服务不自动拉镜像——
# T2.15 已知边界；test-upgrade.sh 同款预备）。digest 引台账 #3/#6。
docker pull alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc >/dev/null 2>&1 || true
docker pull traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2 >/dev/null 2>&1 || true

mkdir -p /opt/fleetly/etc /var/lib/fleetly
cat > /opt/fleetly/etc/config.yaml <<EOF
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
  deploy_timeout_seconds: 120
  observe_seconds: 5
  replicas_below_seconds: 5
  poll_seconds: 1
  drift_interval_seconds: 3600
backup:
  keep: 7
git:
  enabled: false
logging:
  level: info
EOF
assert "CB-P1-config-written" $?

(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90
assert "CB-P1-liveness-200" $?
if [ "$(http_code "$HTTP/healthz/liveness")" != '200' ]; then
    tail -n 40 "$DLOG" 2>/dev/null || true
    fatal 'daemon not live; cannot continue'
fi
nl 'CB-BOOT-OK daemon live; founder fixture runs host-side next'
exit 0
BOOT_EOF

# 断言段（cb-inner.sh）：Traefik/buildkit 门 + 场景 A/B/C + finish。v0.3
# 归属管道：cli 统一用户 PAT + 显式项目（bootstrap token 弃用，fixture 在
# 宿主段——daemon 起服后注册 founder 铸 PAT 送回 /tmp/cb-token）。
# 引号 heredoc：不做宿主展开，内层自足；busybox ash 兼容（无 local/[[ ]]）。
cat >"$TMP/cb-inner.sh" <<'INNER_EOF'
#!/bin/sh
# cb-inner.sh — Builder conformance 断言段（dind 内执行；宿主编排生成注入，
# 在 cb-boot.sh 起服 + 宿主 v0.3 fixture 之后运行）。断言风格与
# e2e/nightly/lib.sh 一致（NAME: PASS/FAIL + 计数 + finish）。
set -u
. /tmp/lib.sh

CB_STAGE=/tmp/conformance
APPS="$CB_STAGE/apps"
HTTP=http://127.0.0.1:8420
DOMAIN_A=confa.test.local

have() { command -v "$1" >/dev/null 2>&1; }

http_code() { # <url> [host] — dind 无 curl，busybox wget -S 状态行走 stderr
    if have curl; then
        if [ -n "${2:-}" ]; then
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: $2" "$1" 2>/dev/null || printf '000'
        else
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1" 2>/dev/null || printf '000'
        fi
    elif have wget; then
        if [ -n "${2:-}" ]; then
            wget -q -S -T 5 -O /dev/null --header "Host: $2" "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        else
            wget -q -S -T 5 -O /dev/null "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        fi
    else
        printf '000'
    fi
}

json_str() { # <json> <key> — 定点字段提取（indent JSON；非通用解析器）
    printf '%s' "$1" |
        grep -o "\"$2\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/.*:[[:space:]]*"//; s/"$//'
}

# v0.3 归属管道：用户 PAT（宿主 fixture 经 exec+stdin 送入 /tmp/cb-token；
# bootstrap token 已随首用户注册按设计吊销弃用）+ 显式项目归属（部署/构建
# 面必须携带 FLEETLY_PROJECT）。
TOKEN=$(cat /tmp/cb-token 2>/dev/null)
[ -n "$TOKEN" ] || fatal 'founder PAT missing at /tmp/cb-token (host fixture did not run?)'
cli() { # <args...> — 带连接 env 的 fleetly CLI（gRPC 面）
    FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN="$TOKEN" FLEETLY_PROJECT=founder/default \
        /opt/fleetly/bin/fleetly "$@"
}

traefik_ready() {
    docker service ls --format '{{.Name}} {{.Replicas}}' 2>/dev/null |
        grep -q 'fleetly-ingress 1/1'
}
wait_traefik() { # <budget-s>
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        traefik_ready && return 0
        sleep 3
    done
    return 1
}
route_ok() { # <domain>
    [ "$(http_code "http://127.0.0.1/" "$1")" = '200' ]
}
wait_route() { # <domain> <budget-s>
    _deadline=$(( $(date +%s) + $2 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        route_ok "$1" && return 0
        sleep 2
    done
    return 1
}
derived_states() {
    cli apps list --json 2>/dev/null |
        grep -o '"derived_state": *"[a-z]*"' |
        sed 's/.*: *"//; s/"$//' |
        sort
}
first_derived_state() {
    derived_states | head -n 1
}
wait_app_running() { # <budget-s>
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(first_derived_state)" = 'running' ] && return 0
        sleep 2
    done
    return 1
}

nl "=== Builder conformance inner (stage=$CB_STAGE) ==="

# Traefik 就绪（daemon 启动期拉起 fleetly-ingress global service）。
wait_traefik 240
assert "CB-P1-traefik-1-1" $?

# buildkitd 收敛门：daemon 后台预热（warmDaemon，预算 6 分钟）与首次构建的
# ensureDaemonReady 并发时会争抢创建 fleetly-buildkit 容器（create 撞 name
# conflict 的一方会空转重试直至预算耗尽——本机实测复现，场景 A 曾因此
# E_BUILD_FAILED）。此门等预热收敛到容器 Running 后再发首个构建：单竞争者
# 无冲突；若预热超预算放弃，后续构建的 ensure 仍会自建（幂等路径）。
wait_buildkitd() { # <budget-s> — fleetly-buildkit 容器进入 Running
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(docker inspect -f '{{.State.Running}}' fleetly-buildkit 2>/dev/null)" = 'true' ] && return 0
        sleep 5
    done
    return 1
}
wait_buildkitd 480
assert "CB-P1-buildkitd-running" $?
docker image ls moby/buildkit --format '{{.Repository}}:{{.Tag}}' | head -n 1

# ------------------------------------------------- 场景 A：Dockerfile 驱动
nl "--- scenario A: dockerfile driver (app confa) ---"
cli build --timeout 15m --json "$APPS/a/fleetly.yaml" >"$CB_STAGE/build-a.json" 2>"$CB_STAGE/build-a.err"
RC=$?
assert "CB-A1-build-rc0" "$RC" "$(tail -n 3 "$CB_STAGE/build-a.err")"
A_JSON=$(cat "$CB_STAGE/build-a.json" 2>/dev/null || printf '{}')
[ "$(json_str "$A_JSON" status)" = 'succeeded' ]
assert "CB-A2-build-succeeded" $? "status=$(json_str "$A_JSON" status) err=$(json_str "$A_JSON" error_code)"
[ "$(json_str "$A_JSON" driver)" = 'dockerfile' ]
assert "CB-A3-driver-dockerfile" $? "driver=$(json_str "$A_JSON" driver)"
A_DIGEST=$(json_str "$A_JSON" image_digest)
printf '%s' "$A_DIGEST" | grep -q '^sha256:[0-9a-f]\{64\}$'
assert "CB-A4-image-digest-sha256" $? "digest=$A_DIGEST"
A_REF=$(json_str "$A_JSON" image_ref)
docker image inspect "$A_REF" >/dev/null 2>&1
assert "CB-A5-image-local-present" $? "ref=$A_REF"

cli deploy --timeout 240s "$APPS/a/fleetly.yaml" >"$CB_STAGE/deploy-a.log" 2>&1
RC=$?
assert "CB-A6-deploy-rc0" "$RC" "$(tail -n 3 "$CB_STAGE/deploy-a.log")"
wait_app_running 60
assert "CB-A7-app-running" $? "state=$(first_derived_state)"
wait_route "$DOMAIN_A" 60
assert "CB-A8-route-200" $? "route=$(http_code "http://127.0.0.1/" "$DOMAIN_A")"

# --------------------------------------- 场景 B：无 Dockerfile（railpack）
# 判定线（验收标准原文「至少断言裁决正确」）：B1 驱动裁决 = railpack 必须过；
# 构建终态（succeeded/failed）都合法，但终态证据必须成立（B3）——成功带
# digest（且 plan 归档），失败带 E_BUILD_FAILED 信封。结论行 CB-B-OUTCOME
# 供 job summary 收敛。
nl "--- scenario B: railpack detection (app confb) ---"
cli build --timeout 30m --json "$APPS/b/fleetly.yaml" >"$CB_STAGE/build-b.json" 2>"$CB_STAGE/build-b.err"
RC=$?
B_JSON=$(cat "$CB_STAGE/build-b.json" 2>/dev/null || printf '{}')
[ "$(json_str "$B_JSON" driver)" = 'railpack' ]
assert "CB-B1-driver-railpack-verdict" $? "driver=$(json_str "$B_JSON" driver) rc=$RC"
B_STATUS=$(json_str "$B_JSON" status)
case "$B_STATUS" in
succeeded | failed)
    assert "CB-B2-terminal-state-reached" 0 "status=$B_STATUS"
    ;;
*)
    assert "CB-B2-terminal-state-reached" 1 "status=$B_STATUS rc=$RC"
    ;;
esac
case "$B_STATUS" in
succeeded)
    printf '%s' "$(json_str "$B_JSON" image_digest)" | grep -q '^sha256:'
    assert "CB-B3-success-digest-evidence" $? "digest=$(json_str "$B_JSON" image_digest)"
    [ -n "$(json_str "$B_JSON" plan_path)" ]
    assert "CB-B4-plan-archived" $? "plan_path=$(json_str "$B_JSON" plan_path)"
    nl "CB-B-OUTCOME railpack build succeeded"
    ;;
failed)
    [ "$(json_str "$B_JSON" error_code)" = 'E_BUILD_FAILED' ]
    assert "CB-B3-failure-envelope-evidence" $? "error_code=$(json_str "$B_JSON" error_code)"
    nl "CB-B-OUTCOME railpack build failed (detection verdict correct; diagnostics below)"
    B_LOG=$(json_str "$B_JSON" log_path)
    [ -n "$B_LOG" ] && tail -n 15 "$B_LOG" 2>/dev/null | sed 's/^/    /' || true
    ;;
esac

# --------------------------------------------- 场景 C：坏 Dockerfile 信封
nl "--- scenario C: broken Dockerfile (app confc) ---"
cli build --timeout 10m "$APPS/c/fleetly.yaml" >"$CB_STAGE/build-c.log" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "CB-C1-build-rejected-rc" $? "rc=$RC (want non-zero)"
cli builds list --limit 1 --json confc >"$CB_STAGE/builds-c.json" 2>/dev/null
C_JSON=$(cat "$CB_STAGE/builds-c.json" 2>/dev/null || printf '{}')
[ "$(json_str "$C_JSON" status)" = 'failed' ]
assert "CB-C2-build-row-failed" $? "status=$(json_str "$C_JSON" status)"
[ "$(json_str "$C_JSON" error_code)" = 'E_BUILD_FAILED' ]
assert "CB-C3-build-envelope-code" $? "error_code=$(json_str "$C_JSON" error_code)"

cli deploy --timeout 240s "$APPS/c/fleetly.yaml" >"$CB_STAGE/deploy-c.log" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "CB-C4-deploy-rejected-rc" $? "rc=$RC (want non-zero)"
cli deployments list --limit 1 --json confc >"$CB_STAGE/deployments-c.json" 2>/dev/null
D_JSON=$(cat "$CB_STAGE/deployments-c.json" 2>/dev/null || printf '{}')
[ "$(json_str "$D_JSON" status)" = 'failed' ]
assert "CB-C5-deployment-failed-row" $? "status=$(json_str "$D_JSON" status)"
[ "$(json_str "$D_JSON" error_code)" = 'E_BUILD_FAILED' ]
assert "CB-C6-deployment-envelope-code" $? "error_code=$(json_str "$D_JSON" error_code)"

finish
INNER_EOF

# ----------------------------------------------------------------- dind
# 宿主私网（fixture 的 curl helper 要能直达 REST 面——默认 bridge 无钉定
# IP；子网避开其余 e2e 套件已占网段，见头注）。
docker network create -d bridge --subnet "$CB_BR_SUBNET" "$CB_BR_NET" >/dev/null ||
    die "create $CB_BR_NET"
docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
docker run -d --name "$DIND_NAME" --privileged \
    --network "$CB_BR_NET" --ip "$DIND_IP" "$DIND_IMAGE" >/dev/null ||
    die "docker run $DIND_NAME"
_i=0
while ! docker exec "$DIND_NAME" docker info >/dev/null 2>&1; do
    _i=$((_i + 2))
    if [ "$_i" -ge 60 ]; then
        docker logs "$DIND_NAME" --tail 40 || true
        docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
        die 'inner dockerd not ready within 60s'
    fi
    sleep 2
done
log "dind $DIND_NAME ready"

# ----------------------------------------------------------------- 注入
log 'staging scripts + binaries + scenario fixtures via exec+stdin'
docker exec "$DIND_NAME" mkdir -p /tmp/conformance/bin \
    /tmp/conformance/apps/a/src /tmp/conformance/apps/b/src \
    /tmp/conformance/apps/c/src || die 'mkdir stage'
stage /tmp/lib.sh "$ROOT/e2e/nightly/lib.sh"
stage /tmp/conformance/install.sh "$ROOT/deploy/install.sh"
stage /tmp/cb-boot.sh "$TMP/cb-boot.sh"
stage /tmp/cb-inner.sh "$TMP/cb-inner.sh"
stage /tmp/conformance/fleetlyd "$CB_BIN_DIR/fleetlyd"
stage /tmp/conformance/fleetly "$CB_BIN_DIR/fleetly"
docker exec "$DIND_NAME" chmod +x /tmp/conformance/fleetlyd \
    /tmp/conformance/fleetly || die 'chmod binaries'
# 场景 A 的镜像载荷 = 探针二进制（hello；镜像内路径 /probe）。staged 文件
# 是 cat > 直传（0644）——容器内 chmod +x 兜底（镜像侧再由 COPY --chmod 保底）。
stage /tmp/conformance/apps/a/src/hello "$APPS/a/src/hello"
docker exec "$DIND_NAME" chmod +x /tmp/conformance/apps/a/src/hello || die 'chmod hello'
stage /tmp/conformance/apps/a/src/Dockerfile "$APPS/a/src/Dockerfile"
stage /tmp/conformance/apps/a/fleetly.yaml "$APPS/a/fleetly.yaml"
stage /tmp/conformance/apps/b/src/go.mod "$APPS/b/src/go.mod"
stage /tmp/conformance/apps/b/src/main.go "$APPS/b/src/main.go"
stage /tmp/conformance/apps/b/fleetly.yaml "$APPS/b/fleetly.yaml"
stage /tmp/conformance/apps/c/src/Dockerfile "$APPS/c/src/Dockerfile"
stage /tmp/conformance/apps/c/fleetly.yaml "$APPS/c/fleetly.yaml"

# ----------------------------------------------------------------- 执行
log 'running in-dind boot (cb-boot.sh)'
docker exec "$DIND_NAME" sh /tmp/cb-boot.sh
RC=$?
if [ "$RC" -ne 0 ]; then
    log "boot RED (rc=$RC) -- dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
    exit "$RC"
fi

# ── v0.3 归属管道 fixture（宿主侧；cron.sh/databases.sh/multinode-rehearsal.sh
# 同款）：curl helper 注册 founder（首用户 = 平台管理员 + 个人队 + 默认项目
# default）→ 会话自服务铸用户 PAT（admin scope——founder 是平台管理员可达
# 集；CLI 不消费会话 cookie）。PAT 经 exec+stdin 送回 dind /tmp/cb-token，
# 断言段的 cli() 统一携带 FLEETLY_PROJECT=founder/default。bootstrap token
# 已随首用户注册按设计吊销弃用。
CURLER="$DIND_NAME-curl"
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$CB_BR_NET" "$CURL_IMAGE" sleep 100000 >/dev/null ||
    die "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://$DIND_IP:8420/healthz/liveness" ||
    die 'curl helper cannot reach the REST face'
docker exec "$CURLER" curl -s -c /tmp/jar -X POST "http://$DIND_IP:8420/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"founder@e2e.test","password":"founder-pass-1","display_name":"Founder"}' \
    >/dev/null || die 'founder register'
CB_TOKEN=$(docker exec "$CURLER" curl -s -b /tmp/jar -X POST "http://$DIND_IP:8420/v1/tokens" \
    -H 'Content-Type: application/json' \
    -d '{"note":"e2e pat","scopes":["admin"]}' | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$CB_TOKEN" ] || die 'founder PAT mint failed'
# curl helper 用毕即除（fixture 只承担注册与铸 PAT；避免钉住 bridge 网络）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
printf '%s' "$CB_TOKEN" >"$TMP/cb-token"
stage /tmp/cb-token "$TMP/cb-token"
log 'founder registered (platform admin); PAT minted and staged; project context founder/default'

log 'running in-dind conformance suite (cb-inner.sh)'
docker exec "$DIND_NAME" sh /tmp/cb-inner.sh
RC=$?
if [ "$RC" -ne 0 ]; then
    log "suite RED (rc=$RC) -- dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
fi
cleanup
if [ "$RC" -eq 0 ]; then
    log 'CONFORMANCE-BUILDER GREEN'
else
    log "CONFORMANCE-BUILDER RED (rc=$RC)"
fi
exit "$RC"
