#!/bin/sh
# deploy/uninstall.sh — fleetly 卸载器（T2.1）。
#
# 行为边界（与 install.sh 对称，验收口径见 task-breakdown T2.1）：
#   - 停服（systemd）并删除 unit、二进制、符号链接、配置、参考 unit；
#   - 应用数据 /var/lib/fleetly（SQLite/主密钥/git bare 仓库/构建缓存/证书/
#     日志）**默认保留并明示**——数据安全默认（误删不可逆）；--purge 才删除；
#   - docker swarm leave --force 提示但**不默认执行**（会把单节点 Swarm 底座
#     拆掉、运行中的应用服务全部失去调度——后果由操作者显式承担）。
# POSIX sh；需要 root。

set -eu

FLEETLY_PREFIX='/opt/fleetly'
FLEETLY_BIN_DIR="$FLEETLY_PREFIX/bin"
FLEETLY_ETC_DIR="$FLEETLY_PREFIX/etc"
FLEETLY_DATA_DIR='/var/lib/fleetly'
FLEETLY_LINK_DIR='/usr/local/bin'
FLEETLY_UNIT_PATH='/etc/systemd/system/fleetlyd.service'
FLEETLY_LOG_FILE='/var/log/fleetlyd.log'

PURGE=0

log()  { printf '[uninstall] %s\n' "$*"; }
warn() { printf '[uninstall] WARNING: %s\n' "$*"; }
die()  { printf '[uninstall] ERROR: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
    cat <<'USAGE'
usage: uninstall.sh [--purge]

  --purge    also remove application data (/var/lib/fleetly) — irreversible
             (SQLite state, master key, git repos, build cache, certs, logs)

Default: keeps /var/lib/fleetly (printed explicitly). The swarm itself is
left running; 'docker swarm leave --force' is suggested but NOT executed.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
    --purge)
        PURGE=1
        shift
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        printf '[uninstall] ERROR: unknown option: %s\n' "$1" >&2
        usage >&2
        exit 2
        ;;
    esac
done

[ "$(id -u)" = '0' ] || die 'must run as root — re-run via: sudo sh uninstall.sh'

# 1) 停服 + 删 unit（systemd 存在才做；--no-systemd 安装无此物，幂等跳过）。
if have systemctl && [ -d /run/systemd/system ]; then
    systemctl stop fleetlyd.service >/dev/null 2>&1 || true
    systemctl disable fleetlyd.service >/dev/null 2>&1 || true
    if [ -f "$FLEETLY_UNIT_PATH" ]; then
        rm -f "$FLEETLY_UNIT_PATH"
        systemctl daemon-reload
        log "unit removed: $FLEETLY_UNIT_PATH"
    fi
else
    warn 'systemd not running — if fleetlyd was started manually, stop it by hand (SIGTERM; it exits 0 gracefully)'
fi

# 2) 二进制 + 符号链接（不留孤儿）。
for _f in \
    "$FLEETLY_BIN_DIR/fleetlyd" \
    "$FLEETLY_BIN_DIR/fleetly" \
    "$FLEETLY_LINK_DIR/fleetly" \
    "$FLEETLY_LINK_DIR/fleetlyd" \
    "$FLEETLY_ETC_DIR/fleetlyd.service"; do
    if [ -e "$_f" ] || [ -L "$_f" ]; then
        rm -f "$_f"
        log "removed: $_f"
    fi
done

# 3) 配置。
if [ -f "$FLEETLY_ETC_DIR/config.yaml" ]; then
    rm -f "$FLEETLY_ETC_DIR/config.yaml"
    log "removed: $FLEETLY_ETC_DIR/config.yaml"
fi

# 4) 空目录收尾（非空说明有未纳管的文件，保留并提示）。
rmdir "$FLEETLY_BIN_DIR" 2>/dev/null || true
rmdir "$FLEETLY_ETC_DIR" 2>/dev/null || true
rmdir "$FLEETLY_PREFIX" 2>/dev/null || true
[ -d "$FLEETLY_PREFIX" ] && warn "$FLEETLY_PREFIX not empty after removal — inspect manually" || true

# 5) 应用数据：默认保留并明示；--purge 才删。
if [ "$PURGE" -eq 1 ]; then
    if [ -d "$FLEETLY_DATA_DIR" ]; then
        rm -rf "$FLEETLY_DATA_DIR"
        log "purged: $FLEETLY_DATA_DIR"
    fi
    if [ -f "$FLEETLY_LOG_FILE" ]; then
        rm -f "$FLEETLY_LOG_FILE"
        log "removed: $FLEETLY_LOG_FILE"
    fi
else
    if [ -d "$FLEETLY_DATA_DIR" ]; then
        log "application data KEPT: $FLEETLY_DATA_DIR (state db, master key, git repos, build cache, certs, logs)"
        log "delete it explicitly with: sh $0 --purge"
    fi
fi

# 6) swarm 底座：提示但不默认执行（明示后果）。
if have docker; then
    if [ "$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || echo unknown)" = 'active' ]; then
        warn 'this host is still a swarm node — leave it with: docker swarm leave --force'
        warn '  (consequence: the single-node swarm substrate is dismantled; running application services lose scheduling and are not cleaned up by uninstall)'
    fi
fi

log 'uninstall done'
