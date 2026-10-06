#!/bin/sh
# e2e dind blue-green（ADR-0048 P5/P16 实施批验收锚）：双节点真 swarm 上的
# 蓝绿全链 + 进程双别名撞名消歧。腿（与 ADR-0048 验收锚一一对应）：
#   1. 切换步零 5xx（部署全程探针轮询，计数 5xx/传输错误）；
#   2. 旧代载体观察窗内始终存活（载体服务 ID 不变 + task 在跑）；
#   3. 新代 L1 失败：旧代零扰动（服务 ID 不变）、终态 failed、无 Replay
#      （部署行数不增）；
#   4. 观察窗内手动切回（fleetly rollback）：Route 指回旧版、旧代载体未
#      重建（窗口内服务 ID 不变）；
#   5. supersede 在双代窗内抢占：无双代残留（终态单载体）；
#   6. 双节点 Placement 一致（spec_file 声明 worker 钉住——两代 task 同落
#      worker）；
#   7. P16：双 App 同名进程共网——全名 web.alpha/web.beta 各自可达且消歧，
#      裸名 web 双 VIP（DNS RR 面）。
#
# 版本可区分后端 = 自备 h2cserver + BG_VERSION env 回显（同镜像两代以环境
# 变量分代；Route 体 BG-VERSION 行是判代锚）。前置：本机 docker 可用且持有
# traefik:v3.5 与 busybox:1.37（受管面其余镜像按 dind-two-node 先例由
# install 拉）。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
MGR_CID=""
WRK_CID=""

