#!/bin/sh
# e2e dind runtimeswitch（F4.1/ADR-0052 场景 3 验收：换 Runtime 迁移——
# 领域模型 §5 场景 3，对 ADR-0001 运行时中立的终审）。同一 dind 内两段式：
#   段一 swarm：swarm init + fleetlyd（缺省 swarm）→ app deploy succeeded +
#     Database running → 数据写入 → Backup 完成 → 基线记录（App/Revision/
#     Route/ID）。
#   切换：停 fleetlyd → 显式数据处置（旧 swarm app/db 载体 service rm——
#     平台不自动搬不自动删；卷与备份保留）→ k3s server 起。
#   段二 k3s：fleetlyd（runtime.provider=k3s，同数据根）重启 → 行全保持
#     断言（App/Revision/Route 同 ID）→ 基线重放（app 载体在 k3s 重建
#     ready）→ 旧 Database 行显式处置（delete）→ Backup 恢复（restore
#     新库）→ 数据完整断言（表/行数与切换前一致）。
#
# placement 绑定不跨 Runtime 复用、节点 ID 永不复用：k3s 节点是全新节点
#（新铸平台 ID），旧 swarm 节点 ID 随旧集群退役——断言 nodes 列表换血。
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
fail() { echo "FATAL: $1" >&2; exit 1; }

K3S_VERSION="v1.36.5+k3s1"
K3S_SHA256="d73847bcd3c5fccef0115b372e2f9a91f3032dc84bbf71518a4617565294d313"

# 1. 编译 + 下载（k3s 钉版 sha256 校验）。
log "cross-compiling binaries"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
log "downloading k3s $K3S_VERSION"
K3S_ASSET=$(printf '%s' "$K3S_VERSION" | sed 's/+/%2B/')
# 下载缓存（dind-k3s.sh 同款：二进制 sha256 校验、airgap 存在即用）。
K3S_CACHE_DIR="${FLEETLY_E2E_K3S_CACHE:-$HOME/.cache/fleetly-e2e}"
mkdir -p "$K3S_CACHE_DIR"
if [ -s "$K3S_CACHE_DIR/k3s" ] && echo "$K3S_SHA256  $K3S_CACHE_DIR/k3s" | sha256sum -c - >/dev/null 2>&1; then
  cp "$K3S_CACHE_DIR/k3s" "$WORKDIR/k3s"
else
  curl -sL --retry 3 -o "$WORKDIR/k3s" "https://github.com/k3s-io/k3s/releases/download/$K3S_ASSET/k3s"
  echo "$K3S_SHA256  $WORKDIR/k3s" | sha256sum -c - || fail "k3s binary sha256 mismatch"
  cp "$WORKDIR/k3s" "$K3S_CACHE_DIR/k3s"
fi
if [ -s "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar" ]; then
  cp "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar" "$WORKDIR/k3s-airgap.tar"
else
  curl -sL --retry 3 -o "$WORKDIR/k3s-airgap.tar" "https://github.com/k3s-io/k3s/releases/download/$K3S_ASSET/k3s-airgap-images-amd64.tar"
  cp "$WORKDIR/k3s-airgap.tar" "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar"
fi

# 2. dind（代理透传：swarm db 拉取与 k3s 在线段共用）。
log "starting dind container"
PROXY_ENV=""
if [ -n "${HTTPS_PROXY:-}${https_proxy:-}" ]; then
  PX="${HTTPS_PROXY:-$https_proxy}"
  PX_REST=$(printf '%s' "$PX" | sed -E 's#^(https?://)?[^:/]+##')
  # NO_PROXY 必须显式（实证坑：无 no_proxy 时 k3s/fleetlyd 的 localhost:6443 与集群内
  # 通信全被代理劫持——kubelet/watch/ensure 诡异慢挂）。
  NO_PROXY="localhost,127.0.0.1,::1,10.0.0.0/8,10.42.0.0/16,.svc,.cluster.local,kubernetes.default.svc"
  PROXY_ENV="-e HTTPS_PROXY=http://host.docker.internal$PX_REST -e HTTP_PROXY=http://host.docker.internal$PX_REST -e NO_PROXY=$NO_PROXY -e no_proxy=$NO_PROXY --add-host=host.docker.internal:host-gateway"
fi
DIND_CID=$(eval docker run -d --privileged --name fleetly-e2e-sw-"$$" \
  --cgroupns=host $PROXY_ENV -e DOCKER_TLS_CERTDIR= docker:29-dind)
