#!/bin/sh
# deploy/run-calibration.sh — T2.25 资源预算与容量校准的宿主编排（架构 §4.2
# 横切：控制面 idle 内存实测含 dockerd+swarmkit、Traefik 单列；≤50 apps /
# ≤200 域名 / 并发构建 2 容量压测——压测后修正建议值）。模式照抄
# run-upgrade-test.sh / e2e/nightly/conformance-builder.sh：交叉编译
# fleetlyd+fleetly+探针 → 宿主构建 probeapp:1 镜像（docker save|gzip）→ 起
# docker:29.8.1-dind 特权容器 → exec+stdin 注入（禁用 docker cp——Engine
# 29.x 宿主→特权 dind 会 exit 0 但文件不落盘，e2e/README.md 已知问题）→
# 容器内跑 cal-inner.sh → 产物（summary.md/cal.json）经 docker exec cat 读回
# → 清理。可在 GitHub Actions（ubuntu-latest）与本地 Git Bash（Windows）/
# Linux shell 复跑。
#
# 场景（T2.25 验收标准）：
#   P1  安装 + 自写配置（ACME 关、git 关、快引擎参数）+ daemon live + token
#   P2  idle 基线：稳定窗 ≥120s 后连采——fleetlyd VmRSS 多拍（min/avg/max）+
#       dockerd RSS（含 swarmkit，同进程）+ containerd RSS（如实单列）+
#       Traefik（fleetly-ingress）与 buildkit（fleetly-buildkit）docker stats
#       单列；对照 <200MB 预算口径出数
#   P3  容量压测：50 apps 顺序部署（cap01–cap20 每应用 2 服务 × 5 域名，
#       cap21–cap50 单服务无域名 → 50 apps / 200 域名 / 70 swarm services）
#       ——逐部署时延（队列吞吐）、全部 succeeded、derived_state=running、
#       域名台账计数=200、/configs 视图合成非空且含 200 域名、Traefik 实际
#       路由 200 抽检、fleetlyd/dockerd 内存增量
#   P4  并发构建 2：5 个独立 dockerfile 构建并发触发 → 采样器断言同时在建
#       ≤2（并发=2）且出现过排队 → 全部 succeeded + 每构建独立 digest
#       （payload 逐构建随机，digest 必异 = 无交叉污染）
#
# usage: run-calibration.sh
# env:
#   DIND_IMAGE     dind 镜像（默认 docker:29.8.1-dind，与 CI/引擎门禁一致）
#   DIND_NAME      容器名（默认 fleetly-calibration）
#   CAL_SKIP_BUILD 1 = 跳过交叉编译，改用 CAL_BIN_DIR 指定的二进制目录
#   CAL_BIN_DIR    CAL_SKIP_BUILD=1 时的二进制目录（需含 fleetlyd 与 fleetly）
#   CAL_VERSION    注入的版本串（默认 v0.1.0-cal）
#   CAL_SETTLE     idle 稳定窗秒（默认 120；T2.25 要求 ≥120s）
#   CAL_SAMPLES    idle RSS 采样拍数（默认 12）
#   CAL_INTERVAL   采样间隔秒（默认 5）
#   CAL_APPS       压测应用数（默认 50；cap01..capNN，前 20 个带 10 域名）
#   CAL_DOMAIN_APPS 带域名应用数（默认 20；每应用 10 域名 → 200 域名）
#   CAL_BUILDS     并发构建压测的构建数（默认 5，须 >2 才能断言排队）
#   CAL_MEMORY     dind 容器内存上限（默认 6g；0 = 不设限——本机资源防护）
#   CAL_OUT_DIR    宿主侧产物目录（默认 $TMP/calibration）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind}"
DIND_NAME="${DIND_NAME:-fleetly-calibration}"
CAL_SKIP_BUILD="${CAL_SKIP_BUILD:-0}"
CAL_VERSION="${CAL_VERSION:-v0.1.0-cal}"
CAL_SETTLE="${CAL_SETTLE:-120}"
CAL_SAMPLES="${CAL_SAMPLES:-12}"
CAL_INTERVAL="${CAL_INTERVAL:-5}"
CAL_APPS="${CAL_APPS:-50}"
CAL_DOMAIN_APPS="${CAL_DOMAIN_APPS:-20}"
CAL_BUILDS="${CAL_BUILDS:-5}"
CAL_MEMORY="${CAL_MEMORY:-6g}"

log() { printf '[calibration %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}
cleanup() {
    docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
}
trap 'rm -rf "$TMP"; cleanup' EXIT

posix_path() {
    printf '%s' "$1" | tr '\\' '/'
}

SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || die 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || die 'docker not on PATH'
command -v go >/dev/null 2>&1 || CAL_SKIP_BUILD=1

stage() { # <remote-path> <local-file> — exec+stdin 直传 + sha256 双校验 + CR 剥离
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
    *.sh)
        docker exec "$DIND_NAME" sed -i 's/\r$//' "$_r" ||
            die "strip CR from $_r"
        ;;
    esac
}

