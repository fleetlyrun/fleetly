#!/bin/sh
# e2e dind k3s 两节点腿（F4.1 补面批/ADR-0053 决策 5，ADR-0052 挂账 5）：
# 第二 dind 容器经 `fleetly nodes enroll` 材料**原样执行** k3s agent join
# （advertise server 地址解析的实机锚）→ 双节点 Ready + 平台锚定 →
# relay_online 双节点（exec 集中形态：per-node 回环注册）→ 卷钉住 worker
# 落点载体 → **worker pod 的 exec 全链**（apiserver→kubelet 通道 + 会话
# 路由的跨节点实证——集中形态的核心断言面）。
#
# 形态与 dind-k3s.sh 同源（fuse snapshotter / 代理透传 / airgap 预载 /
# exec stdin 注入）；worker 容器同款预载（fuse 四 apk + k3s 二进制 +
# airgap tar——agent 起动时自动 import）。swarm 双腿（smoke/two-node 分立）
# 同款组织。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
DIND_CID=""
WRK_CID=""
cleanup() {
  if [ -n "$WRK_CID" ]; then
    docker rm -f "$WRK_CID" >/dev/null 2>&1 || true
  fi
  if [ -n "$DIND_CID" ]; then
    docker rm -f "$DIND_CID" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }
fail() { echo "FATAL: $1" >&2; exit 1; }

# k3s 钉版（与 dind-k3s.sh 同 commit 纪律；守卫 TestK3sPinConstantAndE2EAgree）。
K3S_VERSION="v1.36.5+k3s1"
K3S_SHA256="d73847bcd3c5fccef0115b372e2f9a91f3032dc84bbf71518a4617565294d313"

K3S_SNAPSHOTTER="${FLEETLY_E2E_K3S_SNAPSHOTTER:-native}"
case "$K3S_SNAPSHOTTER" in
  native) K3S_SNAPSHOTTER_FLAG="native" ;;
  overlayfs) K3S_SNAPSHOTTER_FLAG="overlayfs" ;;
  fuse)
    [ -e /dev/fuse ] || { echo "FATAL: fuse form needs /dev/fuse on the host" >&2; exit 1; }
    K3S_SNAPSHOTTER_FLAG="fuse-overlayfs"
    ;;
  *) echo "FATAL: unknown FLEETLY_E2E_K3S_SNAPSHOTTER: $K3S_SNAPSHOTTER (native|overlayfs|fuse)" >&2; exit 1 ;;
esac

# 1. 交叉编译两个二进制（linux/amd64；纯 Go 零 cgo）。
log "cross-compiling fleetlyd + fleetly (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly

# 2. 下载 k3s（缓存同 dind-k3s.sh）。
log "downloading k3s $K3S_VERSION (pinned, sha256-verified)"
K3S_ASSET=$(printf '%s' "$K3S_VERSION" | sed 's/+/%2B/')
K3S_CACHE_DIR="${FLEETLY_E2E_K3S_CACHE:-$HOME/.cache/fleetly-e2e}"
mkdir -p "$K3S_CACHE_DIR"
if [ -s "$K3S_CACHE_DIR/k3s" ] && echo "$K3S_SHA256  $K3S_CACHE_DIR/k3s" | sha256sum -c - >/dev/null 2>&1; then
  cp "$K3S_CACHE_DIR/k3s" "$WORKDIR/k3s"
else
  curl -sL --retry 3 -o "$WORKDIR/k3s" \
    "https://github.com/k3s-io/k3s/releases/download/$K3S_ASSET/k3s"
  echo "$K3S_SHA256  $WORKDIR/k3s" | sha256sum -c - || fail "k3s binary sha256 mismatch"
  cp "$WORKDIR/k3s" "$K3S_CACHE_DIR/k3s"
fi
if [ -s "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar" ]; then
  cp "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar" "$WORKDIR/k3s-airgap.tar"
else
  curl -sL --retry 3 -o "$WORKDIR/k3s-airgap.tar" \
    "https://github.com/k3s-io/k3s/releases/download/$K3S_ASSET/k3s-airgap-images-amd64.tar"
  cp "$WORKDIR/k3s-airgap.tar" "$K3S_CACHE_DIR/k3s-airgap-images-amd64.tar"
fi

