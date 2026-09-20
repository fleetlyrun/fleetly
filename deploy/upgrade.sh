#!/bin/sh
# deploy/upgrade.sh — fleetlyd 平台自升级（T2.23，升级双轨的 fleetlyd 轨）。
#
# 双轨口径（architecture §4.2 横切硬指标，2026-09-17 审核裁决；runbook 见
# docs/runbooks/upgrade.md）：
#   - fleetlyd 轨 = 本脚本：热备快照 + 预下载新二进制 + 失败自动回退；
#     **不停 Docker Engine、应用不停**（Swarm service 与 Traefik 独立于
#     daemon 存活——daemon 停止窗口内应用路由继续服务，已实证）。
#   - Engine/主机轨 = 冷备 + 维护窗口（先备份 + 停应用，有状态应用停机
#     如实告知）——**禁止用本脚本执行**，见 runbook；两类升级不得混淆。
#   - 禁止 --force-recreate 式升级：本脚本只替换 daemon 二进制，对应用
#     服务零操作（无 service update、无容器重建）。
#
# 升级序列（原子化逐条落地）：
#   ① 预下载新二进制并校验（先下后停，最小停机窗）
#   ② 升级前热备快照（kind=pre_upgrade，经 CLI → daemon RPC 同步执行，
#      verify_status 必须 verified——失败即中止，daemon 未被触碰）
#   ③ 记录应用健康基线（apps derived_state + 可选 --probe-url 采样）
#   ④ 停 fleetlyd（应用不停——见上）
#   ⑤ 原子换二进制（同目录 rename；旧件存 fleetlyd.previous；F6/S20——
#      mv 序列包原子性兜底，任一步失败就地归位旧件再 die）
#   ⑥ start + liveness 门
#   ⑦ 升级后验证（版本号 + derived_state 与基线一致 + probe 可达）
#   ⑧ 任一步失败 → 自动回退 fleetlyd.previous（F6/S20：回退段 stop 失败
#      即 die，不再 warn continue——防 systemctl start no-op 误报已回退）
#      → 再验证；仍失败则停在
#     最诚实状态并打诊断（绝不硬编绿色）。F5/S20：拉起旧件前做 schema
#     感知——DB 版本高于旧件支持上限时默认 die 并给三步恢复指引；
#     --auto-restore 从本运行的 verified pre_upgrade 快照自动恢复状态库
#     后再拉起旧件。
#
# 获取形态与完整性（与 install.sh 同基线，delivery-pipeline §2.4 + S14/H15
# 双轨验签）：
#   --version vX.Y.Z / 缺省 stable-latest：GitHub releases 下载，checksums
#     sha256 必验；签名双轨必验其一（cosign bundle 轨优先，cosign 缺席走
#     openssl 内嵌公钥轨），验证失败即中止、双轨全部不可验即拒绝（降级即
#     死）；release 未附任何签名产物（历史版本/nightly）→ 仅 --allow-nightly
#     显式接受并警告降级。
#   --bin-dir <dir>：离线/开发形态（本地直取，自备完整性）。
#
# 兼容性约束：POSIX sh（busybox ash / dash / bash 均可跑），无 bashism；
# systemd 与 --no-systemd（pid 文件形态）双生命周期。

set -eu

# ------------------------------------------------------------------ 常量
FLEETLY_BIN_DIR='/opt/fleetly/bin'
FLEETLY_ETC_DIR='/opt/fleetly/etc'
FLEETLY_DATA_DIR='/var/lib/fleetly'
FLEETLY_LOG_FILE='/var/log/fleetlyd.log'
FLEETLY_PID_FILE='/var/run/fleetlyd.pid'
FLEETLY_GITHUB_REPO='fleetlyrun/fleetly'
LIVENESS_TIMEOUT=90
PROBE_SAMPLES=3
PROBE_INTERVAL=2
STOP_GRACE=35

# ------------------------------------------- release 签名公钥（openssl 轨）
# S14/H15 双轨验签的兜底轨：cosign 缺席（干净 VPS 常态）时，用本内嵌公钥经
# openssl 验 checksums.txt 的 detached 签名。算法裁决：RSA-2048 + SHA-256
# （openssl dgst 轨——dgst 不支持 Ed 系签名、pkeyutl 参数面随版本分裂，
# dgst-RSA 自 1.0.x 起全版本一致，兼容性优先；完整裁决理由与轮换流程见
# deploy/install.sh 同名公钥块注释）。
# fingerprint-sha256: 3977fefb284f721350003ab6289be6930b48c25f79d8b423113c04ff05a6beec
# ⚠ 本块必须与 install.sh 的 FLEETLY_RELEASE_PUBKEY 逐字一致（test-install.sh
# A11 断言）；当前内嵌测试密钥，发布前替换口径见 deploy/README.md。
# 结构注意：起始/结束引号各独占一行——公钥块保持干净 PEM 行（抽取比对
# 口径与 install.sh 注释同：锚定赋值行、剥前缀，替换密钥时保持该形状）。
FLEETLY_RELEASE_PUBKEY='-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA3YszuC4EPy3gVdsQTXt+
v3RMFs7WFH/8IUBk9DAesYcr1srGWL5IcWSmpcO+L9RE0aKoyIxNrR62LYVaJC6H
CcS1D6Qo6ro/kkkFkkc+rRmY6GyVJg++n7af/qlT3Knq+VhdA+UOTNgzjTgohTr9
tSFKBKT7bEmQ2JKXHWwN968Xk4EnSdNSAxQnJFUlAkKsUvNT94DpT4T+vwwW1lhj
efQIhcO0LzphkV6TWqFgmggIZz3Nq9xejOIBRnKcUEh0iBSR3HGe6DIzX7X8KNIt
rP8jwEqxJ/oRIryjr8sVr82I9noGFb4XMLug/mGLok+3eFw6MhgGyEt7xMiwKZif
qQIDAQAB
-----END PUBLIC KEY-----
'

