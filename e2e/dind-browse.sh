#!/bin/sh
# F3.6 e2e：数据浏览器全链（ADR-0051）。
#
# 单腿（pgweb/PG 家族——服务端只读执法的最强锚）：
#   1. postgres 库收敛到 running；
#   2. databases browse 受理（pgweb/SESSION 只读档回显）+ 载体在场
#      （fleetly-browse-<sid> service）；
#   3. traefik 真链：entry 烧票 302 + Set-Cookie → 同票二次 401（单用途）
#      → cookie 过 ForwardAuth 取 pgweb 页 200；
#   4. 服务端只读执法：经 pgweb API SHOW default_transaction_read_only = on
#      （postgres 会话级——不是工具摆设）。
#
# 环境事实（F3.3 实录沿用）：dind 有网（digest index 解析一次）；
# browse 三配置走 install.sh env（host_suffix=<dind-ip-dashed>.sslip.io、
# gateway_url=http://<dind-ip>:9081、tls 缺省 none）。

set -eu

WORKDIR="$(mktemp -d)"
DIND_CID=""

cleanup() {
  if [ -n "$DIND_CID" ]; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

log "cross-compiling fleetlyd + fleetly + browseprobe (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/browseprobe" ./e2e/browseprobe

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-browse-"$$" \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  echo "dind daemon did not become ready" >&2
  exit 1
fi

DIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
if [ -z "$DIND_IP" ]; then
  echo "could not resolve dind container IP" >&2
  exit 1
fi
DIND_SUFFIX=$(printf '%s' "$DIND_IP" | tr '.' '-')
log "dind ip: $DIND_IP (browse suffix: $DIND_SUFFIX.sslip.io)"

log "preloading traefik + pgweb + postgres into dind"
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save sosedoff/pgweb:0.17.0 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save postgres:17-bookworm | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

log "running install.sh inside dind (proxy config endpoint + browse face wired)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins \
  -e FLEETLY_PROXY_CONFIG_ENDPOINT="http://$DIND_IP:9082/proxy/config" \
  -e FLEETLY_BROWSE_HOST_SUFFIX="$DIND_SUFFIX.sslip.io" \
  -e FLEETLY_BROWSE_GATEWAY_URL="http://$DIND_IP:9081" \
  "$DIND_CID" sh /root/install.sh

log "identity chain via fleetly init"
CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
if [ -z "$CURRENT_TOKEN" ]; then
  echo "bootstrap token file missing or empty" >&2
  exit 1
fi
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR="$DIND_IP:9080" "$DIND_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$NEW_TOKEN" ]; then
  echo "fleetly init did not mint a CLI token" >&2
  exit 1
fi
CURRENT_TOKEN="$NEW_TOKEN"

cli() {
  docker exec -e FLEETLY_ADDR="$DIND_IP:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

cli whoami >/dev/null
log "identity chain green"

# ---- 夹具：postgres 库收敛到 running ----

log "creating project + postgres database"
BROWSE_PROJECT_ID=$(cli --json projects create browse-shop \
  | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$BROWSE_PROJECT_ID" ]; then
  echo "could not create the browse project" >&2
  exit 1
fi
cli databases create --project "$BROWSE_PROJECT_ID" --engine postgres shop >/dev/null
DB_ID=$(cli --json databases list --project "$BROWSE_PROJECT_ID" \
  | grep -B3 '"shop"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
if [ -z "$DB_ID" ]; then
  echo "could not resolve the database id" >&2
  exit 1
fi

i=0
db_status=""
while [ "$i" -lt 180 ]; do
  db_status=$(cli --json databases get "$DB_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
  [ "$db_status" = "running" ] && break
  i=$((i + 1))
  sleep 2
done
if [ "$db_status" != "running" ]; then
  echo "database did not reach running (last=$db_status)" >&2
  exit 1
fi
log "database running (id=$DB_ID)"

# ---- 受理 + 回显契约 ----

BROWSE_JSON=$(cli --json databases browse "$DB_ID")
case "$BROWSE_JSON" in
  *"pgweb"*) log "browse accepted with browser=pgweb" ;;
  *) echo "browse response missing browser=pgweb: $BROWSE_JSON" >&2; exit 1 ;;
esac
case "$BROWSE_JSON" in
  *"BROWSE_READ_ONLY_ENFORCEMENT_SESSION"*) : ;;
  *) echo "browse response missing session enforcement: $BROWSE_JSON" >&2; exit 1 ;;
esac
case "$BROWSE_JSON" in
  *"read_only"*true*) : ;;
  *) echo "browse response missing read_only=true: $BROWSE_JSON" >&2; exit 1 ;;
esac
ENTRY_URL=$(printf '%s' "$BROWSE_JSON" | sed -n 's/.*"url": *"\([^"]*\)".*/\1/p' | head -1)
TICKET=$(printf '%s' "$BROWSE_JSON" | sed -n 's/.*"ticket": *"\([^"]*\)".*/\1/p' | head -1)
SESSION_ID=$(printf '%s' "$BROWSE_JSON" | sed -n 's/.*"session_id": *"\([^"]*\)".*/\1/p' | head -1)
BROWSE_HOST=$(printf '%s' "$ENTRY_URL" | sed -n 's|http://\([^/]*\)/.*|\1|p')
case "$BROWSE_HOST" in
  browse-*) : ;;
  *) echo "browse url host malformed: $ENTRY_URL" >&2; exit 1 ;;
esac
log "session=$SESSION_ID host=$BROWSE_HOST"

# 载体在场（第五轴 ns 命名：fleetly-browse-<sid>）。
i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" docker service ls --format '{{.Name}}' 2>/dev/null | grep -q "fleetly-browse-$(printf %s "$SESSION_ID" | tr 'A-Z' 'a-z')"; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
if [ "$i" -ge 60 ]; then
  echo "browse carrier service did not appear" >&2
  docker exec "$DIND_CID" docker service ls >&2 || true
  exit 1
fi
log "browse carrier service present (fleetly-browse-$SESSION_ID)"

# ---- traefik 真链：entry 烧票 → cookie → ForwardAuth 门禁下的 pgweb ----
# browseprobe 承载五步判定（entry 烧票 302+cookie / 同票二次 401 / cookie
# 过门禁取工具页 200 / 无 cookie 401 / pgweb 服务端只读 on）——busybox wget
# 吞 Set-Cookie 头且自动跟重定向，cookie 链判定不可靠，判定单源住探针。
# 入口 URL 的 sslip host 在 dind 内公网解析 → 直连 traefik :80 真链。
if docker exec "$DIND_CID" /root/bins/browseprobe -entry "$ENTRY_URL" -marker pgweb -readonly-query; then
  log "browse chain green (probe: entry/reuse/gate/tool/readonly)"
else
  echo "browseprobe failed" >&2
  echo "--- proxy config snapshot (browse face) ---" >&2
  docker exec "$DIND_CID" wget -q -O - "http://$DIND_IP:9082/proxy/config" 2>/dev/null | grep -B1 -A5 -i "browse" >&2 || true
  echo "--- fleetlyd log tail ---" >&2
  docker exec "$DIND_CID" sh -c 'tail -30 /var/log/fleetlyd.log' >&2 || true
  exit 1
fi

log "BROWSE E2E PASSED"
