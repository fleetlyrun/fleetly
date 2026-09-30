#!/bin/sh
# e2e dind h2c route（N0 修复批 B1）：受管 traefik 挂项目网的端到端回归
# ——quickstart 全链（project → network 实体 → compose 部署（networks 挂
# 网）→ route → succeeded → HTTP 200）+ h2c Route 端到端（messageloop 形态：
# curl --http2-prior-knowledge 全链 h2c，whoami 的 Proto 行是直接证据）。
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

log "cross-compiling fleetlyd + fleetly + h2cclient (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cclient" ./e2e/h2cclient

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

# install.sh 的 setsid env 会继承本 exec 的环境 → Edge 端点直达 fleetlyd。
log "running install.sh inside dind (edge config endpoint wired)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins \
  -e FLEETLY_EDGE_CONFIG_ENDPOINT="http://$DIND_IP:9082/edge/config" \
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

# 受管 traefik 存在 + 挂项目网（B1 直接断言：网络载体里有 edge 任务）。
log "verifying managed traefik joined the project overlay"
NET_CARRIER=$(docker exec "$DIND_CID" docker network ls --format '{{.Name}}' | grep '^fleetly-net-' | head -1)
if [ -z "$NET_CARRIER" ]; then
  echo "no fleetly project network carrier exists" >&2
  exit 1
fi
EDGE_SVC=$(docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep 'fleetly-fleetly-system' | head -1)
if [ -z "$EDGE_SVC" ]; then
  echo "managed edge service not found" >&2
  exit 1
fi
# 网络载体上必须能看到受管 edge 的容器（swarm 网络附着以 container 计）。
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

log "h2c route end to end (messageloop shape)"
cli routes create --project "$PROJECT_ID" --host "h2c.$DIND_IP.sslip.io" \
  --app "$APP_ID" --process web --port 80 --protocol h2c --tls none >/dev/null
# traefik 5s poll 拉配置 + 后端就绪。断言锚：客户端 PROTO 行（协商 h2c）
# 与 whoami 体的 HTTP/2.0 行（后端收到 h2c）。
i=0
h2body=""
while [ "$i" -lt 45 ]; do
  h2body=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "h2c.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$h2body" in
    *PROTO\ HTTP/2.0*|*HTTP/2.0*) break ;;
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
  *HTTP/2.0*) log "backend received HTTP/2.0 (whoami proto line)" ;;
  *) echo "backend did not receive h2c (body: $h2body)" >&2; exit 1 ;;
esac

log "H2C ROUTE E2E PASSED"