cleanup() {
  [ -n "$MGR_CID" ] && docker rm -f "$MGR_CID" >/dev/null 2>&1 || true
  [ -n "$WRK_CID" ] && docker rm -f "$WRK_CID" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

wait_docker() {
  cid="$1"; i=0
  while [ "$i" -lt 60 ]; do
    docker exec "$cid" docker info >/dev/null 2>&1 && return 0
    i=$((i + 1)); sleep 1
  done
  echo "dind daemon did not become ready ($cid)" >&2
  return 1
}

log "cross-compiling fleetlyd + fleetly + h2cclient + h2cserver (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cclient" ./e2e/h2cclient
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cserver" ./e2e/h2cserver

log "starting manager + worker dind containers"
MGR_CID=$(docker run -d --privileged --name fleetly-e2e-bg-mgr-"$$" --cgroupns=host -e DOCKER_TLS_CERTDIR= docker:29-dind)
WRK_CID=$(docker run -d --privileged --name fleetly-e2e-bg-wrk-"$$" --cgroupns=host -e DOCKER_TLS_CERTDIR= docker:29-dind)
wait_docker "$MGR_CID"
wait_docker "$WRK_CID"
MGR_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$MGR_CID")
WRK_HOST=$(docker inspect -f '{{.Config.Hostname}}' "$WRK_CID")
log "manager ip: $MGR_IP worker host: $WRK_HOST"

log "preloading traefik + busybox; building the versioned backend inside dind"
docker image save traefik:v3.5 | docker exec -i "$MGR_CID" docker load >/dev/null
docker image save busybox:1.37 | docker exec -i "$MGR_CID" docker load >/dev/null
docker exec "$MGR_CID" docker tag traefik:v3.5 traefik:v3.5.4
docker exec "$MGR_CID" mkdir -p /root/h2csrc
docker cp "$WORKDIR/bins/h2cserver" "$MGR_CID":/root/h2csrc/h2cserver
docker exec "$MGR_CID" sh -c \
  'printf "FROM scratch\nCOPY h2cserver /h2cserver\nEXPOSE 8080\nENTRYPOINT [\"/h2cserver\"]\n" > /root/h2csrc/Dockerfile && docker build -t h2cbackend:local /root/h2csrc >/dev/null'
# Placement 腿的代次 task 钉 worker——后端镜像双节点在场。
docker exec "$MGR_CID" docker save h2cbackend:local | docker exec -i "$WRK_CID" docker load >/dev/null

log "installing fleetly on the manager (proxy endpoint wired)"
docker cp "$WORKDIR/bins" "$MGR_CID":/root/bins
docker cp install.sh "$MGR_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins \
  -e FLEETLY_PROXY_CONFIG_ENDPOINT="http://$MGR_IP:9082/proxy/config" \
  "$MGR_CID" sh /root/install.sh

CURRENT_TOKEN=$(docker exec "$MGR_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$MGR_CID" \
  fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$NEW_TOKEN" ] || { echo "fleetly init did not mint a token" >&2; exit 1; }
CURRENT_TOKEN="$NEW_TOKEN"

cli() {
  docker exec -e FLEETLY_ADDR="$MGR_IP:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$MGR_CID" fleetly "$@"
}
cli whoami >/dev/null
log "identity chain green"

log "joining the worker via 'fleetly nodes enroll' material"
JOIN_CMD=$(cli --json nodes enroll | sed -n 's/.*"join_command": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$JOIN_CMD" ] || { echo "nodes enroll produced no join command" >&2; exit 1; }
docker exec "$WRK_CID" $JOIN_CMD
i=0
while [ "$i" -lt 30 ]; do
  n=$(docker exec "$MGR_CID" docker node ls --format '{{.Status}}' | grep -c Ready || true)
  [ "$n" -ge 2 ] && break
  i=$((i + 1)); sleep 2
done
[ "${n:-0}" -ge 2 ] || { echo "manager never saw the worker as Ready" >&2; exit 1; }
# 平台节点 ID 经锚定标记铸造（Watch 锚定扫描 10s 节拍——脚本侧带重试）；
# docker 侧 worker 节点（无 ManagerStatus 行）的 fleetly.node.id 标签即平台 ID。
WORKER_NODE_ID=""
i=0
while [ "$i" -lt 30 ]; do
  WORKER_DOCKER_ID=$(docker exec "$MGR_CID" docker node ls --format '{{.ID}} {{.ManagerStatus}}' | awk 'NF == 1 {print $1; exit}')
  if [ -n "$WORKER_DOCKER_ID" ]; then
    WORKER_NODE_ID=$(docker exec "$MGR_CID" docker node inspect "$WORKER_DOCKER_ID" \
      --format '{{index .Spec.Labels "fleetly.node.id"}}')
    case "$WORKER_NODE_ID" in
      ""|*"<no value>"*) WORKER_NODE_ID="" ;;
    esac
    [ -n "$WORKER_NODE_ID" ] && break
  fi
  i=$((i + 1)); sleep 2
done
[ -n "$WORKER_NODE_ID" ] || { echo "could not resolve the worker platform node id" >&2; docker exec "$MGR_CID" docker node ls >&2 || true; exit 1; }
log "two nodes Ready (worker platform id: $WORKER_NODE_ID)"

log "project + app + first blue-green baseline (v1)"
cli projects create bgproj >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" shop >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
log "project=$PROJECT_ID app=$APP_ID"

# 状态等待：最新部署行到 want 即返；意外终态红。终态注释带观测现场。
wait_state() {
  want="$1"; budget="${2:-240}"; i=0; state=""
  while [ "$i" -lt "$budget" ]; do
    state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    [ "$state" = "$want" ] && return 0
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$APP_ID" >&2 || true
        echo "deployment reached $state before $want" >&2
        return 1
        ;;
    esac
    i=$((i + 1)); sleep 1
  done
  echo "timed out waiting for $want (last=$state)" >&2
  cli --json deployments list --app "$APP_ID" >&2 || true
  return 1
}

# bg_compose VER [EXTRA]：写一份蓝绿 compose 进 dind（BG_VERSION 分代）。
bg_compose() {
  ver="$1"; cmd="${2:-}"
  docker exec -i "$MGR_CID" sh -c "cat > /root/bg-$ver.yaml" <<YAML
services:
  web:
    image: h2cbackend:local
    environment:
      BG_VERSION: $ver
    ports: ["8080"]
    networks: [default]
    deploy:
      strategy: blue-green$cmd
YAML
}

# app_services：App 名下全部载体服务名（ns.app 标记锚定，一进程一代一行）。
# 标签值经 Provider 小写净化（sanitizeNamePart）——ULID 大写形态须归一后
# 匹配（workload.id 标签是原样平台 ID，两通道不同形）。小写在函数内动态
# 计算：P16 腿 save/restore 换用 APP_ID。
app_services() {
  app_lower=$(printf '%s' "$APP_ID" | tr 'A-Z' 'a-z')
  docker exec "$MGR_CID" docker service ls --filter "label=fleetly.ns.app=$app_lower" --format '{{.Name}}' | sort
}
svc_id() {
  docker exec "$MGR_CID" docker service ls -q --filter "label=fleetly.workload.id=$1" | head -1
}
svc_running_tasks() {
  sid=$(svc_id "$1"); [ -z "$sid" ] && return 0
  docker exec "$MGR_CID" docker service ps "$sid" --format '{{.Node}}' --filter desired-state=running 2>/dev/null || true
}

ROUTE_HOST="bg.$MGR_IP.sslip.io"
probe() {
  docker exec "$MGR_CID" /root/bins/h2cclient -host "$ROUTE_HOST" "http://$MGR_IP/" 2>&1 || true
}
serving_version() {
  probe | sed -n 's/^BG-VERSION //p' | head -1
}

bg_compose v1
cli deploy --app "$APP_ID" --compose-file /root/bg-v1.yaml >/dev/null
wait_state succeeded
log "v1 deployment succeeded (first blue-green window degenerates to single generation)"

cli routes create --project "$PROJECT_ID" --host "$ROUTE_HOST" \
  --app "$APP_ID" --process web --port 8080 --protocol http --tls none >/dev/null
i=0; got=""
while [ "$i" -lt 45 ]; do
  got=$(serving_version)
  [ "$got" = "v1" ] && break
  i=$((i + 1)); sleep 2
done
[ "$got" = "v1" ] || { echo "route never served v1 (last: $got)" >&2; exit 1; }
log "route serving v1"

# 首代载体锚：观察窗内存活断言的对象（服务 ID + 运行中 task）。
G1_ID="$APP_ID-web-g1"
G1_SID=$(svc_id "$G1_ID")
[ -n "$G1_SID" ] || { echo "generation-1 carrier not found by label" >&2; app_services >&2; exit 1; }

log "leg 1+2: zero-5xx switch + old generation alive through the window"
docker exec "$MGR_CID" sh -c 'echo 0 > /tmp/bg-fails; echo 0 > /tmp/bg-hits; rm -f /tmp/bg-stop'
docker exec -d "$MGR_CID" sh -c '
  ROUTE_HOST="'"$ROUTE_HOST"'"; MGR_IP="'"$MGR_IP"'"
  while [ ! -e /tmp/bg-stop ]; do
    out=$(/root/bins/h2cclient -host "$ROUTE_HOST" "http://$MGR_IP/" 2>&1)
    case "$out" in
      "STATUS 2"*|"STATUS 3"*|"STATUS 4"*) ;;
      *) echo $(( $(cat /tmp/bg-fails) + 1 )) > /tmp/bg-fails ;;
    esac
    echo $(( $(cat /tmp/bg-hits) + 1 )) > /tmp/bg-hits
    sleep 0.3
  done'

