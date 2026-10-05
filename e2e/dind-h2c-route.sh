#!/bin/sh
# e2e dind h2c route（N0 修复批 B1 + N0.1 收口）：受管 traefik 挂项目网的
# 端到端回归——quickstart 全链（project → network 实体 → compose 部署
#（networks 挂网）→ route → succeeded → HTTP 200）+ h2c Route 端到端。
#
# h2c 后端是自备的 h2cserver（stdlib http.Server Protocols，scratch 镜像
# 在 dind 内 build，零外网镜像依赖）：whoami 不说 h2c（N0.1 实证——对先行
# 知识前奏回 HTTP/1.1 字节，traefik 的 h2c:// 后端拨号不回退 → 500），
# "messageloop 形态"需要真 h2c 后端；客户端 h2cclient -h2c 先行知识直连。
#
# ACME/TLS 不在本脚本（dind 无公网 DNS/80-443 不可达；LE 真机随 staging
# 批）。镜像离线预载：宿侧 save → dind load；traefik 以本地 v3.5 别名
# v3.5.4 预载（版本保真由 staging 现役背书，本脚本测的是挂网与路由机制）。
#
# 前置：本机 docker 可用且持有 traefik:v3.5 与 traefik/whoami:v1.10。
set -eu

export MSYS_NO_PATHCONV=1

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

log "cross-compiling fleetlyd + fleetly + h2cclient + h2cserver (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cclient" ./e2e/h2cclient
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cserver" ./e2e/h2cserver

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-h2c-"$$" \
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

# dind 容器 IP：受管 traefik 从 swarm 网络经 gwbridge 回连控制面 :9082 用。
DIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
if [ -z "$DIND_IP" ]; then
  echo "could not resolve dind container IP" >&2
  exit 1
fi
log "dind ip: $DIND_IP"

log "preloading traefik + whoami into dind"
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save traefik/whoami:v1.10 | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

# h2c 回显后端镜像：dind 内 scratch build（零外网依赖；CGO 关静态可直入
# scratch）。
log "building h2c backend image inside dind"
docker exec "$DIND_CID" mkdir -p /root/h2csrc
docker cp "$WORKDIR/bins/h2cserver" "$DIND_CID":/root/h2csrc/h2cserver
docker exec "$DIND_CID" sh -c \
  'printf "FROM scratch\nCOPY h2cserver /h2cserver\nEXPOSE 8080\nENTRYPOINT [\"/h2cserver\"]\n" > /root/h2csrc/Dockerfile && docker build -t h2cbackend:local /root/h2csrc >/dev/null'

# install.sh 的 setsid env 会继承本 exec 的环境 → Proxy 端点直达 fleetlyd。
log "running install.sh inside dind (proxy config endpoint wired)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins \
  -e FLEETLY_PROXY_CONFIG_ENDPOINT="http://$DIND_IP:9082/proxy/config" \
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
  # FLEETLY_ADDR 用 dind IP：quickstart 的 sslip host 由此拼出（DNS 天然
  # 可解析，curl 无需 Host 头）。
  docker exec -e FLEETLY_ADDR="$DIND_IP:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

cli whoami >/dev/null
log "identity chain green"

log "quickstart end to end (managed traefik + project network attach, B1)"
cli quickstart --image traefik/whoami:v1.10 --port 80 >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
log "project=$PROJECT_ID app=$APP_ID"

wait_state() {
  want="$1"; i=0; state=""
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$APP_ID" >&2 || true
        echo "deployment reached $state before $want" >&2
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  echo "timed out waiting for $want (last=$state)" >&2
  return 1
}
wait_state succeeded
log "quickstart deployment succeeded"

# 受管 traefik 存在 + 挂项目网（B1 直接断言：网络载体里有 proxy 任务）。
log "verifying managed traefik joined the project overlay"
NET_CARRIER=$(docker exec "$DIND_CID" docker network ls --format '{{.Name}}' | grep '^fleetly-net-' | head -1)
if [ -z "$NET_CARRIER" ]; then
  echo "no fleetly project network carrier exists" >&2
  exit 1
fi
PROXY_SVC=$(docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep 'fleetly-fleetly-system' | head -1)
if [ -z "$PROXY_SVC" ]; then
  echo "managed proxy service not found" >&2
  exit 1
fi
# 网络载体上必须能看到受管 proxy 的容器（swarm 网络附着以 container 计）。
i=0
attached=""
while [ "$i" -lt 30 ]; do
  attached=$(docker exec "$DIND_CID" docker network inspect "$NET_CARRIER" \
    --format '{{range .Containers}}{{.Name}} {{end}}' | tr ' ' '\n' | grep 'traefik' | head -1)
  [ -n "$attached" ] && break
  i=$((i + 1))
  sleep 2
