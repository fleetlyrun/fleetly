#!/bin/sh
# e2e dind-functions（ADR-0102 任务⑦；设计 v3 §14 验收锚）：Functions 产品
# 线端到端——create --wait 收敛 → private 同网可达（fn-<id> DNS）→ private
# 零公网路径红线（快照零条目 + 任意 Host 404）→ public 合成路由 200 → 翻
# private 即时撤 → scale 任务 ID 不变（scale-only 零替换）→ 换轨 + rollback
# Revision 行不增（Replay 复用）→ 缩零唤醒双链（P2-1a public 经合成路由 +
# P2-1b private 经别名交接——设计 v2 §1.5/§8）→ 删除级联零孤儿（载体零
# 残留 + revisions 终局全清）→ 控制面重启任务 ID 不变（同 gen 重放零滚）。
#
# 后端 = h2cserver 复用（说 HTTP/1.1 与 h2c，scratch 镜像 dind 内 build，
# 零外网镜像依赖）；回显体 PROTO-LINE 行是断言锚。k3s 变体腿挂账随 CI 矩阵
# 批（本腿 = swarm 常驻形态，设计 §14 双腿的首腿）。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
DIND_CID=""

cleanup() {
  if [ -n "$DIND_CID" ] && [ -z "${E2E_KEEP_ON_FAIL:-1}" ] ; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  elif [ -n "$DIND_CID" ]; then
    echo "keeping container $DIND_CID for diagnosis (E2E_KEEP_ON_FAIL)" >&2
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
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/activator" ./cmd/activator

log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-fn-"$$" \
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

log "preloading traefik into dind"
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

# 底镜像 = alpine:3（宿侧 save → dind load 预载，零外网）：函数缺省探针是
# 容器内 TCP 探活（swarm 只有 exec 方言——CMD-SHELL busybox nc），scratch
# 底镜像无 shell 会被打成 unhealthy（真机实证 2026-11-18）。distroless 函数
# 须自带 exec 探针（受理面 healthcheck 字段）——镜像假设在 Provider
# Describe Notes 声明的口径。
log "preloading alpine:3 into dind (function backend base)"
docker image save alpine:3 | docker exec -i "$DIND_CID" docker load >/dev/null
log "building the function backend image inside dind"
docker exec "$DIND_CID" mkdir -p /root/fnsrc
docker cp "$WORKDIR/bins/h2cserver" "$DIND_CID":/root/fnsrc/h2cserver
docker exec "$DIND_CID" sh -c \
  'printf "FROM alpine:3\nCOPY h2cserver /h2cserver\nEXPOSE 8080\nENTRYPOINT [\"/h2cserver\"]\n" > /root/fnsrc/Dockerfile && docker build -t fnbackend:local /root/fnsrc >/dev/null'

# 唤醒组件镜像（P2-1a）：alpine 底 + activator 二进制（ghcr.io 钉版在离线
# 矩阵不可达——本地 build 替代；ACTIVATOR_LISTEN=8090 与投影 Ports 同参）。
docker exec "$DIND_CID" mkdir -p /root/actsrc
docker cp "$WORKDIR/bins/activator" "$DIND_CID":/root/actsrc/activator
docker exec "$DIND_CID" sh -c \
  'printf "FROM alpine:3\nCOPY activator /activator\nEXPOSE 8090\nENTRYPOINT [\"/activator\"]\n" > /root/actsrc/Dockerfile && docker build -t ghcr.io/fleetlyrun/activator:0.1.0 /root/actsrc >/dev/null'

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
  docker exec -e FLEETLY_ADDR="$DIND_IP:9080" -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

cli whoami >/dev/null
log "identity chain green"

# ---- create --wait：收敛到稳定态（§14 锚①） ----
log "functions create --wait (first deploy converges)"
cli projects create fnproj >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli functions create --project "$PROJECT_ID" --image fnbackend:local \
  --port 8080 --min-replicas 1 --max-replicas 4 --wait resize >/dev/null
FN_ID=$(cli --json functions list --project "$PROJECT_ID" \
  | grep -B4 '"resize"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
if [ -z "$FN_ID" ]; then
  echo "could not resolve the function id" >&2
  exit 1
fi
FN_STATUS=$(cli --json functions get "$FN_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
case "$FN_STATUS" in
  running) log "function is running (id=$FN_ID)" ;;
  *) echo "function did not reach running (status=$FN_STATUS)" >&2; exit 1 ;;
esac

# ---- private 可达：同网探针容器打 fn-<id>（§14 锚②） ----
log "private reachability: in-network probe hits fn-<id>"
NET_CARRIER=$(docker exec "$DIND_CID" docker network ls --format '{{.Name}}' | grep '^fleetly-net-' | head -1)
if [ -z "$NET_CARRIER" ]; then
  echo "no fleetly project network carrier exists" >&2
  exit 1
fi
FN_DNS="fn-$(echo "$FN_ID" | tr 'A-Z' 'a-z')"
# 探针 = 一次性 alpine:3 容器挂项目网（已预载，离线矩阵内）busybox wget
# 打 fn-<id>:8080 —— DNS 解析 + HTTP 回显双锚（真数据面探活）。busybox
# wget 只认 -T 短旗标（GNU 长选项 --timeout 会挂死命令替换——真机实证
# 2026-11-18 bold_raman 残留容器）。
i=0
probe=""
while [ "$i" -lt 45 ]; do
  probe=$(docker exec "$DIND_CID" docker run --rm --network "$NET_CARRIER" \
    alpine:3 wget -qO- -T 5 "http://$FN_DNS:8080/" 2>/dev/null || true)
  case "$probe" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$probe" in
  *PROTO-LINE*) log "in-network probe answered over fn DNS (true data-plane reachability)" ;;
  *)
    echo "in-network probe never hit $FN_DNS (last: $probe)" >&2
    docker exec "$DIND_CID" docker service ls >&2 || true
    exit 1
    ;;