log()  { printf '[upgrade] %s\n' "$*"; }
warn() { printf '[upgrade] WARNING: %s\n' "$*"; }
die()  { printf '[upgrade] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'USAGE'
usage: upgrade.sh [options]

options:
  --version vX.Y.Z          upgrade to this release from GitHub releases
  --bin-dir <dir>           offline/dev form: use fleetlyd+fleetly already in <dir>
  --api-addr <host:port>    fleetlyd gRPC address for CLI calls (default 127.0.0.1:8421)
  --http-addr <host:port>   local HTTP face for liveness/version checks (default 127.0.0.1:8420)
  --token <tok>             API token for the pre-upgrade backup (env: FLEETLY_TOKEN)
  --probe-url <url>         sample this URL (expect HTTP 200) before/after/during verification
  --probe-host <hostname>   Host header for probe sampling (Traefik Host routing)
  --cli <path>              fleetly CLI path (default: auto-detect /usr/local/bin/fleetly)
  --pid-file <path>         pid file for --no-systemd lifecycle (default /var/run/fleetlyd.pid)
  --no-systemd              force manual lifecycle even if systemctl exists
  --allow-nightly           accept an UNSIGNED release target (nightly; warns loudly)
  --skip-signature-verify   skip signature verification (cosign or openssl
                            track; debug only)
  --skip-backup             DANGEROUS: skip the pre-upgrade hot snapshot (breaks the
                            atomic-upgrade guarantee; red-warned, never silent)
  --auto-restore            rollback with a DB newer than the rollback binary
                            supports: automatically restore the verified
                            pre_upgrade snapshot of this run before restarting
                            the old binary (default: die with manual guidance)
  -h, --help                this help

dual-track contract: this script upgrades fleetlyd ONLY (hot snapshot +
auto-rollback; Docker Engine and running apps are NOT touched). Host/Engine
upgrades are a separate cold-backup + maintenance-window procedure — see
docs/runbooks/upgrade.md. Never mix the two tracks.
USAGE
}

have() { command -v "$1" >/dev/null 2>&1; }

http_fetch() { # <url> <outfile>
    _url=$1
    _out=$2
    if have curl; then
        curl -fsSL --retry 3 -o "$_out" "$_url"
    elif have wget; then
        wget -q -T 30 -O "$_out" "$_url"
    else
        die 'no http client found (need curl or wget)'
    fi
}

# fetch_release_sig <url> <outfile> — 签名产物下载（404 感知，S14/H15；
# 语义与 install.sh 同名函数一致）：0 = 成功；1 = 404（release 未附该签名
# 产物）；其他失败 = die（checksums.txt 刚取成功，签名产物取不到更可能是
# 劫持/投毒面，fail closed）。curl 形态经 -w '%{http_code}' 精确分类；
# wget-only 形态下载失败后用 -S 状态行探测（busybox wget 状态行走 stderr）。
fetch_release_sig() {
    _url=$1
    _out=$2
    if have curl; then
        _code=$(curl -sSL --retry 2 -o "$_out" -w '%{http_code}' "$_url" 2>/dev/null) || _code='000'
        case "$_code" in
        2??) return 0 ;;
        404) rm -f "$_out"; return 1 ;;
        *) die "signature artifact download failed (HTTP $_code): $_url" ;;
        esac
    fi
    if wget -q -T 30 -O "$_out" "$_url" 2>/dev/null; then
        return 0
    fi
    rm -f "$_out"
    _st=$(wget -q -S -T 30 -O /dev/null "$_url" 2>&1 |
        sed -n '1s/^[[:space:]]*HTTP\/[0-9.]*[[:space:]]*\([0-9][0-9][0-9]\).*/\1/p')
    [ "$_st" = '404' ] && return 1
    die "signature artifact download failed (HTTP ${_st:-unknown}): $_url"
}

# verify_release_sig_pem <sigfile> <datafile> — openssl 轨验签（RSA-2048 /
# SHA-256，dgst 轨）。POSIX sh 无进程替换——内嵌公钥先落临时文件再验
# （变量自带收尾换行，用 %s 不再补行）；验签输出（Verified OK）静音，
# 成败经退出码表达。
verify_release_sig_pem() {
    _pub="$TMPD/release-pubkey.pem"
    printf '%s' "$FLEETLY_RELEASE_PUBKEY" > "$_pub"
    openssl dgst -sha256 -verify "$_pub" -signature "$1" "$2" >/dev/null 2>&1
}

# http_code <url> [host-header] — 输出 HTTP 状态码（curl/wget 双实现；
# 失败输出 000。host-header 用于 Host 路由打 Traefik 入口的采样。注意
# wget -S 的状态行走 stderr——必须 2>&1 收编，且行首有空格需先剥离）。
http_code() {
    _url=$1
    _host=${2:-}
    if have curl; then
        if [ -n "$_host" ]; then
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: $_host" "$_url" 2>/dev/null || printf '000'
        else
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$_url" 2>/dev/null || printf '000'
        fi
    elif have wget; then
        if [ -n "$_host" ]; then
            wget -q -S -T 5 -O /dev/null --header "Host: $_host" "$_url" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        else
            wget -q -S -T 5 -O /dev/null "$_url" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        fi
    else
        printf '000'
    fi
}

# json_str <json-text> <key> — 提取顶层字符串字段（compact/indent 双兼容；
# 只用于 ping 与 backups JSON 的定点字段，非通用解析器）。
json_str() {
    _json=$1
    _key=$2
    printf '%s' "$_json" |
        grep -o "\"$_key\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/.*:[[:space:]]*"//; s/"$//'
}

# ---------------------------------------------------------------- 参数解析
TARGET_VERSION=''
BIN_DIR_SRC=''
API_ADDR='127.0.0.1:8421'
HTTP_ADDR='127.0.0.1:8420'
API_TOKEN="${FLEETLY_TOKEN:-}"
PROBE_URL=''
PROBE_HOST=''
CLI_PATH=''
PID_FILE="$FLEETLY_PID_FILE"
FORCE_NO_SYSTEMD=0
ALLOW_NIGHTLY=0
SKIP_SIG=0
SKIP_BACKUP=0
AUTO_RESTORE=0