done
if [ -z "$attached" ]; then
  echo "managed traefik never attached to $NET_CARRIER" >&2
  docker exec "$DIND_CID" docker network inspect "$NET_CARRIER" >&2 || true
  exit 1
fi
log "traefik attached to $NET_CARRIER"

# HTTP 200（quickstart route，tls none；后端=whoami；h2cclient 自带 h2c，
# 普通探活同样用它——busybox wget 无需要，少一个 dind 内依赖）。
i=0
body=""
while [ "$i" -lt 45 ]; do
  body=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "demo.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$body" in
    *Hostname*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$body" in
  *Hostname*) log "quickstart route answers over HTTP (B1 regression green)" ;;
  *) echo "quickstart route did not answer (last body: $body)" >&2; exit 1 ;;
esac

log "h2c route end to end (messageloop shape: h2c backend + prior-knowledge client)"
# 后端换成真 h2c 服务（h2cserver scratch 镜像）——whoami 不说 h2c，
# traefik 的 h2c:// 拨号不回退，用 whoami 只能得 500。
cli apps create --project "$PROJECT_ID" h2cback >/dev/null
H2C_APP_ID=$(cli --json apps list --project "$PROJECT_ID" \
  | grep -B3 '"h2cback"' | grep '"id"' | head -1 \
  | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
if [ -z "$H2C_APP_ID" ]; then
  echo "could not resolve the h2cback app id" >&2
  exit 1
fi
docker exec "$DIND_CID" sh -c \
  'printf "services:\n  web:\n    image: h2cbackend:local\n    ports:\n      - \"8080\"\n    networks:\n      - default\n" > /root/h2c-compose.yml'
cli deploy --app "$H2C_APP_ID" --compose-file /root/h2c-compose.yml >/dev/null
APP_ID_SAVE="$APP_ID"; APP_ID="$H2C_APP_ID"
wait_state succeeded
APP_ID="$APP_ID_SAVE"
log "h2c backend deployment succeeded"

cli routes create --project "$PROJECT_ID" --host "h2c.$DIND_IP.sslip.io" \
  --app "$H2C_APP_ID" --process web --port 8080 --protocol h2c --tls none >/dev/null
# traefik 5s poll 拉配置 + 后端就绪。断言锚：客户端先行知识协商出
# PROTO HTTP/2.0（client↔traefik h2c）与回显体 PROTO-LINE HTTP/2.0
#（traefik↔backend h2c）——两跳各自有据。break 条件必须是后端回显锚：
# traefik 对任意 Host 都以 h2c 应答（PROTO 行首拍即中），只 break 在
# PROTO 行会把传播窗口当失败（N0 假阳性的镜像教训）。
i=0
h2body=""
while [ "$i" -lt 45 ]; do
  h2body=$(docker exec "$DIND_CID" /root/bins/h2cclient -h2c -host "h2c.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$h2body" in
    *PROTO-LINE\ HTTP/2.0*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$h2body" in
  *PROTO\ HTTP/2.0*) log "client negotiated h2c end to end" ;;
  *)
    echo "h2c route did not serve HTTP/2 (last body: $h2body)" >&2
    exit 1
    ;;
esac
case "$h2body" in
  *PROTO-LINE\ HTTP/2.0*) log "backend received HTTP/2.0 (h2cserver proto line)" ;;
  *) echo "backend did not receive h2c (body: $h2body)" >&2; exit 1 ;;
esac

# P9 发布前校验：畸形 Route（反引号注入形态）在受理位拒绝并给精确原因
#（不是等 traefik 静默拒载）；拒绝后存量双路由（HTTP + h2c）继续服务。
log "P9: malformed route rejected at acceptance with a precise reason"
if cli routes create --project "$PROJECT_ID" --host 'evil`host.127.0.0.1.sslip.io' \
  --app "$H2C_APP_ID" --process web --port 8080 >/tmp/p9route.out 2>&1; then
  echo "malformed host must be rejected (got: $(cat /tmp/p9route.out))" >&2
  exit 1
fi
case "$(cat /tmp/p9route.out)" in
  *must\ be\ a\ DNS\ hostname*) log "precise rejection reason surfaced" ;;
  *) echo "rejection carried no precise reason: $(cat /tmp/p9route.out)" >&2; exit 1 ;;
esac
p9http=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "demo.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
case "$p9http" in *Hostname*) log "existing HTTP route still serving after rejection" ;;
  *) echo "existing HTTP route broke after a rejected route attempt (body: $p9http)" >&2; exit 1 ;;
esac
p9h2c=$(docker exec "$DIND_CID" /root/bins/h2cclient -h2c -host "h2c.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
case "$p9h2c" in *PROTO-LINE\ HTTP/2.0*) log "existing h2c route still serving after rejection" ;;
  *) echo "existing h2c route broke after a rejected route attempt (body: $p9h2c)" >&2; exit 1 ;;
esac

log "H2C ROUTE E2E PASSED"