# 3. 起 manager dind（与 dind-k3s.sh 同款；代理透传 + NO_PROXY 显式）。
log "starting manager dind container"
PROXY_ENV=""
if [ -n "${HTTPS_PROXY:-}${https_proxy:-}" ]; then
  PX="${HTTPS_PROXY:-$https_proxy}"
  PX_HOST=$(printf '%s' "$PX" | sed -E 's#(https?://)?([^:/]+).*#\2#')
  PX_REST=$(printf '%s' "$PX" | sed -E 's#^(https?://)?[^:/]+##')
  # DIND_NET 网段（自定义 bridge 172.20.0.0/24）一并入 NO_PROXY——worker
  # join 地址/双节点 flannel 都走该网段。
  NO_PROXY="localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,10.42.0.0/16,.svc,.cluster.local,kubernetes.default.svc"
  PROXY_ENV="-e HTTPS_PROXY=http://host.docker.internal$PX_REST -e HTTP_PROXY=http://host.docker.internal$PX_REST -e NO_PROXY=$NO_PROXY -e no_proxy=$NO_PROXY --add-host=host.docker.internal:host-gateway"
fi
FUSE_DEV=""
[ "$K3S_SNAPSHOTTER" = "fuse" ] && FUSE_DEV="--device /dev/fuse"
DIND_NET="fleetly-e2e-k3stw-$$"
docker network create "$DIND_NET" >/dev/null 2>&1 || true
DIND_CID=$(eval docker run -d --privileged --name fleetly-e2e-k3stw-m-"$$" \
  --cgroupns=host \
  --network "$DIND_NET" \
  $FUSE_DEV \
  $PROXY_ENV \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

wait_docker() {
  i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$1" docker info >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  fail "dind daemon did not become ready ($2)"
}
log "waiting for manager dind daemon"
wait_docker "$DIND_CID" manager

# fuse helper 预载（manager + worker 同款；apk 宿主侧下载一次双容器注入——
# 容器内 apk 经代理不可用的既定坑）。
if [ "$K3S_SNAPSHOTTER" = "fuse" ]; then
  log "fetching fuse-overlayfs helpers (four apks)"
  APK_BASE="https://dl-cdn.alpinelinux.org/alpine/v3.24"
  for apk in \
    main/x86_64/fuse-common-3.18.3-r0.apk \
    main/x86_64/fuse3-libs-3.18.3-r0.apk \
    main/x86_64/fuse3-3.18.3-r0.apk \
    community/x86_64/fuse-overlayfs-1.16-r0.apk; do
    name=$(basename "$apk")
    # 下载缓存（k3s 缓存目录同库——alpine CDN 经代理有瞬态抖动；CI actions
    # cache 路径恰同此目录）。
    if [ -s "$K3S_CACHE_DIR/$name" ]; then
      cp "$K3S_CACHE_DIR/$name" "$WORKDIR/$name"
    else
      curl -sL --retry 3 -o "$WORKDIR/$name" "$APK_BASE/$apk" \
        || { echo "FATAL: apk fetch failed: $name" >&2; exit 1; }
      cp "$WORKDIR/$name" "$K3S_CACHE_DIR/$name"
    fi
  done
  install_fuse_apks() {
    for apk in fuse-common-3.18.3-r0.apk fuse3-libs-3.18.3-r0.apk fuse3-3.18.3-r0.apk fuse-overlayfs-1.16-r0.apk; do
      docker exec -i "$1" sh -c "cat > /tmp/$apk" < "$WORKDIR/$apk"
    done
    docker exec "$1" sh -c 'cd /tmp && apk add --allow-untrusted ./fuse-common-*.apk ./fuse3-libs-*.apk ./fuse3-*.apk ./fuse-overlayfs-*.apk >/dev/null 2>&1' \
      || { echo "FATAL: fuse helper apk install failed" >&2; exit 1; }
  }
  install_fuse_apks "$DIND_CID"
fi

# 4. manager 镜像预载（airgap 官方通道；worker 侧随后同款）。
log "staging images for k3s auto-import (manager)"
docker exec "$DIND_CID" mkdir -p /var/lib/rancher/k3s/agent/images
docker exec -i "$DIND_CID" sh -c 'cat > /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar' < "$WORKDIR/k3s-airgap.tar"
for img in nginx:1.27 busybox:1.37; do
  docker image inspect "$img" >/dev/null 2>&1 || docker image pull "$img" >/dev/null
  name=$(printf '%s' "$img" | sed 's#/#-#g')
  docker image save "$img" | docker exec -i "$DIND_CID" sh -c "cat > /var/lib/rancher/k3s/agent/images/app-$name.tar"
done

# 5. manager 起 k3s server + fleetlyd（与 dind-k3s.sh 同款；节点名带 m 前缀）。
log "injecting and starting k3s server (manager)"
docker cp "$WORKDIR/k3s" "$DIND_CID":/usr/local/bin/k3s
docker exec "$DIND_CID" chmod +x /usr/local/bin/k3s
docker exec -d "$DIND_CID" sh -c \
  "K3S_KUBECONFIG_MODE=644 k3s server --disable=traefik --disable=servicelb --node-name=k3s-tw-m --snapshotter=$K3S_SNAPSHOTTER_FLAG >/var/log/k3s.log 2>&1"
i=0
while [ "$i" -lt 90 ]; do
  if docker exec "$DIND_CID" sh -c 'k3s kubectl get --raw=/readyz' >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$i" -lt 90 ] || { docker exec "$DIND_CID" tail -30 /var/log/k3s.log >&2; fail "k3s did not become ready"; }
log "waiting for CNI readiness (coreDNS running)"
i=0
while [ "$i" -lt 90 ]; do
  cni=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath={.items[0].status.phase} 2>/dev/null" || true)
  cidr=$(docker exec "$DIND_CID" sh -c "k3s kubectl get nodes -o jsonpath={.items[0].spec.podCIDR} 2>/dev/null" || true)
  [ "$cni" = "Running" ] && [ -n "$cidr" ] && [ "$cidr" != " " ] && break
  i=$((i + 1)); sleep 2
done
[ "$cni" = "Running" ] || fail "CNI did not become ready"

# 运行时预热（native 形态首解包窗；fuse 秒级即过）。
log "warming container runtime"
docker exec "$DIND_CID" sh -c '
  k3s kubectl run warm-a --image=nginx:1.27 --restart=Never --command -- true >/dev/null 2>&1 || true
  i=0
  while [ $i -lt 120 ]; do
    done_a=$(k3s kubectl get pod warm-a -o jsonpath={.status.phase} 2>/dev/null || echo Pending)
    case "$done_a" in Succeeded|Failed) exit 0;; esac
    i=$((i+1)); sleep 2
  done