while [ $# -gt 0 ]; do
    case "$1" in
    --version)
        [ $# -ge 2 ] || die '--version requires a value'
        TARGET_VERSION=$2
        shift 2
        ;;
    --bin-dir)
        [ $# -ge 2 ] || die '--bin-dir requires a value'
        BIN_DIR_SRC=$2
        shift 2
        ;;
    --api-addr)
        [ $# -ge 2 ] || die '--api-addr requires a value'
        API_ADDR=$2
        shift 2
        ;;
    --http-addr)
        [ $# -ge 2 ] || die '--http-addr requires a value'
        HTTP_ADDR=$2
        shift 2
        ;;
    --token)
        [ $# -ge 2 ] || die '--token requires a value'
        API_TOKEN=$2
        shift 2
        ;;
    --probe-url)
        [ $# -ge 2 ] || die '--probe-url requires a value'
        PROBE_URL=$2
        shift 2
        ;;
    --probe-host)
        [ $# -ge 2 ] || die '--probe-host requires a value'
        PROBE_HOST=$2
        shift 2
        ;;
    --cli)
        [ $# -ge 2 ] || die '--cli requires a value'
        CLI_PATH=$2
        shift 2
        ;;
    --pid-file)
        [ $# -ge 2 ] || die '--pid-file requires a value'
        PID_FILE=$2
        shift 2
        ;;
    --no-systemd)
        FORCE_NO_SYSTEMD=1
        shift
        ;;
    --allow-nightly)
        ALLOW_NIGHTLY=1
        shift
        ;;
    --skip-signature-verify)
        SKIP_SIG=1
        shift
        ;;
    --skip-backup)
        SKIP_BACKUP=1
        shift
        ;;
    --auto-restore)
        AUTO_RESTORE=1
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        printf '[upgrade] ERROR: unknown option: %s\n' "$1" >&2
        usage >&2
        exit 2
        ;;
    esac
done

[ -n "$TARGET_VERSION" ] && [ -n "$BIN_DIR_SRC" ] &&
    die '--version and --bin-dir are mutually exclusive'

# ------------------------------------------------------------- 前置门禁
[ "$(uname -s)" = 'Linux' ] || die "unsupported platform: $(uname -s) (this upgrader targets Linux only)"
[ "$(id -u)" = '0' ] || die 'must run as root — re-run via: curl -fsSL <upgrade-url> | sudo sh -'
[ -x "$FLEETLY_BIN_DIR/fleetlyd" ] || die "fleetlyd not installed at $FLEETLY_BIN_DIR/fleetlyd — run install.sh first"
have sha256sum || die 'sha256sum not found — install coreutils/busybox and re-run'
have tar || die 'tar not found — install tar and re-run'
[ -f "$FLEETLY_ETC_DIR/config.yaml" ] || warn "config not found at $FLEETLY_ETC_DIR/config.yaml — daemon start will use it anyway if you changed paths"