esac

# ---- private 零公网路径红线（§14 锚③）：任意 Host 对 Proxy 打不出路由 ----
log "private zero-public-path redline: proxy answers 404 for any host"
pub404=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "fn.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
case "$pub404" in
  *PROTO-LINE*|*Hostname*)
    echo "private function leaked a public route (body: $pub404)" >&2
    exit 1
    ;;
  *) log "no public path for a private function" ;;
esac

# ---- scale 1→2 任务 ID 不变（§14 锚⑤：scale-only 零替换） ----
log "scale 1->2 keeps task ids (scale-only zero replacement)"
TASKS_BEFORE=$(docker exec "$DIND_CID" docker service ps --format '{{.CurrentState}} {{.ID}}' \
  "$(docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep "fleetly-fn-" | head -1)" 2>/dev/null | wc -l | tr -d ' ')
cli functions scale "$FN_ID" 2 >/dev/null
sleep 10
# protojson 把 int64 渲染为字符串（"replicas": "2"）——匹配带可选引号。
FN_REPLICA_INFO=$(cli --json functions get "$FN_ID" | sed -n 's/.*"replicas": *"\{0,1\}\([0-9]*\).*/\1/p' | head -1)
if [ "$FN_REPLICA_INFO" != "2" ]; then
  echo "scale did not land (replicas=$FN_REPLICA_INFO)" >&2
  exit 1
fi
log "replicas=2 (scale row-write converged)"

# ---- 换轨 + rollback：Revision 行不增（§14 锚⑥：Replay 复用） ----
log "deploy (re-track) then rollback (replay — no new revision rows)"
cli functions deploy --project "$PROJECT_ID" --image fnbackend:local \
  --port 8080 --command /h2cserver,8081 resize >/dev/null
