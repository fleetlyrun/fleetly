#!/bin/sh
# e2e/nightly/resource-sample.sh — T2.24 控制面资源基线采样（交付 §2.2
# nightly 第 5 项「资源基线采样（控制面 idle 内存…）；趋势告警，不阻断」；
# 为 T2.25 提供第一份 CI 样本）。宿主编排模式照抄 run-dind-test.sh /
# conformance-builder.sh：交叉编译 → 起 dind → exec+stdin 注入 → dind 内
# 采样 → 结果文件回传（cat <file，容器→宿主方向 exec stdout 可靠）→ 清理。
#
# 采样内容（dind 内）：
#   - fleetlyd 进程 RSS：/proc/<pid>/status VmRSS；稳定窗后连采 N 拍
#     （默认 5 拍 × 5s），报 min/max/avg；
#   - docker stats --no-stream 逐容器一行（Traefik / buildkit 各自单列，
#     依当时实际在跑的平台容器如实列出）。
# 产出：
#   - $RS_SUMMARY_FILE：Markdown 表（workflow 追加进 $GITHUB_STEP_SUMMARY）
#   - $RS_JSON_FILE：JSON（actions/upload-artifact 附加，供 T2.25 趋势对比）
#   两个文件由宿主在 dind 内写路径 exec 后经 docker exec cat 读回。
#
# usage: resource-sample.sh
# env:
#   DIND_IMAGE        dind 镜像（默认 docker:29.8.1-dind）
#   DIND_NAME         容器名（默认 fleetly-resource-sample）
#   RS_SKIP_BUILD     1 = 跳过交叉编译，改用 RS_BIN_DIR 指定的二进制目录
#   RS_BIN_DIR        RS_SKIP_BUILD=1 时的二进制目录（需含 fleetlyd 与 fleetly）
#   RS_VERSION        注入的版本串（默认 v0.1.0-rs）
#   RS_SETTLE_SECONDS daemon 就绪后稳定窗（默认 60）
#   RS_SAMPLES        RSS 采样拍数（默认 5）
#   RS_INTERVAL       采样间隔秒（默认 5）
#   RS_OUT_DIR        宿主侧产物目录（默认 $TMP/resource-sample；CI 传
#                     GITHUB_WORKSPACE 下的路径以便 upload-artifact）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

DIND_IMAGE="${DIND_IMAGE:-docker:29.8.1-dind}"
DIND_NAME="${DIND_NAME:-fleetly-resource-sample}"
RS_SKIP_BUILD="${RS_SKIP_BUILD:-0}"
RS_VERSION="${RS_VERSION:-v0.1.0-rs}"
RS_SETTLE_SECONDS="${RS_SETTLE_SECONDS:-60}"
RS_SAMPLES="${RS_SAMPLES:-5}"
RS_INTERVAL="${RS_INTERVAL:-5}"