HTTP_PORT=${HTTP_ADDR##*:}
PING_URL="http://127.0.0.1:$HTTP_PORT/v1/system/ping"
LIVE_URL="http://127.0.0.1:$HTTP_PORT/healthz/liveness"

# 生命周期形态：systemd（unit 在 + systemd 运行 + 未显式 --no-systemd）
# 或 pid 文件形态。
SYSTEMD_MODE=0
if [ "$FORCE_NO_SYSTEMD" -eq 0 ] && have systemctl && [ -d /run/systemd/system ] &&
    [ -f /etc/systemd/system/fleetlyd.service ]; then
    SYSTEMD_MODE=1
fi

daemon_pid_alive() {
    [ -f "$PID_FILE" ] || return 1
    _pid=$(cat "$PID_FILE" 2>/dev/null || true)
    [ -n "$_pid" ] || return 1
    kill -0 "$_pid" 2>/dev/null
}

# daemon 在跑（门禁 + 回退点判定共用）：liveness 200 或 systemd active 或
# pid 存活，三者其一即可（liveness 失败但进程在 = 诊断信息，不阻断）。
daemon_running() {
    [ "$(http_code "$LIVE_URL")" = '200' ] && return 0
    if [ "$SYSTEMD_MODE" -eq 1 ]; then
        systemctl is-active --quiet fleetlyd.service && return 0
    fi
    daemon_pid_alive
}

CURRENT_VERSION='unknown'
CURRENT_VERSION=$(json_str "$(http_fetch "$PING_URL" - 2>/dev/null || printf '{}')" version) ||
    CURRENT_VERSION='unknown'
[ -n "$CURRENT_VERSION" ] || CURRENT_VERSION='unknown'
daemon_running || die "fleetlyd is not running (liveness $LIVE_URL not 200) — start it first; the upgrader refuses to boot-strap from a stopped daemon"
log "gate: daemon running (current version: $CURRENT_VERSION, lifecycle: $( [ "$SYSTEMD_MODE" -eq 1 ] && printf systemd || printf manual))"

# ------------------------------------------------------------- 生命周期原语
stop_daemon() {
    if [ "$SYSTEMD_MODE" -eq 1 ]; then
        systemctl stop fleetlyd.service
    else
        if daemon_pid_alive; then
            _pid=$(cat "$PID_FILE")
            kill -TERM "$_pid" 2>/dev/null || true
            _i=0
            while kill -0 "$_pid" 2>/dev/null; do
                _i=$((_i + 1))
                [ "$_i" -gt "$STOP_GRACE" ] && break
                sleep 1
            done
            if kill -0 "$_pid" 2>/dev/null; then
                warn "daemon still alive after ${STOP_GRACE}s SIGTERM grace — sending SIGKILL (downtime will be honest in probe counters)"
                kill -KILL "$_pid" 2>/dev/null || true
            fi
        fi
    fi
    # 停机判定双确认：liveness 关停（先发生）+ 进程/服务真正退出（后发生）
    # ——听众关闭先于进程退出的窗口里换件会让新 daemon 撞 bind 竞争即死。
    _i=0
    while [ "$(http_code "$LIVE_URL")" = '200' ]; do
        _i=$((_i + 1))
        [ "$_i" -gt "$STOP_GRACE" ] && return 1
        sleep 1
    done
    if [ "$SYSTEMD_MODE" -eq 0 ]; then
        _i=0
        while daemon_pid_alive; do
            _i=$((_i + 1))
            [ "$_i" -gt "$STOP_GRACE" ] && return 1
            sleep 1
        done
    fi
    return 0
}

start_daemon() {
    if [ "$SYSTEMD_MODE" -eq 1 ]; then
        systemctl start fleetlyd.service
        return 0
    fi
    (
        cd "$FLEETLY_DATA_DIR"
        nohup "$FLEETLY_BIN_DIR/fleetlyd" -c "$FLEETLY_ETC_DIR/config.yaml" >>"$FLEETLY_LOG_FILE" 2>&1 &
        echo $! >"$PID_FILE"
    )
    chmod 0644 "$PID_FILE" 2>/dev/null || true
    sleep 1
    daemon_pid_alive || {
        warn 'daemon process exited immediately after start — log tail follows'
        tail -n 20 "$FLEETLY_LOG_FILE" 2>/dev/null || true
        return 1
    }
    return 0
}

# wait_liveness <budget-seconds> — liveness 门（0 = 就绪）。manual 生命周期
# 下进程已死即快速失败（不耗满预算——坏二进制启动即退的回退路径要快）。
wait_liveness() {
    _budget=$1
    _deadline=$(( $(date +%s) + _budget ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(http_code "$LIVE_URL")" = '200' ] && return 0
        if [ "$SYSTEMD_MODE" -eq 0 ]; then
            daemon_pid_alive || return 1
        fi
        sleep 2
    done
    return 1
}

# ------------------------------------------------------------- CLI 定位
if [ -z "$CLI_PATH" ]; then
    if [ -x /usr/local/bin/fleetly ]; then
        CLI_PATH=/usr/local/bin/fleetly
    elif [ -x "$FLEETLY_BIN_DIR/fleetly" ]; then
        CLI_PATH="$FLEETLY_BIN_DIR/fleetly"
    fi
fi
have_cli() { [ -n "$CLI_PATH" ] && [ -x "$CLI_PATH" ]; }

# ------------------------------------------------------------- 获取形态
TMPD=$(mktemp -d /tmp/fleetly-upgrade.XXXXXX) || die 'mktemp failed'
cleanup() { rm -rf "$TMPD"; }
trap cleanup EXIT
trap 'exit 130' INT TERM

TARGET_LABEL=''
if [ -n "$BIN_DIR_SRC" ]; then
    # ---- 离线/开发形态：本地二进制直取（版本串经 CLI 自报）。
    [ -d "$BIN_DIR_SRC" ] || die "bin-dir not found: $BIN_DIR_SRC"
    [ -f "$BIN_DIR_SRC/fleetlyd" ] || die "$BIN_DIR_SRC/fleetlyd missing"
    [ -f "$BIN_DIR_SRC/fleetly" ] || die "$BIN_DIR_SRC/fleetly missing"
    cp "$BIN_DIR_SRC/fleetlyd" "$TMPD/fleetlyd"
    cp "$BIN_DIR_SRC/fleetly" "$TMPD/fleetly"
    chmod 0755 "$TMPD/fleetlyd" "$TMPD/fleetly"
    TARGET_LABEL="bin-dir:$BIN_DIR_SRC"
    TARGET_VERSION=$("$TMPD/fleetly" version 2>/dev/null | awk '{print $2}' | head -n 1)
    [ -n "$TARGET_VERSION" ] || TARGET_VERSION='unknown'
    log "source: $TARGET_LABEL (target version: $TARGET_VERSION)"
else
    # ---- GitHub releases 下载（先下后停：此刻 daemon 仍在服务）。
    if [ -n "$TARGET_VERSION" ]; then
        case "$TARGET_VERSION" in
        v*) TAG=$TARGET_VERSION ;;
        *) TAG="v$TARGET_VERSION" ;;
        esac
        TARGET_LABEL="$TAG (--version)"
    else
        log 'resolving latest stable release from GitHub...'
        LATEST=$(http_fetch "https://api.github.com/repos/$FLEETLY_GITHUB_REPO/releases/latest" - 2>/dev/null || true)
        TAG=$(printf '%s\n' "$LATEST" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
        [ -n "$TAG" ] || die 'cannot resolve latest release (GitHub API unreachable?) — retry, or pin with --version / use --bin-dir'
        TARGET_LABEL="$TAG (latest stable)"
    fi
    case "$(uname -m)" in
    x86_64) TARGET_ARCH='amd64' ;;
    aarch64 | arm64) TARGET_ARCH='arm64' ;;
    *) die "unsupported architecture: $(uname -m)" ;;
    esac
    ASSET="fleetly_${TAG}_linux_${TARGET_ARCH}.tar.gz"
    BASE_URL="https://github.com/$FLEETLY_GITHUB_REPO/releases/download/$TAG"
    log "downloading $ASSET (pre-download: daemon keeps serving during acquisition)"
    http_fetch "$BASE_URL/$ASSET" "$TMPD/$ASSET" ||
        die "download failed: $BASE_URL/$ASSET"
    http_fetch "$BASE_URL/checksums.txt" "$TMPD/checksums.txt" ||
        die "download failed: $BASE_URL/checksums.txt"
    # 双轨签名产物各自尝试下载（S14）：.sig = cosign bundle；.sig.pem =
    # openssl 轨 detached 签名（裸 DER，.pem 为产物链约定名）。非 404 失败
    # → die；双 404 = 未附签名产物 → 下方验证段的 --allow-nightly 门。
    HAVE_BUNDLE_SIG=0
    HAVE_PEM_SIG=0
    if fetch_release_sig "$BASE_URL/checksums.txt.sig" "$TMPD/checksums.txt.sig"; then
        HAVE_BUNDLE_SIG=1
    fi
    if fetch_release_sig "$BASE_URL/checksums.txt.sig.pem" "$TMPD/checksums.txt.sig.pem"; then
        HAVE_PEM_SIG=1
    fi

    # 校验和必验。
    EXPECTED=$(grep -E "^[0-9a-f]{64}[[:space:]]+\*?$ASSET\$" "$TMPD/checksums.txt" | awk '{print $1}' | head -n 1)
    [ -n "$EXPECTED" ] || die "$ASSET not listed in checksums.txt — refusing to install unverified artifacts"
    ACTUAL=$(sha256sum "$TMPD/$ASSET" | awk '{print $1}')
    [ "$ACTUAL" = "$EXPECTED" ] ||
        die "checksum mismatch for $ASSET: expected $EXPECTED, got $ACTUAL"
    log "checksum: sha256 OK ($ACTUAL)"

    # 签名链（S14/H15 双轨，降级即死）：cosign 在且 bundle 在 → cosign 轨
    # （失败即中止）；否则 .sig.pem 在 → openssl 轨（内嵌公钥，失败即中止）；
    # 两者都不可验 → die；双产物均未附（历史版本/nightly）→ --allow-nightly
    # 显式接受（警告降级口径同安装器）。
    if [ "$SKIP_SIG" -eq 1 ]; then
        warn 'signature verification SKIPPED by --skip-signature-verify (debug only — do not use in production)'
    elif [ "$HAVE_BUNDLE_SIG" -eq 0 ] && [ "$HAVE_PEM_SIG" -eq 0 ]; then
        if [ "$ALLOW_NIGHTLY" -eq 1 ]; then
            warn 'no signature artifacts (pre-S14 historical or nightly release) — proceeding because --allow-nightly was given (NIGHTLY/degraded channel; not for production)'
        else
            die 'stable channel requires a signed release (neither checksums.txt.sig nor checksums.txt.sig.pem present) — pass --allow-nightly to accept an unsigned target explicitly'
        fi
    elif have cosign && [ "$HAVE_BUNDLE_SIG" -eq 1 ]; then
        cosign verify-blob \
            --bundle "$TMPD/checksums.txt.sig" \
            --certificate-identity-regexp "^https://github.com/$FLEETLY_GITHUB_REPO/" \
            --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
            "$TMPD/checksums.txt" ||
            die 'cosign signature verification FAILED — refusing to upgrade'
        log 'signature: cosign verify-blob OK'
    elif [ "$HAVE_PEM_SIG" -eq 1 ]; then
        have openssl ||
            die 'openssl not found — cannot verify checksums.txt.sig.pem (install openssl — or cosign — and re-run)'
        verify_release_sig_pem "$TMPD/checksums.txt.sig.pem" "$TMPD/checksums.txt" ||
            die 'openssl signature verification FAILED — refusing to upgrade (release key mismatch usually means a rotated key: update this installer from https://github.com/fleetlyrun/fleetly)'
        log 'signature: openssl dgst verify OK (embedded release key)'
    else
        die 'release carries no signature verifiable on this host (cosign absent and no checksums.txt.sig.pem) — install cosign for the bundle track, or update this installer; --allow-nightly accepts the risk explicitly'
    fi

    tar -xzf "$TMPD/$ASSET" -C "$TMPD" || die "extract $ASSET failed"
    [ -x "$TMPD/fleetlyd" ] || die "$ASSET does not contain an executable fleetlyd at archive root"
    [ -x "$TMPD/fleetly" ] || die "$ASSET does not contain an executable fleetly at archive root"
    TARGET_VERSION=$("$TMPD/fleetly" version 2>/dev/null | awk '{print $2}' | head -n 1)
    [ -n "$TARGET_VERSION" ] || TARGET_VERSION='unknown'
    log "target version: $TARGET_VERSION"