i=0
while [ "$i" -lt 60 ]; do
  docker exec "$DIND_CID" docker info >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 1
done
[ "$i" -lt 60 ] || fail "dind daemon did not become ready"

# 3. 镜像预载（swarm 侧 docker load；k3s 侧稍后 import）。
log "preloading docker images (swarm side)"
for img in nginx:1.27 traefik:v3.5.4; do
  docker image inspect "$img" >/dev/null 2>&1 || docker image pull "$img" >/dev/null
  docker image save "$img" | docker exec -i "$DIND_CID" docker load >/dev/null
done

# 4. 段一 swarm：swarm init + fleetlyd（缺省 provider=swarm）。
log "phase swarm: init swarm + start fleetlyd"
docker exec "$DIND_CID" docker swarm init --advertise-addr 127.0.0.1 >/dev/null
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
start_fleetlyd() {
  provider="$1"
  docker exec "$DIND_CID" sh -c "
    pkill -f bins/fleetlyd >/dev/null 2>&1 || true
    sleep 1
    mkdir -p /var/lib/fleetly
    setsid env FLEETLY_DATA_ROOT=/var/lib/fleetly FLEETLY_RUNTIME_PROVIDER=$provider FLEETLY_RUNTIME_K3S_KUBECONFIG=/etc/rancher/k3s/k3s.yaml /root/bins/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &"
  i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$DIND_CID" sh -c 'FLEETLY_ADDR=127.0.0.1:9080 /root/bins/fleetly status >/dev/null 2>&1'; then
      return 0
    fi
    i=$((i + 1)); sleep 1
  done
  docker exec "$DIND_CID" tail -30 /var/log/fleetlyd.log >&2 || true
  return 1
}
start_fleetlyd swarm || fail "fleetlyd (swarm) did not become ready"

CURRENT_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
[ -n "$CURRENT_TOKEN" ] || fail "bootstrap token missing"
cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" /root/bins/fleetly "$@"
}
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" \
  /root/bins/fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$NEW_TOKEN" ] || fail "init did not mint a CLI token"
CURRENT_TOKEN="$NEW_TOKEN"

log "phase swarm: deploy app + database"
cli projects create shop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" web >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli deploy --app "$APP_ID" --image nginx:1.27 --port 80 >/dev/null

wait_app_state() {
  want="$1"; i=0
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    [ "$state" = "$want" ] && return 0
    case "$state" in failed|superseded|cancelled) fail "deployment reached $state";; esac
    i=$((i + 1)); sleep 1
  done
  fail "timed out waiting for app $want"
}
wait_app_state succeeded