# ------------------------------------------------------------------ 构建
log "repo root: $ROOT  tmp: $TMP"
if [ "$CAL_SKIP_BUILD" != '1' ]; then
    log "cross-compiling linux/amd64 fleetlyd+fleetly+probe (version $CAL_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CAL_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$CAL_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w" \
                -o "$TMP/hello" ./deploy/testdata/probeapp
    ) || die 'go build failed'
    CAL_BIN_DIR="$TMP"
else
    CAL_BIN_DIR="${CAL_BIN_DIR:?CAL_SKIP_BUILD=1 requires CAL_BIN_DIR}"
    log "using prebuilt binaries from $CAL_BIN_DIR"
fi
for _f in "$CAL_BIN_DIR/fleetlyd" "$CAL_BIN_DIR/fleetly" "$CAL_BIN_DIR/hello"; do
    [ -f "$_f" ] || die "missing: $_f"
done
for _f in deploy/install.sh deploy/cal-inner.sh; do
    [ -f "$ROOT/$_f" ] || die "missing: $ROOT/$_f"
done

# 宿主侧构建探针镜像（scratch + 静态二进制，零 registry 依赖）并导出——
# dind 内不做 docker build（buildkit 形态差异），统一 docker load（upgrade 套件同款）。
printf 'FROM scratch\nCOPY hello /probe\nENTRYPOINT ["/probe", "serve"]\n' >"$TMP/probeimg.Dockerfile"
mkdir -p "$TMP/probeimg"
cp "$CAL_BIN_DIR/hello" "$TMP/probeimg/hello" || die 'cp hello'
docker build --platform linux/amd64 -t probeapp:1 -f "$TMP/probeimg.Dockerfile" "$TMP/probeimg" ||
    die 'host docker build of probe image failed'
docker save probeapp:1 | gzip >"$TMP/probe.tar.gz" ||
    die 'docker save of probe image failed'
log "probe image tarball: $(ls -la "$TMP/probe.tar.gz" 2>/dev/null | tr -s ' ')"

# ----------------------------------------------------------------- dind
docker rm -f "$DIND_NAME" >/dev/null 2>&1 || true
_MEM_ARGS=''
case "$CAL_MEMORY" in
0 | none | '') log 'dind memory cap: none (CAL_MEMORY=0)' ;;
*)
    _MEM_ARGS="--memory $CAL_MEMORY --memory-swap $CAL_MEMORY"
    log "dind memory cap: $CAL_MEMORY (env boundary; 容量结论按环境边界标注)"
    ;;
esac
# shellcheck disable=SC2086
docker run -d --name "$DIND_NAME" --privileged $_MEM_ARGS "$DIND_IMAGE" >/dev/null ||
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
docker exec "$DIND_NAME" mkdir -p /tmp/cal/bin || die 'mkdir stage'
stage /tmp/cal/install.sh "$ROOT/deploy/install.sh"
stage /tmp/cal/cal-inner.sh "$ROOT/deploy/cal-inner.sh"
stage /tmp/cal/fleetlyd "$CAL_BIN_DIR/fleetlyd"
stage /tmp/cal/fleetly "$CAL_BIN_DIR/fleetly"
stage /tmp/cal/hello "$CAL_BIN_DIR/hello"
stage /tmp/cal/probe.tar.gz "$TMP/probe.tar.gz"
docker exec "$DIND_NAME" chmod +x /tmp/cal/fleetlyd /tmp/cal/fleetly /tmp/cal/hello ||
    die 'chmod binaries'

# ----------------------------------------------------------------- 执行
log 'running in-dind calibration (deploy/cal-inner.sh)'
docker exec "$DIND_NAME" sh /tmp/cal/cal-inner.sh
RC=$?

# ------------------------------------------------------- 产物读回（宿主）
CAL_OUT_DIR="${CAL_OUT_DIR:-$TMP/calibration}"
if [ "$RC" -eq 0 ]; then
    mkdir -p "$CAL_OUT_DIR" || die "mkdir $CAL_OUT_DIR"
    docker exec "$DIND_NAME" cat /tmp/cal-out/summary.md >"$CAL_OUT_DIR/summary.md" ||
        die 'read back summary.md'
    docker exec "$DIND_NAME" cat /tmp/cal-out/cal.json >"$CAL_OUT_DIR/cal.json" ||
        die 'read back cal.json'
    log "artifacts: $CAL_OUT_DIR/summary.md $CAL_OUT_DIR/cal.json"
fi
if [ "$RC" -ne 0 ]; then
    log "suite RED (rc=$RC) — dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
fi
cleanup
if [ "$RC" -eq 0 ]; then
    log 'CALIBRATION GREEN'
else
    log "CALIBRATION RED (rc=$RC)"
fi
exit "$RC"