bg_compose v2
cli deploy --app "$APP_ID" --compose-file /root/bg-v2.yaml >/dev/null
wait_state observing
log "switched to observing (traffic moved to the new generation)"

# 双代窗：两代载体并存；旧代服务 ID 不变且有 task 在跑（存活锚）。
G2_ID="$APP_ID-web-g2"
[ -n "$(svc_id "$G2_ID")" ] || { echo "generation-2 carrier missing in the window" >&2; app_services >&2; exit 1; }
[ "$(svc_id "$G1_ID")" = "$G1_SID" ] || { echo "generation-1 carrier identity changed mid-window (zero-touch violated)" >&2; exit 1; }
[ -n "$(svc_running_tasks "$G1_ID")" ] || { echo "generation-1 carrier has no running task in the window" >&2; exit 1; }
log "window: both generations live; generation-1 carrier untouched (id stable, task running)"

i=0; got=""
while [ "$i" -lt 45 ]; do
  got=$(serving_version)
  [ "$got" = "v2" ] && break
  i=$((i + 1)); sleep 2
done
[ "$got" = "v2" ] || { echo "route never switched to v2 (last: $got)" >&2; exit 1; }
log "route serving v2 (switch complete)"

wait_state succeeded
docker exec "$MGR_CID" sh -c 'touch /tmp/bg-stop'
sleep 1
FAILS=$(docker exec "$MGR_CID" cat /tmp/bg-fails)
HITS=$(docker exec "$MGR_CID" cat /tmp/bg-hits)
if [ "${FAILS:-0}" != "0" ]; then
  echo "probe saw $FAILS failed requests out of $HITS during the blue-green deploy (zero-5xx anchor)" >&2
  exit 1