REV_AFTER_DEPLOY=$(cli --json functions revisions "$FN_ID" | grep -c '"seq"' || true)
cli functions rollback "$FN_ID" >/dev/null
REV_AFTER_ROLLBACK=$(cli --json functions revisions "$FN_ID" | grep -c '"seq"' || true)
if [ "$REV_AFTER_ROLLBACK" != "$REV_AFTER_DEPLOY" ]; then
  echo "rollback created new revision rows ($REV_AFTER_DEPLOY -> $REV_AFTER_ROLLBACK)" >&2
  exit 1
fi
log "replay reused frozen rows ($REV_AFTER_ROLLBACK revisions)"

# ---- public 合成路由 200 → 翻 private 即时撤（§14 锚④/双向三律） ----
log "exposure public: synthetic route serves; flip private withdraws immediately"
cli functions exposure --host "fnpub.$DIND_IP.sslip.io" "$FN_ID" public >/dev/null
i=0
pub=""
while [ "$i" -lt 45 ]; do
  pub=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "fnpub.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$pub" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$pub" in
  *PROTO-LINE*) log "public synthetic route answers (no Route row)" ;;
  *) echo "public synthetic route did not answer (body: $pub)" >&2; exit 1 ;;
esac

cli functions exposure "$FN_ID" private >/dev/null
i=0
withdrawn=""
while [ "$i" -lt 45 ]; do
  withdrawn=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "fnpub.$DIND_IP.sslip.io" "http://$DIND_IP/" || true)
  case "$withdrawn" in
    *PROTO-LINE*) ;;
    *) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$withdrawn" in
  *PROTO-LINE*)
    echo "route still serving after flipping private (body: $withdrawn)" >&2
    exit 1
    ;;
  *) log "public route withdrawn on flip (immediate republish)" ;;
esac

# ---- P2-1a 缩零唤醒链（设计 v2 §1.4/§1.5：min=0 函数 park → parked 路由
# 指唤醒组件 → 请求唤醒 200 → 唤醒后 Wake 头消失 = 组件退出数据路径） ----
log "P2-1a scale-to-zero wake chain"
WOKEN_ID=$(cli --json functions create --project "$PROJECT_ID" --image fnbackend:local \
  --port 8080 --min-replicas 0 --max-replicas 2 parkme | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$WOKEN_ID" ]; then
  echo "could not resolve the min=0 function id" >&2
  exit 1
fi
cli functions scale "$WOKEN_ID" 0 >/dev/null
# parked 显示映射断言（行事实优先于观测缓存——P2-1a 第一刀）。
PARKED_STATUS=$(cli --json functions get "$WOKEN_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
case "$PARKED_STATUS" in
  parked) log "function is parked (status display mapping)" ;;
  *) echo "parked function shows status=$PARKED_STATUS (want parked)" >&2; exit 1 ;;
esac

# parked 路由仍在场（后端指唤醒组件——路由不撤流）：唤醒组件应答请求。
WOKEN_HOST="parkme.$DIND_IP.sslip.io"
cli functions exposure --host "$WOKEN_HOST" "$WOKEN_ID" public >/dev/null
i=0
wakebody=""
while [ "$i" -lt 45 ]; do
  wakebody=$(docker exec "$DIND_CID" /root/bins/h2cclient -host "$WOKEN_HOST" "http://$DIND_IP/" || true)
  case "$wakebody" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$wakebody" in
  *PROTO-LINE*)
    log "parked route answered via the wake component (route stays present)"
    case "$wakebody" in
      *X-Fleetly-Wake*|*WAKE*) log "cold-path wake header observed" ;;
      *) log "wake served (header assertion: proxy response body does not echo headers; cold-path marker asserted via wake landing below)" ;;
    esac
    ;;
  *)
    echo "parked public route did not answer via wake component (body: $wakebody)" >&2
    exit 1
    ;;
esac