fi

# ------------------------------------------------- ② 升级前热备快照
if [ "$SKIP_BACKUP" -eq 1 ]; then
    warn 'PRE-UPGRADE BACKUP SKIPPED by --skip-backup — this breaks the atomic-upgrade guarantee (red alarm; rollback binary kept, but state is unprotected)'
else
    have_cli || die 'fleetly CLI not found — cannot trigger the pre-upgrade backup (install.sh symlinks it; or pass --cli)'
    [ -n "$API_TOKEN" ] || die 'no API token for the pre-upgrade backup — pass --token or set FLEETLY_TOKEN (bootstrap token: <data-root>/bootstrap-token, default /var/lib/fleetly/bootstrap-token; never logged), or pass --skip-backup to explicitly upgrade unprotected'
    log 'step 2/8: pre-upgrade hot snapshot (kind=pre_upgrade)...'
    BACKUP_JSON=$(FLEETLY_ADDR="$API_ADDR" FLEETLY_TOKEN="$API_TOKEN" "$CLI_PATH" backups create --kind pre_upgrade --json 2>"$TMPD/backup.err") ||
        die "pre-upgrade backup RPC failed: $(cat "$TMPD/backup.err")"
    BACKUP_STATUS=$(json_str "$BACKUP_JSON" verify_status)
    BACKUP_ID=$(json_str "$BACKUP_JSON" id)
    if [ "$BACKUP_STATUS" != 'verified' ]; then
        printf '%s\n' "$BACKUP_JSON" >&2
        die "pre-upgrade backup is NOT verified (status: $BACKUP_STATUS) — upgrade aborted BEFORE touching the daemon; inspect ledger: fleetly backups list"
    fi
    log "pre-upgrade backup verified: $BACKUP_ID"
fi

# ------------------------------------------------- ③ 应用健康基线
BASELINE_FILE="$TMPD/derived.baseline"
: >"$BASELINE_FILE"
if have_cli && [ -n "$API_TOKEN" ]; then
    FLEETLY_ADDR="$API_ADDR" FLEETLY_TOKEN="$API_TOKEN" "$CLI_PATH" apps list --json 2>/dev/null |
        grep -o '"derived_state": *"[a-z]*"' |
        sed 's/.*: *"//; s/"$//' |
        sort >"$BASELINE_FILE" || true
fi
log "step 3/8: app health baseline: $(wc -l <"$BASELINE_FILE" | tr -d ' ') state(s): $(paste -sd, "$BASELINE_FILE" 2>/dev/null || true)"

# probe 基线采样（可选）。
probe_sample() { # <label>
    [ -n "$PROBE_URL" ] || return 0
    _label=$1
    _ok=0
    _i=0
    while [ "$_i" -lt "$PROBE_SAMPLES" ]; do
        [ "$(http_code "$PROBE_URL" "$PROBE_HOST")" = '200' ] && _ok=$((_ok + 1))
        _i=$((_i + 1))
        [ "$_i" -lt "$PROBE_SAMPLES" ] && sleep "$PROBE_INTERVAL"
    done
    log "probe $_label: $_ok/$PROBE_SAMPLES ok ($PROBE_URL host=${PROBE_HOST:-default})"
    [ "$_ok" -eq "$PROBE_SAMPLES" ] || return 1
    return 0
}
probe_sample baseline || die "probe-url baseline not 200 — fix reachability before upgrading"