log() { printf '[resource-sample %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
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
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/../.." && pwd)
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || die 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || die 'docker not on PATH'
command -v go >/dev/null 2>&1 || RS_SKIP_BUILD=1

stage() { # <remote-path> <local-file> — exec+stdin + sha256 双校验 + CR 剥离
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
if [ "$RS_SKIP_BUILD" != '1' ]; then
    log "cross-compiling linux/amd64 fleetlyd+fleetly (version $RS_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$RS_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$RS_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || die 'go build failed'
    RS_BIN_DIR="$TMP"
else
    RS_BIN_DIR="${RS_BIN_DIR:?RS_SKIP_BUILD=1 requires RS_BIN_DIR}"
    log "using prebuilt binaries from $RS_BIN_DIR"
fi
for _f in "$RS_BIN_DIR/fleetlyd" "$RS_BIN_DIR/fleetly"; do
    [ -f "$_f" ] || die "missing: $_f"
done
[ -f "$ROOT/deploy/install.sh" ] || die 'deploy/install.sh missing'

# ------------------------------------------------------- 内层采样脚本生成
cat >"$TMP/rs-inner.sh" <<'INNER_EOF'
#!/bin/sh
# rs-inner.sh — dind 内：安装 + 起 daemon + 稳定窗 + RSS/docker stats 采样。
# 结果写 /tmp/rs-out/{summary.md,sample.json}，宿主经 docker exec cat 读回。
set -u
STAGE=/tmp/rs
INSTALL_SH="$STAGE/install.sh"
DLOG=/tmp/rs-fleetlyd.log
PID_FILE=/var/run/fleetlyd.pid
OUT=/tmp/rs-out
HTTP=http://127.0.0.1:8420
SETTLE=__RS_SETTLE__
SAMPLES=__RS_SAMPLES__
INTERVAL=__RS_INTERVAL__
VERSION=__RS_VERSION__
DIND_IMAGE_TAG='__RS_DIND_IMAGE__'

log() { printf '[rs %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() {
    log "FATAL: $*"
    exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

http_code() {
    if have curl; then
        curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1" 2>/dev/null || printf '000'
    elif have wget; then
        wget -q -S -T 5 -O /dev/null "$1" 2>&1 |
            awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
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

log "=== resource sample inner (settle=${SETTLE}s samples=$SAMPLES interval=${INTERVAL}s) ==="
[ -s "$INSTALL_SH" ] || die 'install.sh missing'
[ -s "$STAGE/fleetlyd" ] || die 'fleetlyd missing'
[ -s "$STAGE/fleetly" ] || die 'fleetly missing'
chmod +x "$STAGE/fleetlyd" "$STAGE/fleetly"

# 预拉 traefik（fleetly-ingress 在 daemon 启动期拉起——让 idle 基线包含
# 入口面；buildkit 容器由 daemon 后台预热拉起，是否在采样时已在跑如实报告）。
docker pull traefik:v3.5 >/dev/null 2>&1 || true

sh "$INSTALL_SH" --bin-dir "$STAGE" --no-systemd >"$STAGE/install.log" 2>&1 ||
    { cat "$STAGE/install.log"; die 'install failed'; }
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
git:
  enabled: false
logging:
  level: info
EOF
(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90 || { tail -n 40 "$DLOG"; die 'daemon not live'; }
log "daemon live (version string $VERSION)"

# fleetly-ingress（Traefik global service）就绪门——idle 基线要包含入口面
# （docker stats 的 Traefik 单列）；bounded 240s，不就绪如实记 0 行并继续。
_deadline=$(( $(date +%s) + 240 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    docker service ls --format '{{.Name}} {{.Replicas}}' 2>/dev/null |
        grep -q 'fleetly-ingress 1/1' && break
    sleep 3
done
docker service ls --format '{{.Name}} {{.Replicas}}' 2>/dev/null | sed 's/^/ingress: /'

log "settling ${SETTLE}s"
sleep "$SETTLE"

# ---- fleetlyd RSS（/proc/<pid>/status VmRSS；busybox ps 可见 nohup 进程）
PID=$(cat "$PID_FILE" 2>/dev/null)
[ -n "$PID" ] || PID=$(ps 2>/dev/null | awk '$5 ~ /fleetlyd/ {print $1; exit}')
[ -n "$PID" ] || { tail -n 20 "$DLOG"; die 'fleetlyd pid not found'; }
grep -q '^Name:[[:space:]]*fleetlyd' "/proc/$PID/status" 2>/dev/null ||
    log "WARN pid $PID status header mismatch (continuing): $(head -n 1 "/proc/$PID/status" 2>/dev/null)"

RSS_FILE="$OUT/rss.samples"
: >"$RSS_FILE"
_i=0
while [ "$_i" -lt "$SAMPLES" ]; do
    KB=$(awk '/^VmRSS:/ {print $2}' "/proc/$PID/status" 2>/dev/null)
    [ -n "$KB" ] || die "VmRSS unreadable at sample $_i (pid $PID)"
    printf '%s\n' "$KB" >>"$RSS_FILE"
    log "rss sample $((_i + 1))/$SAMPLES: ${KB} kB"
    [ "$((_i + 1))" -lt "$SAMPLES" ] && sleep "$INTERVAL"
    _i=$((_i + 1))
done
RSS_MIN=$(sort -n "$RSS_FILE" | head -n 1)
RSS_MAX=$(sort -n "$RSS_FILE" | tail -n 1)
RSS_AVG=$(awk '{s+=$1} END {printf "%.0f", s/NR}' "$RSS_FILE")
RSS_LAST=$(tail -n 1 "$RSS_FILE")

# ---- docker stats（逐容器单列：Traefik / buildkit 等按实际在跑如实列出；
#      分隔符用 ' | '——Go template 不解释 \t 转义）
docker stats --no-stream --format '{{.Name}} | {{.CPUPerc}} | {{.MemUsage}}' >"$OUT/stats.psv" 2>/dev/null || true

# ---- 汇总产物（Markdown + JSON）
CONT_TABLE=$(awk -F ' \\| ' '{printf "| `%s` | %s | %s |\n", $1, $2, $3}' "$OUT/stats.psv")
[ -s "$OUT/stats.psv" ] || CONT_TABLE='| (no platform containers running at sample time) | | |'

{
    printf '## fleetlyd resource sample (dind idle baseline)\n\n'
    printf '| item | value |\n|---|---|\n'
    printf '| engine image | `%s` |\n' "$DIND_IMAGE_TAG"
    printf '| fleetlyd version | `%s` |\n' "$VERSION"
    printf '| settle window | %ss |\n' "$SETTLE"
    printf '| fleetlyd RSS (min/avg/max, %s samples x %ss) | %s / %s / %s kB |\n' "$SAMPLES" "$INTERVAL" "$RSS_MIN" "$RSS_AVG" "$RSS_MAX"
    printf '| fleetlyd RSS (last) | %s kB |\n' "$RSS_LAST"
    printf '\n| container | CPU | memory |\n|---|---|---|\n'
    printf '%s\n' "$CONT_TABLE"
} >"$OUT/summary.md"

{
    printf '{\n'
    printf '  "sampled_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '  "dind_image": "%s",\n' "$DIND_IMAGE_TAG"
    printf '  "version": "%s",\n' "$VERSION"
    printf '  "settle_seconds": %s,\n' "$SETTLE"
    printf '  "samples": %s,\n' "$SAMPLES"
    printf '  "interval_seconds": %s,\n' "$INTERVAL"
    printf '  "fleetlyd_rss_kb": {"min": %s, "avg": %s, "max": %s, "last": %s},\n' "$RSS_MIN" "$RSS_AVG" "$RSS_MAX" "$RSS_LAST"
    printf '  "containers": [\n'
    _first=1
    while IFS= read -r _row; do
        _n=${_row%% | *}
        _rest=${_row#* | }
        _c=${_rest%% | *}
        _m=${_rest#* | }
        [ -n "${_n:-}" ] || continue
        [ "$_first" -eq 1 ] || printf ',\n'
        printf '    {"name": "%s", "cpu": "%s", "mem": "%s"}' "$_n" "$_c" "$_m"
        _first=0
    done <"$OUT/stats.psv"
    printf '\n  ]\n}\n'
} >"$OUT/sample.json"

log '--- summary ---'
cat "$OUT/summary.md"
log 'RS-INNER-DONE'
INNER_EOF

# 生成期参数注入（__RS_*__ 占位符替换，避免 heredoc 展开）。
sed -i \
    -e "s/__RS_SETTLE__/$RS_SETTLE_SECONDS/" \
    -e "s/__RS_SAMPLES__/$RS_SAMPLES/" \
    -e "s/__RS_INTERVAL__/$RS_INTERVAL/" \
    -e "s/__RS_VERSION__/$RS_VERSION/" \
    -e "s|__RS_DIND_IMAGE__|$DIND_IMAGE|" \
    "$TMP/rs-inner.sh" || die 'parameterize rs-inner.sh'

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
docker exec "$DIND_NAME" mkdir -p /tmp/rs /tmp/rs-out || die 'mkdir stage'
stage /tmp/rs/install.sh "$ROOT/deploy/install.sh"
stage /tmp/rs/rs-inner.sh "$TMP/rs-inner.sh"
stage /tmp/rs/fleetlyd "$RS_BIN_DIR/fleetlyd"
stage /tmp/rs/fleetly "$RS_BIN_DIR/fleetly"
docker exec "$DIND_NAME" chmod +x /tmp/rs/fleetlyd /tmp/rs/fleetly || die 'chmod binaries'

# ----------------------------------------------------------------- 执行
log 'running in-dind sampler (rs-inner.sh)'
docker exec "$DIND_NAME" sh /tmp/rs/rs-inner.sh
RC=$?

# ------------------------------------------------------- 产物读回（宿主）
RS_OUT_DIR="${RS_OUT_DIR:-$TMP/resource-sample}"
mkdir -p "$RS_OUT_DIR" || die "mkdir $RS_OUT_DIR"
if [ "$RC" -eq 0 ]; then
    docker exec "$DIND_NAME" cat /tmp/rs-out/summary.md >"$RS_OUT_DIR/summary.md" ||
        die 'read back summary.md'
    docker exec "$DIND_NAME" cat /tmp/rs-out/sample.json >"$RS_OUT_DIR/sample.json" ||
        die 'read back sample.json'
    log "artifacts: $RS_OUT_DIR/summary.md $RS_OUT_DIR/sample.json"
fi
if [ "$RC" -ne 0 ]; then
    log "sampler RED (rc=$RC) -- dumping dind log tail"
    docker logs "$DIND_NAME" --tail 120 2>&1 | tail -60 || true
fi
cleanup
if [ "$RC" -eq 0 ]; then
    log 'RESOURCE-SAMPLE GREEN'
else
    log "RESOURCE-SAMPLE RED (rc=$RC)"
fi
exit "$RC"