fi
log "zero failed probes across the whole deploy ($HITS requests)"

log "leg 3: new-generation L1 failure leaves the baseline untouched (no Replay)"
G2_SID=$(svc_id "$G2_ID")
[ -n "$G2_SID" ] || { echo "generation-2 carrier not found after collection" >&2; exit 1; }
docker exec -i "$MGR_CID" sh -c "cat > /root/bg-v3.yaml" <<'YAML'
services:
  web:
    image: h2cbackend:local
    environment:
      BG_VERSION: v3
    command: ["/nonexistent-entry"]
    ports: ["8080"]
    networks: [default]
    deploy:
      strategy: blue-green
YAML
cli deploy --app "$APP_ID" --compose-file /root/bg-v3.yaml >/dev/null
wait_state failed 300
log "v3 deployment failed at L1 (terminal, rollback already effectuated)"

[ "$(serving_version)" = "v2" ] || { echo "route drifted off v2 after the L1 failure (baseline must keep serving)" >&2; exit 1; }
[ "$(svc_id "$G2_ID")" = "$G2_SID" ] || { echo "baseline carrier identity changed across the L1 failure (zero-touch violated)" >&2; exit 1; }
AFTER_FAIL_ROWS=$(cli --json deployments list --app "$APP_ID" | grep -c '"state"' || true)
[ "$AFTER_FAIL_ROWS" -eq 3 ] || { echo "expected exactly 3 deployment rows after the failed one (no Replay), got $AFTER_FAIL_ROWS" >&2; exit 1; }
[ "$(app_services | grep -c .)" -eq 1 ] || {
  echo "failed generation left carriers behind" >&2
  docker exec "$MGR_CID" docker service ls >&2 || true
  cli --json deployments list --app "$APP_ID" >&2 || true
  exit 1
}
log "L1 failure: route still v2, baseline carrier untouched, no replay row, no residue"

log "leg 4: manual switch-back inside the observing window (fleetly rollback)"
bg_compose v4
cli deploy --app "$APP_ID" --compose-file /root/bg-v4.yaml >/dev/null
wait_state observing
i=0; got=""
while [ "$i" -lt 45 ]; do
  got=$(serving_version)
  [ "$got" = "v4" ] && break
  i=$((i + 1)); sleep 2
done
[ "$got" = "v4" ] || { echo "route did not reach v4 before the switch-back (last: $got)" >&2; exit 1; }
log "v4 serving; rolling back to the last succeeded revision (v2)"
cli rollback --app "$APP_ID" >/dev/null
# 回放部署自身是蓝绿：其 releasing 窗内服务代即回基线（v2）——切回生效
# 早于回放收口。
i=0; got=""
while [ "$i" -lt 60 ]; do
  got=$(serving_version)
  [ "$got" = "v2" ] && break
  i=$((i + 1)); sleep 2