# ------------------------------------------------- ④ 停 daemon（应用不停）
log 'step 4/8: stopping fleetlyd (apps keep serving: Swarm services + Traefik are independent of the daemon)'
stop_daemon || die 'daemon did not stop within grace period — aborting without changes'

# ------------------------------------------------- ⑤ 原子换二进制
# swap_binaries — F6（S20）：换件 mv 序列的原子性兜底。四步 mv 任一步失败
# 立即执行回退段（已摘下的旧件 mv 归位 + 清 incoming 残留）再 die——此前
# set -e 在序列中段失败会裸退，留下「fleetlyd 缺位 + .previous 已摘链」的
# 半换件现场（systemd start 对缺位二进制报错，但 .previous 摘链后连应急
# 回退路径都要人肉拼）。实现形态：&& 链 + rc 检查（POSIX sh 无 ERR trap
# ——dash 不支持，见文件头兼容性约束），语义等价 per-step ERR 处理。
swap_binaries() {
    (
        mv "$FLEETLY_BIN_DIR/fleetlyd" "$FLEETLY_BIN_DIR/fleetlyd.previous" &&
            mv "$FLEETLY_BIN_DIR/fleetly" "$FLEETLY_BIN_DIR/fleetly.previous" &&
            mv "$FLEETLY_BIN_DIR/fleetlyd.incoming" "$FLEETLY_BIN_DIR/fleetlyd" &&
            mv "$FLEETLY_BIN_DIR/fleetly.incoming" "$FLEETLY_BIN_DIR/fleetly"
    ) || {
        warn 'binary swap FAILED mid-sequence — restoring previous binaries in place'
        if [ -f "$FLEETLY_BIN_DIR/fleetlyd.previous" ]; then
            mv -f "$FLEETLY_BIN_DIR/fleetlyd.previous" "$FLEETLY_BIN_DIR/fleetlyd" || true
        fi
        if [ -f "$FLEETLY_BIN_DIR/fleetly.previous" ]; then
            mv -f "$FLEETLY_BIN_DIR/fleetly.previous" "$FLEETLY_BIN_DIR/fleetly" || true
        fi
        rm -f "$FLEETLY_BIN_DIR/fleetlyd.incoming" "$FLEETLY_BIN_DIR/fleetly.incoming"
        die "binary swap aborted (previous binaries restored in place; daemon is STOPPED — restart it manually after investigating)"
    }
}

log 'step 5/8: atomic binary swap (old binary kept as fleetlyd.previous)'
cp "$TMPD/fleetlyd" "$FLEETLY_BIN_DIR/fleetlyd.incoming" ||
    die 'stage fleetlyd.incoming failed'
cp "$TMPD/fleetly" "$FLEETLY_BIN_DIR/fleetly.incoming" ||
    die 'stage fleetly.incoming failed'
chmod 0755 "$FLEETLY_BIN_DIR/fleetlyd.incoming" "$FLEETLY_BIN_DIR/fleetly.incoming"
# rm 先于 rename 的旧件摘链：运行中的旧 daemon 已被停止，ETXTBSY 不再
# 存在，但残存的 .previous 覆盖前先清掉，保证 rename 链确定。
rm -f "$FLEETLY_BIN_DIR/fleetlyd.previous" "$FLEETLY_BIN_DIR/fleetly.previous"
swap_binaries
if [ -d /usr/local/bin ]; then
    ln -sfn "$FLEETLY_BIN_DIR/fleetly" /usr/local/bin/fleetly
    ln -sfn "$FLEETLY_BIN_DIR/fleetlyd" /usr/local/bin/fleetlyd
fi

# ------------------------------------------------- ⑥/⑦ start + 验证
verify_running() {
    wait_liveness "$LIVENESS_TIMEOUT" || return 1
    _ping=$(http_fetch "$PING_URL" - 2>/dev/null || printf '{}')
    _v=$(json_str "$_ping" version)
    [ -n "$_v" ] || return 1
    # 诊断行一律走 stderr——stdout 只承载版本串（本函数在命令替换中被捕获）。
    printf '[upgrade] daemon up: version %s\n' "$_v" >&2
    printf '%s' "$_v"
    return 0
}

UPGRADE_OK=0
FINAL_VERSION=''
log 'step 6/8: starting upgraded daemon + liveness gate'
if start_daemon && NEW_VERSION=$(verify_running); then
    log 'step 7/8: post-upgrade verification'
    VERIFY_FAIL=''
    if [ "$TARGET_VERSION" != 'unknown' ] && [ "$NEW_VERSION" != "$TARGET_VERSION" ]; then
        warn "version mismatch: want $TARGET_VERSION, ping reports $NEW_VERSION"
        VERIFY_FAIL="version mismatch ($NEW_VERSION != $TARGET_VERSION)"
    fi
    if have_cli && [ -n "$API_TOKEN" ]; then
        FLEETLY_ADDR="$API_ADDR" FLEETLY_TOKEN="$API_TOKEN" "$CLI_PATH" apps list --json 2>/dev/null |
            grep -o '"derived_state": *"[a-z]*"' |
            sed 's/.*: *"//; s/"$//' |
            sort >"$TMPD/derived.after" || true
        if ! diff "$BASELINE_FILE" "$TMPD/derived.after" >/dev/null 2>&1; then
            warn "app derived_state drifted across upgrade:"
            warn "  baseline: $(paste -sd, "$BASELINE_FILE" 2>/dev/null || true)"
            warn "  after   : $(paste -sd, "$TMPD/derived.after" 2>/dev/null || true)"
            VERIFY_FAIL="$VERIFY_FAIL derived_state drift"
        else
            log "apps derived_state unchanged ($(wc -l <"$BASELINE_FILE" | tr -d ' ') state(s))"
        fi
    fi
    if probe_sample after; then
        : # probe 验证通过
    else
        VERIFY_FAIL="$VERIFY_FAIL probe-url not 200 after upgrade"
    fi
    if [ -z "$VERIFY_FAIL" ]; then
        UPGRADE_OK=1
        FINAL_VERSION=$NEW_VERSION
    fi