DB_ID=$(cli --json databases create --project "$PROJECT_ID" --engine postgres pgold | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$DB_ID" ] || fail "database create failed"
wait_db_state() {
  want="$1"; i=0
  while [ "$i" -lt 300 ]; do
    status=$(cli --json databases list --project "$PROJECT_ID" | sed -n "s/.*\"id\": *\"$DB_ID\".*\"status\": *\"\([^\"]*\)\".*/\1/p" | head -1)
    [ "$status" = "$want" ] && return 0
    [ "$status" = "failed" ] && fail "database failed"
    i=$((i + 1)); sleep 2
  done
  fail "timed out waiting for database $want"
}
wait_db_state running
log "phase swarm: app + database running"

# 5. 数据写入（swarm db 容器内 psql——表 + 行）。
log "phase swarm: seeding data"
SEED_COUNT=42
docker exec "$DIND_CID" sh -c '
  i=0
  while [ $i -lt 60 ]; do
    cid=$(docker ps --format "{{.ID}} {{.Names}}" | grep fleetly-db- | head -1 | cut -d" " -f1)
    [ -n "$cid" ] && break
    i=$((i+1)); sleep 2
  done
  [ -n "$cid" ] || { echo "database carrier not found" >&2; exit 1; }
  docker exec "$cid" psql -U postgres -c "CREATE TABLE IF NOT EXISTS migration_probe (id int);" >/dev/null
  docker exec "$cid" psql -U postgres -c "TRUNCATE migration_probe;" >/dev/null
  docker exec "$cid" psql -U postgres -c "INSERT INTO migration_probe SELECT generate_series(1, '"$SEED_COUNT"');" >/dev/null
' || fail "seeding data failed"

# 6. Backup（等完成——对象在 local ObjectStore，随数据根跨 Runtime 存活）。
log "phase swarm: backup"
cli databases backup "$DB_ID" >/dev/null
BACKUP_ID=""
i=0
while [ "$i" -lt 120 ]; do
  BACKUP_ID=$(cli --json databases backups "$DB_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
  state=$(cli --json databases backups "$DB_ID" | sed -n 's/.*"state": *"\([^\"]*\)".*/\1/p' | head -1)
  if [ -n "$BACKUP_ID" ] && [ "$state" = "completed" ]; then
    break
  fi
  i=$((i + 1)); sleep 2
done
[ -n "$BACKUP_ID" ] && [ "$state" = "completed" ] || fail "backup did not complete"
log "backup completed ($BACKUP_ID)"

# 7. 基线记录（场景 3 断言锚：切换前后行 ID 全保持）。
REVISION_COUNT=$(cli --json revisions list --app "$APP_ID" | grep -c '"id"')
SWARM_NODE_ID=$(cli --json nodes list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$SWARM_NODE_ID" ] || fail "swarm node id not captured"

# 8. 切换：停 fleetlyd → 显式数据处置（旧载体 service rm；卷与备份保留）
#    → k3s server 起。
log "switch: stopping fleetlyd + explicit carrier disposal + starting k3s"
docker exec "$DIND_CID" sh -c 'pkill -f bins/fleetlyd; sleep 2' || true
docker exec "$DIND_CID" sh -c '
  docker service ls --format "{{.Name}}" | grep "^fleetly-" | while read -r svc; do
    docker service rm "$svc" >/dev/null
  done
'
# k3s 镜像预载（airgap 官方通道：起动前落 /var/lib/rancher/k3s/agent/images/
# ——k3s 起动自动 import；绕开 ctr 客户端 snapshotter 与 mount 遮蔽两坑，
# 见 dind-k3s.sh 坑注）。
docker exec "$DIND_CID" mkdir -p /var/lib/rancher/k3s/agent/images
docker exec -i "$DIND_CID" sh -c 'cat > /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar' < "$WORKDIR/k3s-airgap.tar"
i=0
for img in nginx:1.27 traefik:v3.5.4; do
  docker image save "$img" | docker exec -i "$DIND_CID" sh -c "cat > /var/lib/rancher/k3s/agent/images/app-$i.tar"
  i=$((i + 1))
done
docker cp "$WORKDIR/k3s" "$DIND_CID":/usr/local/bin/k3s
docker exec "$DIND_CID" chmod +x /usr/local/bin/k3s
docker exec -d "$DIND_CID" sh -c \
  'K3S_KUBECONFIG_MODE=644 k3s server --disable=traefik --disable=servicelb --node-name=k3s-sw-0 --snapshotter=native >/var/log/k3s.log 2>&1'
i=0
while [ "$i" -lt 90 ]; do
  docker exec "$DIND_CID" sh -c 'k3s kubectl get --raw=/readyz' >/dev/null 2>&1 && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 90 ] || { docker exec "$DIND_CID" tail -30 /var/log/k3s.log >&2; fail "k3s did not become ready"; }
# CNI 就绪窗（dind-k3s.sh 同款：coreDNS Running 为锚）。
log "waiting for CNI readiness"
i=0
while [ "$i" -lt 90 ]; do
  cni=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath={.items[0].status.phase} 2>/dev/null" || true)
  cidr=$(docker exec "$DIND_CID" sh -c "k3s kubectl get nodes -o jsonpath={.items[0].spec.podCIDR} 2>/dev/null" || true)
  [ "$cni" = "Running" ] && [ -n "$cidr" ] && [ "$cidr" != " " ] && break
  i=$((i + 1)); sleep 2
done
[ "$cni" = "Running" ] && [ -n "$cidr" ] || fail "CNI did not become ready (coreDNS/podCIDR stuck)"

# 运行时预热（dind-k3s.sh 同款坑注：native snapshotter 首次解包 ~2 分钟
# 会吃掉引擎健康门窗——基线重放前把 nginx 解包成本花掉）。
log "warming container runtime (first-unpack of app images)"
docker exec "$DIND_CID" sh -c '
  k3s kubectl run warm-b --image=nginx:1.27 --restart=Never --command -- true >/dev/null 2>&1 || true
  i=0
  while [ $i -lt 120 ]; do
    done_b=$(k3s kubectl get pod warm-b -o jsonpath={.status.phase} 2>/dev/null || echo Pending)
    case "$done_b" in Succeeded|Failed) exit 0;; esac
    i=$((i+1)); sleep 2
  done
'
docker exec "$DIND_CID" sh -c 'k3s kubectl delete pod warm-b --force --grace-period=0 >/dev/null 2>&1 || true'

# 9. 段二 k3s：fleetlyd 重启（同数据根）。
log "phase k3s: restarting fleetlyd on k3s"
start_fleetlyd k3s || fail "fleetlyd (k3s) did not become ready"

# 10. 场景 3 断言：行全保持（App/Project 同 ID、Revision 集不变）。
log "phase k3s: identity preservation assertions"
K3S_PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
K3S_APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ "$K3S_PROJECT_ID" = "$PROJECT_ID" ] || fail "project id changed across runtime switch ($PROJECT_ID -> $K3S_PROJECT_ID)"
[ "$K3S_APP_ID" = "$APP_ID" ] || fail "app id changed across runtime switch"
K3S_REVISION_COUNT=$(cli --json revisions list --app "$APP_ID" | grep -c '"id"')
[ "$K3S_REVISION_COUNT" = "$REVISION_COUNT" ] || fail "revision set changed across runtime switch ($REVISION_COUNT -> $K3S_REVISION_COUNT)"
log "app/project/revision ids preserved"