'
docker exec "$DIND_CID" sh -c 'k3s kubectl delete pod warm-a --force --grace-period=0 >/dev/null 2>&1 || true'

DIND_IP=$(docker inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" "$DIND_CID")
log "starting fleetlyd (runtime.provider=k3s)"
docker exec "$DIND_CID" mkdir -p /root/bins
docker exec -i "$DIND_CID" sh -c 'cat > /root/bins/fleetlyd && chmod +x /root/bins/fleetlyd' < "$WORKDIR/bins/fleetlyd"
docker exec -i "$DIND_CID" sh -c 'cat > /root/bins/fleetly && chmod +x /root/bins/fleetly' < "$WORKDIR/bins/fleetly"
docker exec "$DIND_CID" sh -c \
  "mkdir -p /var/lib/fleetly && setsid env FLEETLY_DATA_ROOT=/var/lib/fleetly FLEETLY_RUNTIME_PROVIDER=k3s FLEETLY_RUNTIME_K3S_KUBECONFIG=/etc/rancher/k3s/k3s.yaml FLEETLY_PROXY_CONFIG_ENDPOINT=http://$DIND_IP:9082/proxy/config /root/bins/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &"
i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$DIND_CID" sh -c 'FLEETLY_ADDR=127.0.0.1:9080 /root/bins/fleetly status >/dev/null 2>&1'; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
[ "$i" -lt 60 ] || { docker exec "$DIND_CID" tail -30 /var/log/fleetlyd.log >&2; fail "fleetlyd did not become ready"; }
log "fleetlyd ready"

# 6. 身份链。
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
cli whoami >/dev/null
log "identity chain green"

# 7. enroll 材料 → worker join（advertise 地址解析的实机锚：kubeconfig 是
#    127.0.0.1 形态，join 命令必须携带 manager 容器 IP）。
log "joining the worker via 'fleetly nodes enroll' material (advertise address)"
JOIN_CMD=$(cli --json nodes enroll | sed -n 's/.*"join_command": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$JOIN_CMD" ]; then
  JOIN_CMD=$(cli nodes enroll | grep -o 'k3s agent --server [^ ]* --token [^ ]*')
fi
[ -n "$JOIN_CMD" ] || { cli --json nodes enroll >&2 || true; fail "nodes enroll produced no join command"; }
case "$JOIN_CMD" in
  *"--server https://$DIND_IP:"*) log "join command carries the manager advertise address ($DIND_IP)" ;;
  *) echo "join command server address is not the manager IP: $JOIN_CMD" >&2
     docker exec "$DIND_CID" sh -c "k3s kubectl get nodes -o wide" >&2 || true
     fail "advertise address resolution broke" ;;
