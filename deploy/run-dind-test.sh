#!/bin/sh
# deploy/run-dind-test.sh — T2.1 安装器 dind 验收的宿主编排（T2.1 验收标准 2：
# "dind 内 test-install.sh 全断言通过"）。
#
# 做什么：交叉编译 linux/amd64 fleetlyd+fleetly（或复用 TI_BIN_DIR 里的现成
# 二进制）→ 起 docker:29.8.1-dind 特权容器 → 经 exec+stdin 注入脚本与二进制
# （禁用 docker cp——Engine 29.x 宿主→特权 dind 会 exit 0 但文件不落盘，见
# e2e/README.md 已知问题）→ 容器内跑 test-install.sh → 失败时 dump dind 日志
# → 清理。可在 GitHub Actions（ubuntu-latest）与本地 Git Bash（Windows）/
# Linux shell 复跑，命令路径与 CI 一致。
#
# usage: run-dind-test.sh
# env:
#   DIND_IMAGE      dind 镜像（默认 docker:29.8.1-dind，与 CI/引擎门禁一致）
#   DIND_NAME       容器名（默认 fleetly-install-test）
#   TI_SKIP_BUILD   1 = 跳过交叉编译，改用 TI_BIN_DIR 指定的二进制目录
#   TI_BIN_DIR      TI_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   TI_VERSION      注入的版本串（默认 v0.1.0-stage）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# dind 镜像钉 digest（T0-V2.3 供应链）：默认值 tag@sha256——tag 保留作可读性，
# digest 为准；env 覆盖仍可用（显式传入即按传入值起容器）。
DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
DIND_NAME="${DIND_NAME:-fleetly-install-test}"
TI_SKIP_BUILD="${TI_SKIP_BUILD:-0}"
TI_VERSION="${TI_VERSION:-v0.1.0-stage}"

log() { printf '[install-test %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}
cleanup() {
    docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
}
trap 'rm -rf "$TMP"; cleanup' EXIT

# posix_path <p> — 反斜杠路径归一为正斜杠（Windows 宿主上 mktemp 可能返回
# C:\… 形式；正斜杠形式 sh/go/docker 三方都无歧义）。Linux 宿主原样返回。
posix_path() {
    printf '%s' "$1" | tr '\\' '/'
}

# $0 可能带反斜杠（Git Bash 下 cmd 调用）——先归一再取目录，否则 dirname
# 返回 "." 导致 ROOT 落错、go build 在错误目录执行。
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——原生 go.exe 在 MSYS_NO_PATHCONV=1
# 下拿不到映射，会把产物写到别的盘符；经 cygpath -m 归一为 C:/… 混合形态
# （sh/go/docker 三方都认）。非 Cygwin 宿主原样归一。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || die 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || die 'docker not on PATH'
command -v go >/dev/null 2>&1 || TI_SKIP_BUILD=1

# stage <remote-path> <local-file> — exec+stdin 直传 + 两侧 sha256 校验 +
# CR 剥离（仅对文本脚本：busybox ash 无法执行 CRLF；本地检出可能是 CRLF。
# 二进制绝不 sed——ELF 里的 \r\n 字节对会被破坏，曾致 daemon segfault）。
# 宿主侧 sha256 经 stdin 喂给原生 sha256sum（Windows 宿主上原生 exe 打不开
# /d/… 形式的 MSYS 路径，stdin 重定向由 sh 代开，无路径问题）；哈希解析用
# 纯 shell（宿主 PATH 上无 sed/awk）。
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
    *.sh | *.service | *.pem)
        # PEM 同样剥 CR：openssl 的 PEM 解析不吃 CRLF（Windows 检出风险面
        # 同脚本；A11 双轨验签断言依赖这把测试私钥可直接签名）。
        docker exec "$DIND_NAME" sed -i 's/\r$//' "$_r" ||
            die "strip CR from $_r"
        ;;
    esac
}

# ------------------------------------------------------------------ 构建
log "repo root: $ROOT  tmp: $TMP"
if [ "$TI_SKIP_BUILD" != '1' ]; then
    log "cross-compiling linux/amd64 fleetlyd+fleetly (version $TI_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$TI_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$TI_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || die 'go build failed'
    log "build artifacts: $(ls -la "$TMP" 2>/dev/null | tr '\n' ' ')"
    TI_BIN_DIR="$TMP"
else
    TI_BIN_DIR="${TI_BIN_DIR:?TI_SKIP_BUILD=1 requires TI_BIN_DIR}"
    log "using prebuilt binaries from $TI_BIN_DIR"
fi
# 注意：宿主侧只验存在性（-f）——Windows 宿主（Git Bash）上 [ -x ] 对
# Linux ELF 恒为假（无 .exe 扩展、无 PE 头，MSYS 判其不可执行）；可执行性
# 由容器内 chmod +x + 套件断言兜底。
[ -f "$TI_BIN_DIR/fleetlyd" ] || die "fleetlyd missing in $TI_BIN_DIR"
[ -f "$TI_BIN_DIR/fleetly" ] || die "fleetly missing in $TI_BIN_DIR"
[ -f "$ROOT/deploy/install.sh" ] || die 'deploy/install.sh missing'
[ -f "$ROOT/deploy/uninstall.sh" ] || die 'deploy/uninstall.sh missing'
[ -f "$ROOT/deploy/fleetlyd.service" ] || die 'deploy/fleetlyd.service missing'
[ -f "$ROOT/deploy/test-install.sh" ] || die 'deploy/test-install.sh missing'
[ -f "$ROOT/deploy/upgrade.sh" ] || die 'deploy/upgrade.sh missing'
[ -f "$ROOT/deploy/testdata/release-test-key.pem" ] ||
    die 'deploy/testdata/release-test-key.pem missing (A11 openssl-track asserts need the test signing key)'

# ----------------------------------------------------------------- dind
docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
docker run -d --name "$DIND_NAME" --privileged "$DIND_IMAGE" >/dev/null ||
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
log 'staging scripts + binaries via exec+stdin'
docker exec "$DIND_NAME" mkdir -p /tmp/install-test/bin /tmp/install-test/testdata || die 'mkdir stage'
stage /tmp/install-test/install.sh "$ROOT/deploy/install.sh"
stage /tmp/install-test/uninstall.sh "$ROOT/deploy/uninstall.sh"
stage /tmp/install-test/fleetlyd.service "$ROOT/deploy/fleetlyd.service"
stage /tmp/install-test/test-install.sh "$ROOT/deploy/test-install.sh"
stage /tmp/install-test/upgrade.sh "$ROOT/deploy/upgrade.sh"
stage /tmp/install-test/testdata/release-test-key.pem "$ROOT/deploy/testdata/release-test-key.pem"
stage /tmp/install-test/bin/fleetlyd "$TI_BIN_DIR/fleetlyd"
stage /tmp/install-test/bin/fleetly "$TI_BIN_DIR/fleetly"
docker exec "$DIND_NAME" chmod +x /tmp/install-test/bin/fleetlyd /tmp/install-test/bin/fleetly ||
    die 'chmod binaries'

# ----------------------------------------------------------------- 执行
log 'running in-dind suite (deploy/test-install.sh)'
docker exec "$DIND_NAME" sh /tmp/install-test/test-install.sh
RC=$?
if [ "$RC" -ne 0 ]; then
    log "suite RED (rc=$RC) — dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
fi
cleanup
if [ "$RC" -eq 0 ]; then
    log 'INSTALL-TEST GREEN'
else
    log "INSTALL-TEST RED (rc=$RC)"
fi
exit "$RC"