fi

# dump_diagnostics — 失败路径的诚实诊断转储（systemd journal 或手动日志 +
# 内核事件；绝不硬编绿色）。
dump_diagnostics() {
    if [ "$SYSTEMD_MODE" -eq 1 ]; then
        systemctl status fleetlyd.service --no-pager -l 2>&1 || true
        journalctl -u fleetlyd.service -n 50 --no-pager 2>&1 || true
    else
        tail -n 50 "$FLEETLY_LOG_FILE" 2>/dev/null || true
    fi
    dmesg 2>/dev/null | tail -n 6 || true
}

# schema_probe <fleetlyd-binary> — F5（S20 升级回退 schema 感知）：经只读
# 子命令 `schema-version`（不开 Store、不迁移、不触发启动守卫）提取 DB
# 当前 schema 版本与该二进制的支持上限（及 db/备份根/密钥路径的装配
# 派生）。成功 0 并置 SV_*；失败 1（旧于本子命令的二进制等场景由调用方
# 决定降级语义——守护仍是 daemon 启动时的高版本守卫）。
schema_probe() {
    SV_DB=''
    SV_MAX=''
    SV_DBPATH=''
    SV_BACKUPROOT=''
    SV_KEYPATH=''
    _sv_out=$("$1" schema-version -c "$FLEETLY_ETC_DIR/config.yaml" 2>&1) || return 1
    SV_DB=$(printf '%s\n' "$_sv_out" | sed -n 's/^db=//p' | head -n 1)
    SV_MAX=$(printf '%s\n' "$_sv_out" | sed -n 's/^max=//p' | head -n 1)
    SV_DBPATH=$(printf '%s\n' "$_sv_out" | sed -n 's/^db_path=//p' | head -n 1)
    SV_BACKUPROOT=$(printf '%s\n' "$_sv_out" | sed -n 's/^backup_root=//p' | head -n 1)
    SV_KEYPATH=$(printf '%s\n' "$_sv_out" | sed -n 's/^key_path=//p' | head -n 1)
    [ -n "$SV_DB" ] && [ -n "$SV_MAX" ] && [ -n "$SV_DBPATH" ] && [ -n "$SV_BACKUPROOT" ]
}

# auto_restore_snapshot — F5 --auto-restore 的最小自动链（daemon 已停）：
# 校验本运行 ② 步 pre_upgrade 快照（verify_status=verified + sha256 + 主
# 密钥指纹）→ 现 DB 旁存（.pre-schema-restore.<ts>，只增不销毁）→ 快照
# 入库（清残留 -wal/-shm，防旧 WAL 回放污染恢复件）→ 复核 schema ≤ 旧件
# 上限。任一校验失败即 die（半恢复不静默：现场 + runbook 交给人处理）。
auto_restore_snapshot() {
    [ -n "$BACKUP_ID" ] ||
        die '--auto-restore: no pre-upgrade snapshot from this run (--skip-backup was used?) — restore manually per docs/runbooks/backup-restore.md'
    _snap_dir="$SV_BACKUPROOT/$BACKUP_ID"
    _snap_db="$_snap_dir/fleetly.db"
    [ -f "$_snap_db" ] && [ -f "$_snap_dir/manifest.json" ] ||
        die "--auto-restore: snapshot files missing under $_snap_dir"
    _man=$(cat "$_snap_dir/manifest.json")
    [ "$(json_str "$_man" verify_status)" = 'verified' ] ||
        die "--auto-restore: snapshot $BACKUP_ID is NOT verified — refusing (restore manually per docs/runbooks/backup-restore.md)"
    _man_sha=$(json_str "$_man" sha256)
    _snap_sha=$(sha256sum "$_snap_db" | awk '{print $1}')
    [ "$_snap_sha" = "$_man_sha" ] ||
        die "--auto-restore: snapshot sha256 mismatch (manifest=$_man_sha actual=$_snap_sha) — corrupted snapshot, refusing"
    _man_fp=$(json_str "$_man" key_fingerprint)
    _key_sha=$(sha256sum "$SV_KEYPATH" 2>/dev/null | awk '{print $1}')
    [ -n "$_man_fp" ] && [ "$_key_sha" = "$_man_fp" ] ||
        die "--auto-restore: master key fingerprint mismatch (manifest=$_man_fp actual=$_key_sha) — snapshot belongs to a different key; see docs/runbooks/backup-restore.md"
    _stamp=$(date +%Y%m%d%H%M%S)
    mv "$SV_DBPATH" "$SV_DBPATH.pre-schema-restore.$_stamp" ||
        die "--auto-restore: could not move the current db aside ($SV_DBPATH)"
    rm -f "$SV_DBPATH-wal" "$SV_DBPATH-shm" "$SV_DBPATH-journal"
    cp "$_snap_db" "$SV_DBPATH" ||
        die "--auto-restore: copy snapshot into place failed — current db kept at $SV_DBPATH.pre-schema-restore.$_stamp"
    chmod 0600 "$SV_DBPATH" 2>/dev/null || true
    schema_probe "$FLEETLY_BIN_DIR/fleetlyd" ||
        die '--auto-restore: post-restore schema re-probe failed'
    [ "$SV_DB" -le "$SV_MAX" ] ||
        die "--auto-restore: restored snapshot still reports schema $SV_DB > binary max $SV_MAX (unexpected — snapshot does not predate this rollback target)"
    log "auto-restore: state db restored from verified snapshot $BACKUP_ID (schema $SV_DB <= binary max $SV_MAX)"
    log "auto-restore: superseded db kept at $SV_DBPATH.pre-schema-restore.$_stamp (safe to remove after confirming the rollback)"
    AUTO_RESTORED=1
}

