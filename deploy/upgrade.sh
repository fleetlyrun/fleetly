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
#   ⑤ 原子换二进制（同目录 rename；旧件存 fleetlyd.previous）
#   ⑥ start + liveness 门
#   ⑦ 升级后验证（版本号 + derived_state 与基线一致 + probe 可达）
#   ⑧ 任一步失败 → 自动回退 fleetlyd.previous → 再验证；仍失败则停在
#     最诚实状态并打诊断（绝不硬编绿色）。
#
# 获取形态与完整性（与 install.sh 同基线，delivery-pipeline §2.4）：
#   --version vX.Y.Z / 缺省 stable-latest：GitHub releases 下载，checksums
#     sha256 必验；签名 stable 只接受带签名版本（无签名 → 拒绝，除非
#     --allow-nightly 显式接受并警告降级）；cosign 在即验（失败即中止）。
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
  --skip-signature-verify   skip cosign verification (debug only)
  --skip-backup             DANGEROUS: skip the pre-upgrade hot snapshot (breaks the
                            atomic-upgrade guarantee; red-warned, never silent)
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
    HAVE_SIG=1
    http_fetch "$BASE_URL/checksums.txt.sig" "$TMPD/checksums.txt.sig" || HAVE_SIG=0

    # 校验和必验。
    EXPECTED=$(grep -E "^[0-9a-f]{64}[[:space:]]+\*?$ASSET\$" "$TMPD/checksums.txt" | awk '{print $1}' | head -n 1)
    [ -n "$EXPECTED" ] || die "$ASSET not listed in checksums.txt — refusing to install unverified artifacts"
    ACTUAL=$(sha256sum "$TMPD/$ASSET" | awk '{print $1}')
    [ "$ACTUAL" = "$EXPECTED" ] ||
        die "checksum mismatch for $ASSET: expected $EXPECTED, got $ACTUAL"
    log "checksum: sha256 OK ($ACTUAL)"

    # 签名链：stable 只接受带签名版本；cosign 在即验（失败即中止）；
    # 无签名 → --allow-nightly 显式接受（警告降级口径同安装器）。
    if [ "$SKIP_SIG" -eq 1 ]; then
        warn 'signature verification SKIPPED by --skip-signature-verify (debug only — do not use in production)'
    elif [ "$HAVE_SIG" -eq 0 ]; then
        if [ "$ALLOW_NIGHTLY" -eq 1 ]; then
            warn 'no signature file — proceeding because --allow-nightly was given (NIGHTLY/degraded channel; not for production)'
        else
            die 'stable channel requires a signed release (checksums.txt.sig missing) — pass --allow-nightly to accept an unsigned nightly target explicitly'
        fi
    elif have cosign; then
        cosign verify-blob \
            --bundle "$TMPD/checksums.txt.sig" \
            --certificate-identity-regexp "^https://github.com/$FLEETLY_GITHUB_REPO/" \
            --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
            "$TMPD/checksums.txt" ||
            die 'cosign signature verification FAILED — refusing to upgrade'
        log 'signature: cosign verify-blob OK'
    else
        warn 'cosign not found — signature present but NOT verified (degraded; install cosign for full verification)'
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
    [ -n "$API_TOKEN" ] || die 'no API token for the pre-upgrade backup — pass --token or set FLEETLY_TOKEN (bootstrap token: fleetlyd first-start log), or pass --skip-backup to explicitly upgrade unprotected'
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
log 'step 5/8: atomic binary swap (old binary kept as fleetlyd.previous)'
cp "$TMPD/fleetlyd" "$FLEETLY_BIN_DIR/fleetlyd.incoming" ||
    die 'stage fleetlyd.incoming failed'
cp "$TMPD/fleetly" "$FLEETLY_BIN_DIR/fleetly.incoming" ||
    die 'stage fleetly.incoming failed'
chmod 0755 "$FLEETLY_BIN_DIR/fleetlyd.incoming" "$FLEETLY_BIN_DIR/fleetly.incoming"
# rm 先于 rename 的旧件摘链：运行中的旧 daemon 已被停止，ETXTBSY 不再
# 存在，但残存的 .previous 覆盖前先清掉，保证 rename 链确定。
rm -f "$FLEETLY_BIN_DIR/fleetlyd.previous" "$FLEETLY_BIN_DIR/fleetly.previous"
mv "$FLEETLY_BIN_DIR/fleetlyd" "$FLEETLY_BIN_DIR/fleetlyd.previous"
mv "$FLEETLY_BIN_DIR/fleetly" "$FLEETLY_BIN_DIR/fleetly.previous"
mv "$FLEETLY_BIN_DIR/fleetlyd.incoming" "$FLEETLY_BIN_DIR/fleetlyd"
mv "$FLEETLY_BIN_DIR/fleetly.incoming" "$FLEETLY_BIN_DIR/fleetly"
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

# ------------------------------------------------- ⑧ 失败自动回退
if [ "$UPGRADE_OK" -ne 1 ]; then
    warn "upgrade verification FAILED (${VERIFY_FAIL:-daemon did not become healthy}) — auto-rollback to fleetlyd.previous; new-daemon output follows"
    dump_diagnostics
    stop_daemon || warn 'stop during rollback exceeded grace (continuing)'
    rm -f "$FLEETLY_BIN_DIR/fleetlyd" "$FLEETLY_BIN_DIR/fleetly"
    mv "$FLEETLY_BIN_DIR/fleetlyd.previous" "$FLEETLY_BIN_DIR/fleetlyd"
    mv "$FLEETLY_BIN_DIR/fleetly.previous" "$FLEETLY_BIN_DIR/fleetly"
    # schema 提示（整改③）：迁移只加法——若新 daemon 已应用 schema 迁移，
    # 回退后的旧 daemon 拒绝启动（启动守卫显式报错，不静默 no-op）；此时
    # 须按快照恢复状态库后再回退（本脚本不替用户决定数据回滚）。
    log 'rollback note: if the rolled-back daemon refuses to start with a schema-version error, restore the pre-upgrade snapshot first — see docs/runbooks/backup-restore.md (runbook: docs/runbooks/upgrade.md)'
    if start_daemon && ROLLBACK_VERSION=$(verify_running); then
        warn "ROLLED BACK to $ROLLBACK_VERSION — platform healthy, upgrade NOT applied (honest result: RED, not green)"
        printf '\n'
        printf '==================== fleetly upgrade report ====================\n'
        printf 'result          : ROLLED BACK (upgrade failed; platform healthy)\n'
        printf 'from -> to      : %s -> %s (NOT applied)\n' "$CURRENT_VERSION" "${TARGET_VERSION:-unknown}"
        printf 'running version : %s\n' "$ROLLBACK_VERSION"
        printf 'reason          : %s\n' "${VERIFY_FAIL:-liveness gate failed}"
        printf 'pre-upgrade bkp : %s\n' "${BACKUP_ID:-skipped}"
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