done
[ "$got" = "v2" ] || { echo "rollback never switched traffic back to v2 (last: $got)" >&2; exit 1; }
# 切回窗内基线载体未重建（v2 代服务 ID 不变——同一载体继续在服）。
[ "$(svc_id "$G2_ID")" = "$G2_SID" ] || { echo "baseline carrier was rebuilt during the switch-back" >&2; exit 1; }
log "traffic back on v2; baseline carrier not rebuilt (same service id)"
wait_state succeeded
[ "$(app_services | grep -c .)" -eq 1 ] || { echo "switch-back left carriers behind" >&2; app_services >&2; exit 1; }
log "switch-back collected to a single carrier"

log "leg 5: supersede inside the double-generation window (zero residue)"
bg_compose v5
cli deploy --app "$APP_ID" --compose-file /root/bg-v5.yaml >/dev/null
wait_state observing
[ "$(app_services | grep -c .)" -eq 2 ] || { echo "expected a live double-generation window before superseding" >&2; app_services >&2; exit 1; }
bg_compose v6
cli deploy --app "$APP_ID" --compose-file /root/bg-v6.yaml --supersede >/dev/null
wait_state succeeded 300
LEFT=$(app_services | grep -c .)
[ "$LEFT" -eq 1 ] || { echo "supersede left $LEFT carriers (expected 1 — zero residue)" >&2; app_services >&2; exit 1; }
log "supersede collected the window to a single carrier (zero residue)"

log "leg 6: both generations honor the same placement across two nodes"
PRE_SERVICES=$(app_services)
docker exec -i "$MGR_CID" sh -c "cat > /root/bg-spec.json" <<JSON
{"source":{"image":{"ref":"h2cbackend:local"}},
 "processes":[{"name":"web","image":"h2cbackend:local","replicas":1,
   "env":{"BG_VERSION":"v7"},
   "ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}],
   "networks":["default"],
   "placement":{"node_ids":["$WORKER_NODE_ID"]},
   "strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]}
JSON
cli deploy --app "$APP_ID" --spec-file /root/bg-spec.json >/dev/null
wait_state observing
NEW_SVC=""
for s in $(app_services); do
  case "$PRE_SERVICES" in
    *"$s"*) ;;
    *) NEW_SVC="$s"; break ;;
  esac
done
[ -n "$NEW_SVC" ] || { echo "could not identify the new-generation carrier in the placement window" >&2; app_services >&2; exit 1; }
# 新代 task 落 worker（Placement 一致锚：声明约束的两代面以新代为断言
# 对象——旧代按基线 spec 无约束自由落点，窗口内不翻新）。
G7_TASK_NODES=$(docker exec "$MGR_CID" docker service ps "$NEW_SVC" --format '{{.Node}}' --filter desired-state=running 2>/dev/null || true)
case "$G7_TASK_NODES" in
  *"$WRK_HOST"*) log "new-generation tasks honor the worker placement ($G7_TASK_NODES)" ;;
  *) echo "new-generation tasks not on the pinned worker (got: $G7_TASK_NODES; worker: $WRK_HOST)" >&2; exit 1 ;;
esac
CONSTRAINT=$(docker exec "$MGR_CID" docker service inspect "$NEW_SVC" \
  --format '{{join .Spec.TaskTemplate.Placement.Constraints ","}}')
case "$CONSTRAINT" in
  *node.labels.fleetly.node.id=="$WORKER_NODE_ID"*) log "placement constraint anchors the platform node id" ;;
  *) echo "placement constraint wrong: $CONSTRAINT" >&2; exit 1 ;;
esac
wait_state succeeded
i=0; got=""
while [ "$i" -lt 45 ]; do
  got=$(serving_version)
  [ "$got" = "v7" ] && break
  i=$((i + 1)); sleep 2
