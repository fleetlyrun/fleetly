#!/bin/sh
# deploy/run-upgrade-test.sh — T2.23 自升级 dind 验收的宿主编排（模式照抄
# run-dind-test.sh：交叉编译两份不同版本串的 fleetlyd+fleetly + 探针应用 →
# 起 docker:29.8.1-dind 特权容器 → exec+stdin 注入（禁用 docker cp——
# Engine 29.x 宿主→特权 dind 会 exit 0 但文件不落盘）→ 容器内跑
# test-upgrade.sh → 失败时 dump dind 日志 → 清理。可在 GitHub Actions
# （ubuntu-latest）与本地 Git Bash（Windows）/ Linux shell 复跑。
#
# usage: run-upgrade-test.sh
# env:
#   DIND_IMAGE      dind 镜像（默认 docker:29.8.1-dind，与 CI/引擎门禁一致）
#   DIND_NAME       容器名（默认 fleetly-upgrade-test）
#   UG_SKIP_BUILD   1 = 跳过交叉编译，改用 UG_BIN_A / UG_BIN_DIR 指定的目录
#   UG_BIN_A        UG_SKIP_BUILD=1 时的 vA 二进制目录（含 fleetlyd+fleetly）
#   UG_BIN_B        UG_SKIP_BUILD=1 时的 vB 二进制目录（含 fleetlyd+fleetly）
#   UG_BIN_C        UG_SKIP_BUILD=1 时的 vC fleetlyd（schema-skew 变体，S4；
#                   只需 fleetlyd——CLI 由 test-upgrade.sh 在 dind 内伪造）
#   UG_VERSION_A    vA 版本串（默认 v0.1.0-uga）
#   UG_VERSION_B    vB 版本串（默认 v0.1.0-ugb；必须 ≠ A）
#   UG_VERSION_C    vC 版本串（默认 v0.1.0-ugc；S4 变体——见下方 vc 变体说明）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# dind 镜像钉 digest（T0-V2.3 供应链）：默认值 tag@sha256——tag 保留作可读性，
# digest 为准；env 覆盖仍可用（显式传入即按传入值起容器）。
DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
DIND_NAME="${DIND_NAME:-fleetly-upgrade-test}"
UG_SKIP_BUILD="${UG_SKIP_BUILD:-0}"
UG_VERSION_A="${UG_VERSION_A:-v0.1.0-uga}"
UG_VERSION_B="${UG_VERSION_B:-v0.1.0-ugb}"
UG_VERSION_C="${UG_VERSION_C:-v0.1.0-ugc}"

log() { printf '[upgrade-test %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}
cleanup() {
    docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
}
trap 'rm -rf "$TMP"; cleanup' EXIT

# posix_path <p> — 反斜杠路径归一为正斜杠（MSYS/Windows 宿主防御，
# run-dind-test.sh 同款）。
posix_path() {
    printf '%s' "$1" | tr '\\' '/'
}

# $0 可能带反斜杠（Git Bash 下 cmd 调用）——先归一再取目录。
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——cygpath -m 归一为 C:/… 混合
# 形态（sh/go/docker 三方都认）；非 Cygwin 宿主原样归一。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || die 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || die 'docker not on PATH'
command -v go >/dev/null 2>&1 || UG_SKIP_BUILD=1

# stage <remote-path> <local-file> — exec+stdin 直传 + 两侧 sha256 校验 +
# CR 剥离（仅文本脚本；二进制绝不 sed——ELF 字节对被破坏曾致 segfault）。
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
    *.sh | *Dockerfile | *.yaml)
        docker exec "$DIND_NAME" sed -i 's/\r$//' "$_r" ||
            die "strip CR from $_r"
        ;;
    esac
}

# ------------------------------------------------------------------ 构建
log "repo root: $ROOT  tmp: $TMP"
if [ "$UG_SKIP_BUILD" != '1' ]; then
    mkdir -p "$TMP/bin-vA" "$TMP/bin-vB" "$TMP/bin-vC"
    log "cross-compiling linux/amd64 fleetlyd+fleetly (vA=$UG_VERSION_A, vB=$UG_VERSION_B)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$UG_VERSION_A" \
                -o "$TMP/bin-vA/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$UG_VERSION_A" \
                -o "$TMP/bin-vA/fleetly" ./cmd/fleetly &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$UG_VERSION_B" \
                -o "$TMP/bin-vB/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$UG_VERSION_B" \
                -o "$TMP/bin-vB/fleetly" ./cmd/fleetly &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w" \
                -o "$TMP/probe" ./deploy/testdata/probeapp
    ) || die 'go build failed'
    # vC 变体（S4/F5，S20 schema 感知场景）：临时源树副本注入一条 vA/vB
    # 都不认识的更高版本迁移（00099）——go:embed 编译期读取，只能改树构建。
    # vC = 「新版本应用迁移 → 验证失败 → 回退旧版本」的错配源。只构建
    # fleetlyd（触发错位的 CLI 由 test-upgrade.sh 在 dind 内伪造）。
    log "building vC schema-skew variant ($UG_VERSION_C, injected migration 00099)"
    VC_SRC="$TMP/vc-src"
    mkdir -p "$VC_SRC"
    # 排除重目录只影响复制速度，不参与编译闭包（go.work 三模块 = cmd/
    # internal/genproto/sdk）。
    tar -C "$ROOT" -cf - \
        --exclude=./.git --exclude=./console --exclude=./spike \
        --exclude=./docs --exclude=./.tmp-* --exclude=./cmd/fleetlyd/fleetlyd.exe \
        . | tar -C "$VC_SRC" -xf - ||
        die 'copy source tree for vC variant'
    cat >"$VC_SRC/internal/state/migrations/00099_f5_schema_skew.sql" <<'MIG'