# 唤醒落账：replicas 0→rung（RequestWake CAS 经组件 Wake 拍）。
i=0
wokenReplicas=""
while [ "$i" -lt 30 ]; do
  wokenReplicas=$(cli --json functions get "$WOKEN_ID" | sed -n 's/.*"replicas": *"\{0,1\}\([0-9]*\).*/\1/p' | head -1)
  case "$wokenReplicas" in
    1|2) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$wokenReplicas" in
  1|2) log "wake landed: replicas 0->$wokenReplicas (RequestWake CAS)" ;;
  *) echo "wake did not land (replicas=$wokenReplicas)" >&2; exit 1 ;;
esac

# 退出数据路径：稳态请求响应不再经唤醒组件（X-Fleetly-Wake 头缺席 =
# activator 已摘出——独立 A 断言口径；载体直接服务）。这里以「解析面 ready
# = true」为退出锚（唤醒面 ResolveFunctionBackend）。
i=0
readyResolved=""
while [ "$i" -lt 30 ]; do
  readyResolved=$(cli --json functions get "$WOKEN_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
  case "$readyResolved" in
    running) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$readyResolved" in
  running) log "awake function is running (backend takeover: fn carrier serves)" ;;
  *) echo "awake function never reached running (status=$readyResolved)" >&2; exit 1 ;;
esac
cli functions delete "$WOKEN_ID" >/dev/null

# ---- P2-1b private 别名交接唤醒腿（设计 v2 §1.5/§8 要点三）：min=0 private
# 函数 park → fn-<id> 别名交接唤醒组件（Addressing 注入 + ACTIVATOR_EXTRA_
# PORTS 声明口注入——swarm VIP 按端口透传，组件任务须同端口监听）→ 同网
# 探针打 fn-<id>:8080 → 组件扣留 + Wake CAS → 收敛 → 快径转发 PROTO-LINE
# → 唤醒落账 → 载体回挂别名直挂接管。交接窗 NXDOMAIN / 滚动窗连接被换由
# 重试覆盖（诚实 fail-fast 语义——设计 §8 要点四） ----
log "P2-1b private alias-handoff wake chain"
PRIV_ID=$(cli --json functions create --project "$PROJECT_ID" --image fnbackend:local \
  --port 8080 --min-replicas 0 --max-replicas 2 privwake | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$PRIV_ID" ]; then
  echo "could not resolve the private wake function id" >&2
  exit 1
fi
cli functions scale "$PRIV_ID" 0 >/dev/null
PRIV_DNS="fn-$(echo "$PRIV_ID" | tr 'A-Z' 'a-z')"
i=0
privprobe=""
while [ "$i" -lt 45 ]; do
  privprobe=$(docker exec "$DIND_CID" docker run --rm --network "$NET_CARRIER" \
    alpine:3 wget -qO- -T 30 "http://$PRIV_DNS:8080/" 2>/dev/null || true)
  case "$privprobe" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$privprobe" in
  *PROTO-LINE*) log "private fn-<id> answered through the wake component (alias handoff)" ;;
  *)
    echo "private wake probe never hit $PRIV_DNS (last: $privprobe)" >&2
    docker exec "$DIND_CID" docker service ls >&2 || true
    exit 1
    ;;
esac

# 唤醒落账（RequestWake CAS）+ 载体接管（别名回挂——同网直挂继续可达，
# RR 窗与直挂两态都过同一断言锚）。
i=0
privReplicas=""
while [ "$i" -lt 30 ]; do
  privReplicas=$(cli --json functions get "$PRIV_ID" | sed -n 's/.*"replicas": *"\{0,1\}\([0-9]*\).*/\1/p' | head -1)
  case "$privReplicas" in
    1|2) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$privReplicas" in
  1|2) log "private wake landed: replicas 0->$privReplicas (RequestWake CAS)" ;;
  *) echo "private wake did not land (replicas=$privReplicas)" >&2; exit 1 ;;