# ------------------------------------------------- ⑧ 失败自动回退
if [ "$UPGRADE_OK" -ne 1 ]; then
    warn "upgrade verification FAILED (${VERIFY_FAIL:-daemon did not become healthy}) — auto-rollback to fleetlyd.previous; new-daemon output follows"
    dump_diagnostics
    # F6（S20）：回退段 stop 失败即 die，不再 warn continue——旧进程未确认
    # 退出就换件/重启，systemctl start 对在跑服务会 no-op 成功，把「未真正
    # 回退」误报成 ROLLED BACK。
    stop_daemon ||
        die 'rollback: daemon did not stop within grace — aborting rollback (binaries NOT yet restored; investigate, then re-run)'
    rm -f "$FLEETLY_BIN_DIR/fleetlyd" "$FLEETLY_BIN_DIR/fleetly"
    mv "$FLEETLY_BIN_DIR/fleetlyd.previous" "$FLEETLY_BIN_DIR/fleetlyd" ||
        die "rollback: restore fleetlyd.previous failed — manual: mv $FLEETLY_BIN_DIR/fleetlyd.previous $FLEETLY_BIN_DIR/fleetlyd"
    mv "$FLEETLY_BIN_DIR/fleetly.previous" "$FLEETLY_BIN_DIR/fleetly" ||
        die "rollback: restore fleetly.previous failed — manual: mv $FLEETLY_BIN_DIR/fleetly.previous $FLEETLY_BIN_DIR/fleetly"

    # F5（S20）：回退 schema 感知——拉起旧件前比对 DB schema 版本与旧二进制
    # 支持上限。迁移只加法：新 daemon 若已应用迁移，旧 daemon 启动必被高
    # 版本守卫拒绝（整改③）——这里在 start 之前把错配显式化：默认 die 并给
    # 三步人肉指引；--auto-restore 走「停服（已停）+ 校验快照 + 恢复状态库
    # + 拉起旧件」的最小自动链。探测失败（旧件早于 schema-version 子命令）
    # 不阻断：warn 放行，最终防线仍是 daemon 启动守卫。
    AUTO_RESTORED=0
    if schema_probe "$FLEETLY_BIN_DIR/fleetlyd"; then
        log "rollback schema check: db schema $SV_DB vs rollback-binary max $SV_MAX"
        if [ "$SV_DB" -gt "$SV_MAX" ]; then
            if [ "$AUTO_RESTORE" -ne 1 ]; then
                _snap_ref="$SV_BACKUPROOT/${BACKUP_ID:-<snapshot-id>}"
                warn "database schema ($SV_DB) is NEWER than the rollback binary supports ($SV_MAX) — the old daemon will REFUSE to start (high-version guard)."
                warn 'restore the pre-upgrade state snapshot first (manual, 3 steps):'
                warn '  1) keep fleetlyd STOPPED (it is stopped now — do NOT start it)'
                warn "  2) verify + restore the snapshot of this run: check sha256sum $_snap_ref/fleetly.db against manifest.json, then copy it over $SV_DBPATH (removing stale ${SV_DBPATH}-wal/-shm first); runbook: docs/runbooks/backup-restore.md"
                warn '  3) re-run this upgrader (or start fleetlyd) with the old binary'
                die 'schema above rollback target — refusing to start the old daemon (pass --auto-restore to perform step 2 automatically from the verified pre_upgrade snapshot)'
            fi
            log '--auto-restore: restoring the verified pre_upgrade snapshot before restarting the old binary'
            auto_restore_snapshot
        fi
    else
        warn 'rollback schema check: schema-version probe failed (rollback binary predates the subcommand?) — proceeding; the daemon startup guard remains the backstop'
    fi
    if start_daemon && ROLLBACK_VERSION=$(verify_running); then
        warn "ROLLED BACK to $ROLLBACK_VERSION — platform healthy, upgrade NOT applied (honest result: RED, not green)"
        printf '\n'
        printf '==================== fleetly upgrade report ====================\n'
        printf 'result          : ROLLED BACK (upgrade failed; platform healthy)\n'
        printf 'from -> to      : %s -> %s (NOT applied)\n' "$CURRENT_VERSION" "${TARGET_VERSION:-unknown}"
        printf 'running version : %s\n' "$ROLLBACK_VERSION"
        printf 'reason          : %s\n' "${VERIFY_FAIL:-liveness gate failed}"
        printf 'pre-upgrade bkp : %s\n' "${BACKUP_ID:-skipped}"
        if [ "$AUTO_RESTORED" -eq 1 ]; then
            printf 'auto-restore    : yes — state db restored from snapshot %s (schema was above the rollback binary)\n' "$BACKUP_ID"
        fi
        printf '================================================================\n'
        exit 1
    fi
    printf '\n'
    printf '==================== fleetly upgrade report ====================\n'
    printf 'result          : DEGRADED — rollback ALSO failed (honest state, no green)\n'
    printf 'from            : %s\n' "$CURRENT_VERSION"
    printf 'target          : %s (binary at %s/fleetlyd.previous)\n' "${TARGET_VERSION:-unknown}" "$FLEETLY_BIN_DIR"
    printf 'diagnose        : systemctl status fleetlyd; journalctl -u fleetlyd -n 100\n'
    printf '                  (manual form) tail -n 100 %s\n' "$FLEETLY_LOG_FILE"
    printf 'recover         : fix the start failure, then: mv %s/fleetlyd.previous %s/fleetlyd && start fleetlyd\n' "$FLEETLY_BIN_DIR" "$FLEETLY_BIN_DIR"
    printf 'pre-upgrade bkp : %s (restore runbook: docs/runbooks/backup-restore.md)\n' "${BACKUP_ID:-skipped}"
    printf '================================================================\n'
    dump_diagnostics
    exit 1
fi

# ---------------------------------------------------------------- 升级报告
printf '\n'
printf '==================== fleetly upgrade report ====================\n'
printf 'result          : UPGRADED\n'
printf 'from -> to      : %s -> %s\n' "$CURRENT_VERSION" "$FINAL_VERSION"
printf 'apps            : derived_state unchanged; engine/apps untouched (no --force-recreate)\n'
printf 'pre-upgrade bkp : %s (verified)\n' "${BACKUP_ID:-skipped (--skip-backup)}"
printf 'rollback binary : %s/fleetlyd.previous (= %s)\n' "$FLEETLY_BIN_DIR" "$CURRENT_VERSION"
printf '================================================================\n'
log 'upgrade done'
