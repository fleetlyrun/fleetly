#!/bin/sh
# fleetly 一行安装（F0.1）：curl -fsSL https://fleetly.dev/install.sh | sh
#
# 流程：OS/arch 检测 → 二进制获取（GitHub Releases 或 FLEETLY_BIN_DIR 本地
# 目录——dev/e2e 模式）→ Docker 检测/安装 → 单节点 swarm init → 数据根
# 初始化 → fleetlyd 起服（systemd 优先；无 systemd 环境退化为 setsid 后台）
# → 健康等待 → 下一步指引（fleetly init 消费 bootstrap token）。
#
# 发布通道说明：默认从 GitHub Releases 拉取 fleetly-<os>-<arch>.tar.gz；
# 首个 tag 发布前该通道 404（诚实失败），发布流程随发版批次落地。
# 容器形态运行时（ghcr 镜像）随发布通道同批接入——本脚本是宿主二进制形态。
#
# 环境变量：
#   FLEETLY_VERSION   发布 tag（默认 latest）
#   FLEETLY_BIN_DIR   本地二进制目录（含 fleetlyd 与 fleetly；跳过下载）
#   FLEETLY_DATA_ROOT 数据根（默认 /var/lib/fleetly）
#   FLEETLY_ADVERTISE_ADDR  swarm advertise 地址（默认自动探测默认路由 IP）
set -eu

log() { printf '==> %s\n' "$1"; }
die() { printf 'install: %s\n' "$1" >&2; exit 1; }

# ---- 1. OS/arch 检测 ----
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
  Linux) OS="linux" ;;
  *) die "unsupported OS '$OS' — this installer covers Linux (docker run the container form elsewhere)" ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture '$ARCH'" ;;
esac
log "detected $OS/$ARCH"

[ "$(id -u)" = "0" ] || die "must run as root (curl | sudo sh)"

# ---- 2. 二进制获取（Releases 或本地目录） ----
BIN_DIR="/usr/local/bin"
if [ -n "${FLEETLY_BIN_DIR:-}" ]; then
  log "installing binaries from $FLEETLY_BIN_DIR (local mode)"
  [ -f "$FLEETLY_BIN_DIR/fleetlyd" ] || die "FLEETLY_BIN_DIR/fleetlyd not found"
  [ -f "$FLEETLY_BIN_DIR/fleetly" ] || die "FLEETLY_BIN_DIR/fleetly not found"
  install -m 0755 "$FLEETLY_BIN_DIR/fleetlyd" "$BIN_DIR/fleetlyd"
  install -m 0755 "$FLEETLY_BIN_DIR/fleetly" "$BIN_DIR/fleetly"
else
  VERSION="${FLEETLY_VERSION:-latest}"
  REPO="fleetlyrun/fleetly"
  if [ "$VERSION" = "latest" ]; then
    log "resolving latest release"
    VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | grep -o '"tag_name": *"[^"]*"' | head -1 | grep -o 'v[^"]*')" \
      || die "no releases published yet — build from source (go install) or set FLEETLY_BIN_DIR"
  fi
  URL="https://github.com/$REPO/releases/download/$VERSION/fleetly-$OS-$ARCH.tar.gz"
  log "downloading fleetly $VERSION ($OS/$ARCH)"
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  curl -fsSL "$URL" -o "$TMP/fleetly.tar.gz" || die "download failed: $URL"
  tar -xzf "$TMP/fleetly.tar.gz" -C "$TMP"
  install -m 0755 "$TMP/fleetlyd" "$BIN_DIR/fleetlyd"
  install -m 0755 "$TMP/fleetly" "$BIN_DIR/fleetly"
fi

# ---- 3. Docker 检测/安装 ----
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  log "docker $(docker version --format '{{.Server.Version}}') reachable"
else
  if command -v docker >/dev/null 2>&1; then
    log "docker CLI present but daemon unreachable — starting dockerd via service manager"
    (service docker start || systemctl start docker) >/dev/null 2>&1 || true
    sleep 2
    docker info >/dev/null 2>&1 || die "docker daemon did not come up — start it manually and re-run"
  else
    log "docker not found — installing via get.docker.com"
    curl -fsSL https://get.docker.com | sh || die "docker installation failed"
    service docker start >/dev/null 2>&1 || systemctl start docker >/dev/null 2>&1 || true
  fi
fi

# ---- 4. 单节点 swarm ----
# 注意精确比较：grep -q active 会把 "inactive" 一并匹配（子串坑）。
if [ "$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null)" = "active" ]; then
  log "swarm already active"
else
  ADDR="${FLEETLY_ADVERTISE_ADDR:-}"
  if [ -z "$ADDR" ]; then
    # 默认路由源 IP（云主机公/私网卡均可广播；多网卡环境请显式指定）。
    ADDR="$(ip route get 1.1.1.1 2>/dev/null | grep -o 'src [0-9.]*' | head -1 | grep -o '[0-9.]*')" || true
  fi
  if [ -n "$ADDR" ]; then
    log "swarm init (advertise $ADDR)"
    docker swarm init --advertise-addr "$ADDR" >/dev/null
  else
    docker swarm init >/dev/null || die "swarm init failed — set FLEETLY_ADVERTISE_ADDR explicitly"
  fi
fi

# ---- 5. 数据根 + fleetlyd 起服 ----
DATA_ROOT="${FLEETLY_DATA_ROOT:-/var/lib/fleetly}"
mkdir -p "$DATA_ROOT"

start_fleetlyd_systemd() {
  log "starting fleetlyd via systemd"
  cat > /etc/systemd/system/fleetlyd.service <<UNIT
[Unit]
Description=fleetlyd control plane
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=$BIN_DIR/fleetlyd
Environment=FLEETLY_DATA_ROOT=$DATA_ROOT
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now fleetlyd >/dev/null
}

start_fleetlyd_background() {
  # 无 systemd 环境（容器/最小镜像）：setsid 脱离会话进程组 + 日志落盘
  #（与 e2e dind 同款形态；进程托管随容器形态批次收口）。
  log "no systemd — starting fleetlyd in background (log: /var/log/fleetlyd.log)"
  setsid env FLEETLY_DATA_ROOT="$DATA_ROOT" \
    "$BIN_DIR/fleetlyd" > /var/log/fleetlyd.log 2>&1 < /dev/null &
}

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  start_fleetlyd_systemd
else
  start_fleetlyd_background
fi

# ---- 6. 健康等待 ----
log "waiting for fleetlyd"
i=0
while [ "$i" -lt 60 ]; do
  if "$BIN_DIR/fleetly" status >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  [ -f /var/log/fleetlyd.log ] && tail -20 /var/log/fleetlyd.log >&2 || true
  journalctl -u fleetlyd -n 20 --no-pager >&2 2>/dev/null || true
  die "fleetlyd did not become healthy"
fi
log "fleetlyd healthy"

# ---- 7. 下一步指引 ----
BOOTSTRAP="$DATA_ROOT/bootstrap-token"
cat <<EOF

fleetly is running.

  1. Read the bootstrap token (shown once at first boot):
       cat $BOOTSTRAP
  2. Initialize the admin user (creates a CLI token, revokes bootstrap):
       fleetly init --token <BOOTSTRAP_TOKEN> admin
  3. Sanity-check the install:
       fleetly doctor

Docs: https://docs.fleetly.dev
EOF