esac

log "starting worker dind container"
# --hostname k3s-tw-w:agent 的缺省 node-name 取 hostname——就绪断言与落点
# 断言都用该名(enroll 材料原样执行,不得追加旗标——节点名经环境定形)。
WRK_CID=$(eval docker run -d --privileged --name fleetly-e2e-k3stw-w-"$$" \
  --cgroupns=host \
  --hostname k3s-tw-w \
  --network "$DIND_NET" \
  $FUSE_DEV \
  $PROXY_ENV \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)
log "waiting for worker dind daemon"
wait_docker "$WRK_CID" worker
if [ "$K3S_SNAPSHOTTER" = "fuse" ]; then
  install_fuse_apks "$WRK_CID"
fi
# worker 预载：k3s 二进制 + airgap tar + 应用镜像（agent 起动自动 import；
# 起动前注入——docker cp 在 agent 起动后受 mount 遮蔽，统一走 exec stdin）。
# snapshotter 经 k3s config.yaml 注入（环境面——join 材料原样执行不得追加
# 旗标；agent 缺省 overlayfs 在 dind 被内核拒，run1 实证 agent 卡在
# "Waiting to retrieve agent configuration" 本地快照器校验环）。
log "staging worker (k3s binary + airgap images + snapshotter config)"
docker exec "$WRK_CID" mkdir -p /var/lib/rancher/k3s/agent/images /etc/rancher/k3s /usr/local/bin /root
docker exec "$WRK_CID" sh -c "printf 'snapshotter: %s\n' '$K3S_SNAPSHOTTER_FLAG' > /etc/rancher/k3s/config.yaml"
docker exec -i "$WRK_CID" sh -c 'cat > /usr/local/bin/k3s && chmod +x /usr/local/bin/k3s' < "$WORKDIR/k3s"
docker exec -i "$WRK_CID" sh -c 'cat > /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar' < "$WORKDIR/k3s-airgap.tar"
for img in nginx:1.27 busybox:1.37; do
  name=$(printf '%s' "$img" | sed 's#/#-#g')
  docker image save "$img" | docker exec -i "$WRK_CID" sh -c "cat > /var/lib/rancher/k3s/agent/images/app-$name.tar"
done

# enroll 材料原样执行（worker 上 k3s agent 直跑——节点零平台安装物）。
log "executing join command on the worker"
docker exec -d "$WRK_CID" sh -c "$JOIN_CMD >/var/log/k3s-agent.log 2>&1"

# 就绪探针：kubectl 表格 STATUS 列（宿主侧 awk 取列）——jsonpath 的
# [?(@.type==…)] 谓词带括号，内层 sh -c 双引号串会让 dash 语法炸
#（run2 实证 "unexpected ("）。
i=0
while [ "$i" -lt 90 ]; do
  ready=$(docker exec "$DIND_CID" sh -c "k3s kubectl get node k3s-tw-w --no-headers" 2>/dev/null | awk '{print $2}' || true)
  [ "$ready" = "Ready" ] && break
  i=$((i + 1)); sleep 2
done
if [ "$i" -ge 90 ]; then
  docker exec "$WRK_CID" tail -30 /var/log/k3s-agent.log >&2 || true
  docker exec "$DIND_CID" sh -c 'k3s kubectl get nodes -o wide; k3s kubectl get events --sort-by=.lastTimestamp | tail -10' >&2 || true
  fail "worker node never became Ready"
fi
log "worker node Ready (joined via enrollment material)"

# 8. 平台锚定断言（第二节点进锚定表——NodeJoined 铸造新平台 ID）。
i=0
while [ "$i" -lt 60 ]; do
  n=$(cli --json nodes list | grep -c '"platform_id"' || true)
  [ "$n" -ge 2 ] && break
  i=$((i + 1)); sleep 2
