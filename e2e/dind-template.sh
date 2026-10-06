#!/bin/sh
# e2e dind template（F3.3，ADR-0050）：模板库全链两腿。
#
# 腿 1（内嵌目录冷启动）：templates list（source=builtin）→ instantiate
# nginx（sslip 缺省 host 由 CLI 拼装）→ deployment succeeded → Route HTTP
# 200 → 幂等重跑（app/route reused 报告面）。
#
# 腿 2（目录热更新）：dind 内 busybox httpd 承载 catalog.json（清单手工
# 铸：body 逐字节转义 + sha256 digest）→ fleetlyd 以
# FLEETLY_SERVER_TEMPLATES_CATALOG_URL 重启 → templates refresh（
# previous→new digest + source=refreshed）→ instantiate 刷新目录里的
# whoami 模板 → succeeded → Route 200。fail-closed 反锚：digest 篡改的
# 清单整次拒绝、快照不变。
#
# 前置：本机 docker 可用且持有 traefik:v3.5、traefik/whoami:v1.10 与
# nginx:1.27。
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
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-tpl-"$$" \
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
log "dind ip: $DIND_IP"

log "preloading traefik + whoami + nginx into dind"
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save traefik/whoami:v1.10 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save nginx:1.27 | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

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
  # FLEETLY_ADDR 用 dind IP：instantiate 的 sslip 缺省 host 由此拼出。
  docker exec -e FLEETLY_ADDR="$DIND_IP:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

cli whoami >/dev/null
log "identity chain green"

# ---- 腿 1：内嵌目录冷启动（无目录源配置——内嵌即全部） ----

case "$(cli --json templates list)" in
  *"source"*"builtin"*) log "templates list serves the builtin catalog" ;;
  *) echo "templates list did not report source=builtin" >&2; exit 1 ;;
esac
case "$(cli --json templates list)" in
  *nginx*) log "builtin catalog carries nginx" ;;
  *) echo "builtin catalog missing nginx" >&2; exit 1 ;;
esac
if cli templates refresh >/dev/null 2>&1; then
  echo "templates refresh must fail without a configured catalog url" >&2
  exit 1
fi
log "refresh without a catalog url is precisely rejected"

log "leg 1: instantiate nginx from the builtin catalog (cli default sslip host)"
cli templates instantiate --project tpl --app site nginx >/dev/null
# JSON 条目里 id 先于 name（protojson 字段序）——id 提取用 -B3 逆找
#（dind-h2c-route.sh 的 apps 同款）。
TPL_PROJECT_ID=$(cli --json projects list | grep -B3 '"tpl"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
SITE_APP_ID=$(cli --json apps list --project "$TPL_PROJECT_ID" | grep -B3 '"site"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
if [ -z "$SITE_APP_ID" ]; then
  echo "could not resolve the instantiated app id" >&2
  exit 1
fi
log "project=$TPL_PROJECT_ID app=$SITE_APP_ID"

wait_state() {
  want="$1"; app="$2"; i=0; state=""
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$app" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$app" >&2 || true
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
wait_state succeeded "$SITE_APP_ID"
log "templated deployment succeeded"

i=0
body=""
while [ "$i" -lt 45 ]; do
  body=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "site.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$body" in
    *Welcome\ to\ nginx*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$body" in
  *Welcome\ to\ nginx*) log "templated route answers over HTTP (leg 1 green)" ;;
  *) echo "templated route did not answer (last body: $body)" >&2; exit 1 ;;
esac

log "leg 1: idempotent re-run reports reused resources"
RERUN=$(cli --json templates instantiate --project tpl --app site --no-wait nginx)
case "$RERUN" in
  *"reused"*true*) log "re-run reused existing resources" ;;
  *) echo "re-run did not report reused resources: $RERUN" >&2; exit 1 ;;
esac

# ---- 腿 2：目录热更新（busybox httpd 目录源） ----

log "leg 2: building a local catalog server inside dind"
docker exec "$DIND_CID" sh -c 'mkdir -p /root/catsrc && cat > /root/catsrc/whoami.yaml <<"YAML"
x-fleetly-template:
  name: whoami
  version: 1.0.0
  description: whoami served from a refreshed catalog snapshot.
  variables:
    - name: host
      type: domain
      description: route host serving whoami
      required: true
  routes:
    - var: host
      process: web
      port: 80
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80"]
YAML'
docker exec "$DIND_CID" sh -c '
  cd /root/catsrc
  digest="sha256:$(sha256sum whoami.yaml | cut -d" " -f1)"
  body=$(sed -e "s/\\\\/\\\\\\\\/g" -e "s/\"/\\\\\"/g" whoami.yaml | awk "{printf \"%s\\\\n\", \$0}")
  printf "{\"version\":1,\"templates\":[{\"name\":\"whoami\",\"version\":\"1.0.0\",\"digest\":\"%s\",\"body\":\"%s\"}]}" "$digest" "$body" > catalog.json
  mkdir -p /root/catsrv && cp catalog.json /root/catsrv/catalog.json
  # 篡改清单（改 body 内容不改 digest——"name: whoami" 只在转义正文里
  # 出现，信封键带 JSON 引号不撞）作 fail-closed 反锚素材，精确锚 digest
  # 核对路径。
  sed "s/name: whoami/name: whoam2/" catalog.json > /root/catsrv-tampered.json 2>/dev/null || true