-- 00099_f5_schema_skew.sql —— 测试专用注入迁移（deploy/run-upgrade-test.sh
-- 构建 vC 变体时写入临时源树副本，绝不进仓库）：制造「新版本 fleetlyd 应
-- 用了旧版本不认识的更高 schema 迁移」的回退错配现场（S4/F5，S20）。
CREATE TABLE f5_schema_skew_marker (id INTEGER PRIMARY KEY, note TEXT NOT NULL);
MIG
    (
        cd "$VC_SRC" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$UG_VERSION_C" \
                -o "$TMP/bin-vC/fleetlyd" ./cmd/fleetlyd
    ) || die 'go build of vC variant failed'
    UG_BIN_A="$TMP/bin-vA"
    UG_BIN_B="$TMP/bin-vB"
    UG_BIN_C="$TMP/bin-vC/fleetlyd"
    UG_PROBE="$TMP/probe"
else
    UG_BIN_A="${UG_BIN_A:?UG_SKIP_BUILD=1 requires UG_BIN_A}"
    UG_BIN_B="${UG_BIN_B:?UG_SKIP_BUILD=1 requires UG_BIN_B}"
    UG_BIN_C="${UG_BIN_C:?UG_SKIP_BUILD=1 requires UG_BIN_C (vC schema-skew fleetlyd)}"
    UG_PROBE="${UG_PROBE:?UG_SKIP_BUILD=1 requires UG_PROBE (probeapp binary)}"
    log "using prebuilt binaries: A=$UG_BIN_A B=$UG_BIN_B C=$UG_BIN_C probe=$UG_PROBE"
fi
# 宿主侧只验存在性（Windows 宿主对 Linux ELF 的 -x 恒假——容器内 chmod 兜底）。
for _f in "$UG_BIN_A/fleetlyd" "$UG_BIN_A/fleetly" "$UG_BIN_B/fleetlyd" "$UG_BIN_B/fleetly" "$UG_BIN_C" "$UG_PROBE"; do
    [ -f "$_f" ] || die "missing: $_f"
done
for _f in deploy/install.sh deploy/upgrade.sh deploy/test-upgrade.sh; do
    [ -f "$ROOT/$_f" ] || die "missing: $ROOT/$_f"
done

# 宿主侧构建探针镜像（scratch + 静态二进制，零 registry 依赖）并导出：
# dind 内不做 docker build（buildkit 形态差异），统一 docker load。
printf 'FROM scratch\nCOPY probe /probe\nENTRYPOINT ["/probe"]\n' >"$TMP/probeimg.Dockerfile"
mkdir -p "$TMP/probeimg"
cp "$UG_PROBE" "$TMP/probeimg/probe"
docker build --platform linux/amd64 -t probeapp:1 -f "$TMP/probeimg.Dockerfile" "$TMP/probeimg" ||
    die 'host docker build of probe image failed'
docker save probeapp:1 | gzip >"$TMP/probe.tar.gz" ||
    die 'docker save of probe image failed'
log "probe image tarball: $(ls -la "$TMP/probe.tar.gz" 2>/dev/null | tr -s ' ')"

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
docker exec "$DIND_NAME" mkdir -p /tmp/upgrade-test/bin-vA /tmp/upgrade-test/bin-vB /tmp/upgrade-test/bin-vC ||
    die 'mkdir stage'
stage /tmp/upgrade-test/install.sh "$ROOT/deploy/install.sh"
stage /tmp/upgrade-test/upgrade.sh "$ROOT/deploy/upgrade.sh"
stage /tmp/upgrade-test/test-upgrade.sh "$ROOT/deploy/test-upgrade.sh"
stage /tmp/upgrade-test/probe.tar.gz "$TMP/probe.tar.gz"
stage /tmp/upgrade-test/probe "$UG_PROBE"
docker exec "$DIND_NAME" chmod +x /tmp/upgrade-test/probe || die 'chmod probe'
stage /tmp/upgrade-test/bin-vA/fleetlyd "$UG_BIN_A/fleetlyd"
stage /tmp/upgrade-test/bin-vA/fleetly "$UG_BIN_A/fleetly"
stage /tmp/upgrade-test/bin-vB/fleetlyd "$UG_BIN_B/fleetlyd"
stage /tmp/upgrade-test/bin-vB/fleetly "$UG_BIN_B/fleetly"
stage /tmp/upgrade-test/bin-vC/fleetlyd "$UG_BIN_C"
docker exec "$DIND_NAME" chmod +x /tmp/upgrade-test/bin-vA/fleetlyd \
    /tmp/upgrade-test/bin-vA/fleetly /tmp/upgrade-test/bin-vB/fleetlyd \
    /tmp/upgrade-test/bin-vB/fleetly /tmp/upgrade-test/bin-vC/fleetlyd || die 'chmod binaries'

# ----------------------------------------------------------------- 执行
log 'running in-dind suite (deploy/test-upgrade.sh)'
docker exec "$DIND_NAME" sh /tmp/upgrade-test/test-upgrade.sh
RC=$?
if [ "$RC" -ne 0 ]; then
    log "suite RED (rc=$RC) — dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
fi
cleanup
if [ "$RC" -eq 0 ]; then
    log 'UPGRADE-TEST GREEN'
else
    log "UPGRADE-TEST RED (rc=$RC)"
fi
exit "$RC"