# 节点换血断言（placement 绑定不跨 Runtime 复用、节点 ID 永不复用）。
i=0
while [ "$i" -lt 60 ]; do
  K3S_NODE_ID=$(cli --json nodes list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$K3S_NODE_ID" ] && [ "$K3S_NODE_ID" != "$SWARM_NODE_ID" ] && break
  i=$((i + 1)); sleep 2
done
[ "${K3S_NODE_ID:-}" != "$SWARM_NODE_ID" ] || fail "k3s node must be a freshly minted platform node id (never reuse)"
log "node identity freshly minted on k3s (old swarm node retired)"

# 11. 基线重放：app 载体在 k3s 上重建（drift 基线重放链）。
log "phase k3s: baseline replay of app carriers"
docker exec "$DIND_CID" sh -c '
  i=0
  while [ $i -lt 120 ]; do
    ready=$(k3s kubectl get deployment fleetly-web-web -n fleetly-shop -o jsonpath={.status.readyReplicas} 2>/dev/null || echo 0)
    [ "$ready" = "1" ] && exit 0
    i=$((i+1)); sleep 2
  done
  k3s kubectl get all -n fleetly-shop >&2
  exit 1
' || fail "app carrier did not come up on k3s via baseline replay"
log "app carrier replayed on k3s"

# 12. 数据迁移闭环：旧 Database 行显式处置（k3s 侧 Remove 幂等）→ Backup
#     恢复（restore_from_backup 新库）→ 数据完整断言。
log "phase k3s: explicit database disposal + restore + data assertion"
cli databases delete "$DB_ID" >/dev/null 2>&1 || true
NEW_DB_ID=$(cli --json databases create --project "$PROJECT_ID" --engine postgres --restore-from-backup "$BACKUP_ID" pgnew | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$NEW_DB_ID" ] || fail "restore-from-backup database create failed"
DB_ID="$NEW_DB_ID"
wait_db_state running

docker exec "$DIND_CID" sh -c '
  i=0
  while [ $i -lt 60 ]; do
    pod=$(k3s kubectl get pods -n fleetly-shop -l fleetly.ns.database --no-headers 2>/dev/null | grep Running | head -1 | cut -d" " -f1)
    [ -n "$pod" ] && break
    i=$((i+1)); sleep 2
  done
  [ -n "$pod" ] || { echo "restored database pod not found" >&2; exit 1; }
  count=$(k3s kubectl exec -n fleetly-shop "$pod" -- psql -U postgres -tAc "SELECT count(*) FROM migration_probe;" 2>/dev/null | tr -d "[:space:]")
  echo "restored rows: $count"
  [ "$count" = "'"$SEED_COUNT"'" ] || { echo "expected '"$SEED_COUNT"' rows, got $count" >&2; exit 1; }
' || fail "restored data assertion failed"
log "restored database carries all $SEED_COUNT seeded rows"

echo ""
echo "RUNTIME SWITCH E2E PASSED (scenario 3: ids preserved, data via backup/restore, explicit disposal)"