'
# 目录服务器：dind 内跑一只 nginx 容器（已预载；busybox 无 httpd applet——
# 实证）serve catalog.json，fleetlyd 经 127.0.0.1:8099 拉取。
docker exec "$DIND_CID" docker run -d --name catsrv -p 8099:80 \
  -v /root/catsrv:/usr/share/nginx/html:ro nginx:1.27 >/dev/null
i=0
while [ "$i" -lt 15 ]; do
  if docker exec "$DIND_CID" sh -c 'busybox wget -q -O /dev/null http://127.0.0.1:8099/catalog.json' 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 15 ]; then
  echo "local catalog server did not come up" >&2
  exit 1
fi
log "local catalog server serving catalog.json"

log "restarting fleetlyd with FLEETLY_SERVER_TEMPLATES_CATALOG_URL"
# pkill 按进程名精确匹配（-f 会匹配承载 sh 的命令行自杀——测试坑实录，
# dind-upgrade.sh 同款 -TERM 形态）；排水窗等旧进程真退（1s 假睡会让新
# daemon 绑不上端口、旧 daemon 继续应答——无 env 的旧配置面）。
docker exec "$DIND_CID" pkill -TERM fleetlyd >/dev/null 2>&1 || true
i=0
while [ "$i" -lt 60 ]; do
  if ! docker exec "$DIND_CID" pgrep fleetlyd >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  echo "old fleetlyd did not exit within the drain grace" >&2
  exit 1
fi
docker exec -e FLEETLY_PROXY_CONFIG_ENDPOINT="http://$DIND_IP:9082/proxy/config" \
  -e FLEETLY_SERVER_TEMPLATES_CATALOG_URL="http://127.0.0.1:8099" \
  "$DIND_CID" sh -c \
  'setsid env $(grep -v "^$" /etc/fleetlyd.env | tr "\n" " ") FLEETLY_SERVER_TEMPLATES_CATALOG_URL="http://127.0.0.1:8099" /usr/local/bin/fleetlyd >>/var/log/fleetlyd.log 2>&1 < /dev/null &'
i=0
while [ "$i" -lt 60 ]; do
  if docker exec -e FLEETLY_ADDR="127.0.0.1:9080" "$DIND_CID" fleetly status >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  echo "fleetlyd did not come back with the catalog url" >&2
  docker exec "$DIND_CID" sh -c 'tail -20 /var/log/fleetlyd.log >&2' || true
  exit 1
fi
log "fleetlyd healthy again (catalog url wired)"

REFRESH=$(cli --json templates refresh)
case "$REFRESH" in
  *"template_count"*1*) log "catalog refreshed to a single-entry snapshot" ;;
  *) echo "refresh did not report 1 template: $REFRESH" >&2; exit 1 ;;
esac
case "$(cli --json templates list)" in
  *"source"*"refreshed"*) log "templates list now serves the refreshed snapshot" ;;
  *) echo "templates list did not report source=refreshed" >&2; exit 1 ;;
esac

log "leg 2: instantiate whoami from the refreshed snapshot"
cli templates instantiate --project tpl --app probe whoami >/dev/null
PROBE_APP_ID=$(cli --json apps list --project "$TPL_PROJECT_ID" | grep -B3 '"probe"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
if [ -z "$PROBE_APP_ID" ]; then
  echo "could not resolve the whoami app id" >&2
  exit 1
fi
wait_state succeeded "$PROBE_APP_ID"
log "refreshed-catalog deployment succeeded"

i=0
body=""
while [ "$i" -lt 45 ]; do
  body=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "probe.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$body" in
    *Hostname*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$body" in
  *Hostname*) log "refreshed-catalog route answers over HTTP (leg 2 green)" ;;
  *) echo "refreshed-catalog route did not answer (last body: $body)" >&2; exit 1 ;;
esac

# fail-closed 反锚：digest 不符的清单（信封 version 被改、body 与 digest
# 失配）整次拒绝；快照不变（source 仍 refreshed、whoami 仍可实例化语义
# 由上面的 succeeded 锚承载——此处钉"坏目录不换好目录"）。
log "leg 2: tampered manifest is rejected whole, snapshot unchanged"
docker exec "$DIND_CID" sh -c 'cp /root/catsrv-tampered.json /root/catsrv/catalog.json'
if REFRESH_BAD=$(cli --json templates refresh 2>&1); then
  echo "refresh with a tampered manifest must fail (got: $REFRESH_BAD)" >&2
  exit 1
fi
case "$REFRESH_BAD" in
  *digest\ mismatch*) log "tampered refresh rejected with the digest reason" ;;
  *) echo "tampered refresh rejection carried no digest reason: $REFRESH_BAD" >&2; exit 1 ;;
esac
case "$(cli --json templates list)" in
  *"source"*"refreshed"*) log "served snapshot survived the rejected refresh" ;;
  *) echo "served snapshot changed after a rejected refresh" >&2; exit 1 ;;
esac

log "TEMPLATE E2E PASSED"