done
[ "$n" -ge 2 ] || { cli --json nodes list >&2 || true; fail "platform never saw both nodes"; }
log "both nodes anchored in the platform"

# 9. relay_online 双节点（exec 集中形态：per-node 回环注册——worker 无任何
#    平台代理物，其 exec 可服务性来自 manager 侧注册）。
i=0
while [ "$i" -lt 60 ]; do
  n=$(cli --json nodes list | grep -c '"relay_online": *true' || true)
  [ "$n" -ge 2 ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 60 ] || {
  docker exec "$DIND_CID" sh -c "grep -i relay /var/log/fleetlyd.log | tail -10" >&2 || true
  cli --json nodes list >&2 || true
  fail "both nodes must be relay_online (central loopback agents)"
}
log "both nodes relay_online (central form, zero worker-side platform agents)"

# 10. 卷钉住 worker 落点 + worker pod exec 全链。
log "deploying a volume-pinned workload onto the worker"
cli projects create twshop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" web >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)

# worker 的平台节点 ID（nodes list 里 role=worker 的一行;protojson 字段序
# platform_id 先于 role 三行——-B6 开窗,单行 sed 永不匹配的既定坑）。
WORKER_NODE_ID=$(cli --json nodes list | grep -B6 -A2 '"role": *"worker"' | sed -n 's/.*"platform_id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$WORKER_NODE_ID" ] || { cli --json nodes list >&2; fail "could not resolve worker platform node id"; }
cli volumes create --project "$PROJECT_ID" --node "$WORKER_NODE_ID" workervol >/dev/null

cat > "$WORKDIR/tw-compose.yaml" <<'EOF'
services:
  store:
    image: nginx:1.27
    ports: ["80"]
    volumes: ["workervol:/usr/share/nginx/html"]
volumes:
  workervol: {}
EOF
docker exec -i "$DIND_CID" sh -c 'cat > /tmp/tw-compose.yaml' < "$WORKDIR/tw-compose.yaml"
DEP_ID=$(cli --json deploy --app "$APP_ID" --compose-file /tmp/tw-compose.yaml | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$DEP_ID" ] || fail "compose deploy failed"

wait_state() {
  want="$1"; i=0
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$APP_ID" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$APP_ID" >&2 || true
        docker exec "$DIND_CID" sh -c "k3s kubectl get pods -A -o wide 2>&1 | head -15" >&2 || true
        docker exec "$DIND_CID" sh -c "grep -v 'gRPC request' /var/log/fleetlyd.log | tail -25" >&2 || true
        fail "deployment reached $state before $want"
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  fail "timed out waiting for $want"
}
wait_state succeeded
log "deployment succeeded (volume pinned to worker)"

# 落点断言:store pod 必须在 worker 节点上(nodeSelector fleetly.node.id)。
P_NS="fleetly-$(printf '%s' "$PROJECT_ID" | tr 'A-Z' 'a-z')"
i=0
while [ "$i" -lt 90 ]; do
  landing=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P_NS --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.spec.nodeName}{\"\\n\"}{end}' 2>/dev/null" | grep -x k3s-tw-w || true)
  [ -n "$landing" ] && break
  i=$((i + 1)); sleep 2
done
[ -n "$landing" ] || {
  docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P_NS -o wide 2>&1" >&2 || true
  fail "store pod did not land on the worker node (volume pinning broke)"
}
log "store pod landed on worker node k3s-tw-w"

# worker pod 的 exec 全链(集中形态核心断言:apiserver→worker kubelet SPDY)。
EXEC_OUT=$(cli exec "$APP_ID/store" -- /bin/echo worker-exec-ok 2>&1) \
  || fail "fleetly exec into worker pod failed: $EXEC_OUT"
case "$EXEC_OUT" in
  *worker-exec-ok*) log "exec reached the worker-landing pod (central form cross-node)" ;;
  *) fail "worker exec output missing marker, got: $EXEC_OUT" ;;
esac
rc=0
cli exec "$APP_ID/store" -- /bin/sh -c 'exit 7' >/dev/null 2>&1 || rc=$?
[ "$rc" = "7" ] || fail "worker exec exit code passthrough failed (expected 7, got $rc)"
log "worker exec exit code passthrough green (7)"

echo ""
echo "K3S TWO-NODE E2E PASSED"