done
[ "$got" = "v7" ] || { echo "route did not settle on v7 after the placement leg (last: $got)" >&2; exit 1; }
log "placement leg green (single carrier, serving v7)"

log "leg 7: P16 dual alias disambiguates same-process apps on a shared network"
cli apps create --project "$PROJECT_ID" alpha >/dev/null
cli apps create --project "$PROJECT_ID" beta >/dev/null
ALPHA_ID=$(cli --json apps list --project "$PROJECT_ID" | grep -B3 '"alpha"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
BETA_ID=$(cli --json apps list --project "$PROJECT_ID" | grep -B3 '"beta"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
[ -n "$ALPHA_ID" ] && [ -n "$BETA_ID" ] || { echo "could not resolve alpha/beta app ids" >&2; exit 1; }
for name in alpha beta; do
  docker exec -i "$MGR_CID" sh -c "cat > /root/dual-$name.yaml" <<YAML
services:
  web:
    image: h2cbackend:local
    environment:
      BG_VERSION: dual-$name
    ports: ["8080"]
    networks: [default]
YAML
  cli deploy --app "$([ "$name" = alpha ] && echo "$ALPHA_ID" || echo "$BETA_ID")" \
    --compose-file "/root/dual-$name.yaml" >/dev/null
done
APP_ID_SAVE="$APP_ID"
for aid in "$ALPHA_ID" "$BETA_ID"; do
  APP_ID="$aid"
  wait_state succeeded
done
APP_ID="$APP_ID_SAVE"

NET_CARRIER=$(docker exec "$MGR_CID" docker network ls --format '{{.Name}}' | grep '^fleetly-net-' | head -1)
[ -n "$NET_CARRIER" ] || { echo "no project network carrier found" >&2; exit 1; }
dual_probe() {
  docker exec "$MGR_CID" docker run --rm --network "$NET_CARRIER" busybox:1.37 sh -c "$1"
}
# busybox 1.37 nslookup 输出形态：Name:/Address: 行对（服务端行带 :53 端口
# ——行尾锚定排除）。
dns_addr() {
  dual_probe "nslookup $1 2>/dev/null" | sed -n 's/^Address:[[:space:]]*\([0-9.]*\)$/\1/p' | head -1
}
A_ADDR=$(dns_addr web.alpha)
B_ADDR=$(dns_addr web.beta)
[ -n "$A_ADDR" ] && [ -n "$B_ADDR" ] || { echo "full-name DNS did not resolve (alpha: $A_ADDR beta: $B_ADDR)" >&2; exit 1; }
[ "$A_ADDR" != "$B_ADDR" ] || { echo "full names collapsed to one address ($A_ADDR) — disambiguation failed" >&2; exit 1; }
log "full names resolve distinctly (web.alpha=$A_ADDR web.beta=$B_ADDR)"

A_BODY=$(dual_probe 'wget -q -O - http://web.alpha:8080/ 2>/dev/null' || true)
B_BODY=$(dual_probe 'wget -q -O - http://web.beta:8080/ 2>/dev/null' || true)
case "$A_BODY" in *BG-VERSION\ dual-alpha*) ;; *) echo "web.alpha did not serve the alpha app (body: $A_BODY)" >&2; exit 1 ;; esac
case "$B_BODY" in *BG-VERSION\ dual-beta*) ;; *) echo "web.beta did not serve the beta app (body: $B_BODY)" >&2; exit 1 ;; esac
BARE=$(dual_probe 'nslookup web 2>/dev/null' | sed -n 's/^Address:[[:space:]]*\([0-9.]*\)$/\1/p' | grep -c . || true)
[ "${BARE:-0}" -ge 2 ] || { echo "bare name did not round-robin across both apps (addresses: $BARE)" >&2; dual_probe 'nslookup web' >&2 || true; exit 1; }
log "bare name resolves to both apps (DNS round-robin face); full names disambiguate"

log "ALL BLUE-GREEN + P16 LEGS PASSED"