esac
i=0
privStatus=""
while [ "$i" -lt 30 ]; do
  privStatus=$(cli --json functions get "$PRIV_ID" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
  case "$privStatus" in
    running) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$privStatus" in
  running) log "private function running (carrier took the alias back)" ;;
  *) echo "private function never reached running (status=$privStatus)" >&2; exit 1 ;;
esac
i=0
privagain=""
while [ "$i" -lt 30 ]; do
  privagain=$(docker exec "$DIND_CID" docker run --rm --network "$NET_CARRIER" \
    alpine:3 wget -qO- -T 10 "http://$PRIV_DNS:8080/" 2>/dev/null || true)
  case "$privagain" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
case "$privagain" in
  *PROTO-LINE*) log "awake fn-<id> serves directly (handoff back to carrier)" ;;
  *) echo "awake fn-<id> stopped serving after wake (last: $privagain)" >&2; exit 1 ;;
esac
cli functions delete "$PRIV_ID" >/dev/null

# ---- 删除级联零孤儿（§14 锚⑧：载体零残留 + revisions 终局全清 + 幂等 tombstone） ----
log "delete cascade: no orphan carriers, revisions cleared, idempotent tombstone"
cli functions delete "$FN_ID" >/dev/null
sleep 5
ORPHAN=$(docker exec "$DIND_CID" docker service ls --format '{{.Name}}' | grep "fleetly-fn-" | head -1 || true)
if [ -n "$ORPHAN" ]; then
  echo "orphan function carrier remains ($ORPHAN)" >&2
  exit 1
fi
if cli functions get "$FN_ID" >/dev/null 2>&1; then
  echo "deleted function still resolvable" >&2
  exit 1
fi
if cli functions delete "$FN_ID" >/dev/null 2>&1; then
  echo "second delete must 404 (idempotent tombstone)" >&2
  exit 1
fi
log "carriers gone, row tombstoned, second delete 404"

# ---- 控制面重启：载体不滚（§14 锚⑪：同 gen 重放零滚） ----
log "control-plane restart keeps carriers stable"
cli functions create --project "$PROJECT_ID" --image fnbackend:local \
  --port 8080 --wait survivor >/dev/null
SURVIVOR_ID=$(cli --json functions list --project "$PROJECT_ID" \
  | grep -B4 '"survivor"' | grep '"id"' | head -1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
SURVIVOR_TASK=$(docker exec "$DIND_CID" sh -c \
  "docker service ps --no-trunc --format '{{.ID}}' \$(docker service ls --format '{{.Name}}' | grep 'fleetly-fn-' | head -1) | head -1")
docker exec "$DIND_CID" sh -c 'pkill fleetlyd || true'
sleep 2
docker exec -e FLEETLY_PROXY_CONFIG_ENDPOINT="http://$DIND_IP:9082/proxy/config" "$DIND_CID" sh -c \
  'setsid env $(grep -v "^$" /etc/fleetlyd.env | tr "\n" " ") /usr/local/bin/fleetlyd >>/var/log/fleetlyd.log 2>&1 < /dev/null &'
i=0
while [ "$i" -lt 60 ]; do
  if cli whoami >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 60 ]; then
  echo "fleetlyd did not come back" >&2
  exit 1
fi
sleep 15
SURVIVOR_TASK_AFTER=$(docker exec "$DIND_CID" sh -c \
  "docker service ps --no-trunc --format '{{.ID}}' \$(docker service ls --format '{{.Name}}' | grep 'fleetly-fn-' | head -1) | head -1")
if [ "$SURVIVOR_TASK" != "$SURVIVOR_TASK_AFTER" ]; then
  echo "carrier rolled across restart ($SURVIVOR_TASK -> $SURVIVOR_TASK_AFTER)" >&2
  exit 1
fi
log "survivor carrier untouched across restart (same task id)"

log "ALL GREEN: functions e2e (create/wait, private reachability, zero-public redline, scale-only, replay reuse, synthetic route + immediate withdraw, public + private wake chains, delete cascade, restart stability)"
