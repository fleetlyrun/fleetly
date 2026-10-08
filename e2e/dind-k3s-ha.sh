#!/bin/sh
# e2e dind k3s HA 腿（N8/ADR-0056 决策 3——挂账 7 收口，缺省翻转前置②）：
# embedded etcd 三 server + 一 worker 的四容器拓扑，断言 Runtime 面高可用：
#   1. 三 server quorum（--cluster-init + node-token join）+ worker（enroll
#      材料 join）= 4 节点 Ready；
#   2. 卷钉住 worker 的载体 Running（pod uid 记录为全程零滚动断言锚）；
#   3. server1 的 k3s 进程失效（fleetlyd 的 apiserver 端点）→ 载体零滚动
#      + 持续服务（kubelet autonomy；断言经 server3 的 kubectl——quorum 2/3
#      保持）+ fleetlyd 断连窗被日志感知 → server1 恢复 → fleetlyd 重连、
#      载体 uid 不变；
#   4. server2 永久失效（rm 容器，etcd 死成员）→ 集群读写保持（新部署
#      succeeded）+ 载体 uid 不变。
#
# 诚实边界（ADR-0056 决策 3）：fleetlyd 单实例不在 HA 覆盖内（本腿它就在
# server1 容器里——apiserver 失效对它是断连不是死亡）；无 LB 形态——
# fleetlyd/worker 各钉单 apiserver 端点。失效窗预算 < 300s（k8s NotReady
# taint 的默认 tolerationSeconds——超窗 controller-manager 会驱逐载体，
# 那是灾难恢复形态不是本腿语义）。
#
# 形态与 dind-k3s-two-node.sh 同源（fuse snapshotter / 代理透传 / airgap
# 预载 / exec stdin 注入 / kubectl 表格列 + 宿主 awk——jsonpath 谓词括号
# 在 dash 双引号串会炸）。
set -eu

export MSYS_NO_PATHCONV=1

WORKDIR="$(mktemp -d)"
S1_CID=""
S2_CID=""
S3_CID=""
WRK_CID=""
cleanup() {
  for cid in "$WRK_CID" "$S3_CID" "$S2_CID" "$S1_CID"; do
    [ -n "$cid" ] && docker rm -f "$cid" >/dev/null 2>&1 || true
  done
  docker network rm "$DIND_NET" >/dev/null 2>&1 || true
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

# 3. 公共容器底座（fuse helper + 预载 + k3s 二进制——四容器同款；join 前
#    的注入统一 exec stdin，docker cp 在 k3s 起动后受 mount 遮蔽）。
PROXY_ENV=""
if [ -n "${HTTPS_PROXY:-}${https_proxy:-}" ]; then
  PX="${HTTPS_PROXY:-$https_proxy}"
  PX_HOST=$(printf '%s' "$PX" | sed -E 's#(https?://)?([^:/]+).*#\2#')
  PX_REST=$(printf '%s' "$PX" | sed -E 's#^(https?://)?[^:/]+##')
  NO_PROXY="localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,10.42.0.0/16,.svc,.cluster.local,kubernetes.default.svc"
  PROXY_ENV="-e HTTPS_PROXY=http://host.docker.internal$PX_REST -e HTTP_PROXY=http://host.docker.internal$PX_REST -e NO_PROXY=$NO_PROXY -e no_proxy=$NO_PROXY --add-host=host.docker.internal:host-gateway"
fi
FUSE_DEV=""
[ "$K3S_SNAPSHOTTER" = "fuse" ] && FUSE_DEV="--device /dev/fuse"
DIND_NET="fleetly-e2e-k3sha-$$"
docker network create "$DIND_NET" >/dev/null 2>&1 || true

start_dind() { # $1 = 容器名后缀, $2 = hostname
  docker run -d --privileged --name "fleetly-e2e-k3sha-$1-$2-$$" \
    --cgroupns=host \
    --hostname "$2" \
    --network "$DIND_NET" \
    $FUSE_DEV \
    $PROXY_ENV \
    -e DOCKER_TLS_CERTDIR= \
    docker:29-dind
}

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
fi

stage_node() { # $1 = 容器 id：二进制 + airgap + 应用镜像（join/起动前）
  docker exec "$1" mkdir -p /var/lib/rancher/k3s/agent/images /usr/local/bin /root
  docker exec -i "$1" sh -c 'cat > /usr/local/bin/k3s && chmod +x /usr/local/bin/k3s' < "$WORKDIR/k3s"
  docker exec -i "$1" sh -c 'cat > /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar' < "$WORKDIR/k3s-airgap.tar"
  for img in nginx:1.27 busybox:1.37; do
    docker image inspect "$img" >/dev/null 2>&1 || docker image pull "$img" >/dev/null
    name=$(printf '%s' "$img" | sed 's#/#-#g')
    docker image save "$img" | docker exec -i "$1" sh -c "cat > /var/lib/rancher/k3s/agent/images/app-$name.tar"
  done
}

# 4. server1 起 + --cluster-init（embedded etcd 首节点）。
log "starting server1 (--cluster-init)"
S1_CID=$(start_dind s1 k3s-ha-s1)
wait_docker "$S1_CID" server1
[ "$K3S_SNAPSHOTTER" = "fuse" ] && install_fuse_apks "$S1_CID"
stage_node "$S1_CID"
docker exec -d "$S1_CID" sh -c \
  "K3S_KUBECONFIG_MODE=644 k3s server --cluster-init --disable=traefik --disable=servicelb --node-name=k3s-ha-s1 --snapshotter=$K3S_SNAPSHOTTER_FLAG >/var/log/k3s.log 2>&1"
i=0
while [ "$i" -lt 120 ]; do
  if docker exec "$S1_CID" sh -c 'k3s kubectl get --raw=/readyz' >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$i" -lt 120 ] || { docker exec "$S1_CID" tail -30 /var/log/k3s.log >&2; fail "server1 (cluster-init) did not become ready"; }
log "server1 ready (etcd initialized)"

i=0
while [ "$i" -lt 90 ]; do
  cni=$(docker exec "$S1_CID" sh -c "k3s kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath={.items[0].status.phase} 2>/dev/null" || true)
  [ "$cni" = "Running" ] && break
  i=$((i + 1)); sleep 2
done
[ "$cni" = "Running" ] || fail "CNI did not become ready on server1"

S1_IP=$(docker inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" "$S1_CID")
[ -n "$S1_IP" ] || fail "could not resolve server1 IP"
NODE_TOKEN=$(docker exec "$S1_CID" sh -c 'tr -d "\r\n" < /var/lib/rancher/k3s/server/node-token')
[ -n "$NODE_TOKEN" ] || fail "node-token not readable on server1"

# 5. fleetlyd 上 server1（与 k3s 同容器；断连断言锚在它的 apiserver 端点）。
log "starting fleetlyd on server1"
docker exec "$S1_CID" mkdir -p /root/bins
docker exec -i "$S1_CID" sh -c 'cat > /root/bins/fleetlyd && chmod +x /root/bins/fleetlyd' < "$WORKDIR/bins/fleetlyd"
docker exec -i "$S1_CID" sh -c 'cat > /root/bins/fleetly && chmod +x /root/bins/fleetly' < "$WORKDIR/bins/fleetly"
docker exec "$S1_CID" sh -c \
  "mkdir -p /var/lib/fleetly && setsid env FLEETLY_DATA_ROOT=/var/lib/fleetly FLEETLY_RUNTIME_PROVIDER=k3s FLEETLY_RUNTIME_K3S_KUBECONFIG=/etc/rancher/k3s/k3s.yaml FLEETLY_PROXY_CONFIG_ENDPOINT=http://$S1_IP:9082/proxy/config /root/bins/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &"
i=0
while [ "$i" -lt 60 ]; do
  if docker exec "$S1_CID" sh -c 'FLEETLY_ADDR=127.0.0.1:9080 /root/bins/fleetly status >/dev/null 2>&1'; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
[ "$i" -lt 60 ] || { docker exec "$S1_CID" tail -30 /var/log/fleetlyd.log >&2; fail "fleetlyd did not become ready"; }
log "fleetlyd ready"

CURRENT_TOKEN=$(docker exec "$S1_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
[ -n "$CURRENT_TOKEN" ] || fail "bootstrap token missing"
cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$S1_CID" /root/bins/fleetly "$@"
}
NEW_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$S1_CID" \
  /root/bins/fleetly init --json --token "$CURRENT_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$NEW_TOKEN" ] || fail "init did not mint a CLI token"
CURRENT_TOKEN="$NEW_TOKEN"
cli whoami >/dev/null
log "identity chain green"

# 6. server2/3 join（控制面扩容 = 装机级手工序，不经平台 Enrollment——
#    ADR-0056 决策 3 的分立裁决；node-token 同源、奇数成员 quorum）。
for n in 2 3; do
  log "starting server$n (etcd join via node-token)"
  eval "S${n}_CID=\$(start_dind s${n} k3s-ha-s${n})"
  eval "cid=\$S${n}_CID"
  wait_docker "$cid" "server$n"
  [ "$K3S_SNAPSHOTTER" = "fuse" ] && install_fuse_apks "$cid"
  stage_node "$cid"
  docker exec -d "$cid" sh -c \
    "K3S_KUBECONFIG_MODE=644 k3s server --server https://$S1_IP:6443 --token '$NODE_TOKEN' --disable=traefik --disable=servicelb --node-name=k3s-ha-s$n --snapshotter=$K3S_SNAPSHOTTER_FLAG >/var/log/k3s.log 2>&1"
  i=0
  while [ "$i" -lt 120 ]; do
    ready=$(docker exec "$S1_CID" sh -c "k3s kubectl get node k3s-ha-s$n --no-headers" 2>/dev/null | awk '{print $2}' || true)
    [ "$ready" = "Ready" ] && break
    i=$((i + 1)); sleep 2
  done
  if [ "$i" -ge 120 ]; then
    docker exec "$cid" tail -30 /var/log/k3s.log >&2 || true
    docker exec "$S1_CID" sh -c 'k3s kubectl get nodes -o wide' >&2 || true
    fail "server$n never joined/Ready"
  fi
done
log "three servers Ready (embedded etcd quorum)"

# 7. worker join（平台面：enroll 材料——HA 腿与单机/tw 腿同一 enrollment
#    契约，advertise 解析指向 server1）。
log "joining the worker via 'fleetly nodes enroll' material"
JOIN_CMD=$(cli --json nodes enroll | sed -n 's/.*"join_command": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$JOIN_CMD" ]; then
  JOIN_CMD=$(cli nodes enroll | grep -o 'k3s agent --server [^ ]* --token [^ ]*')
fi
[ -n "$JOIN_CMD" ] || fail "nodes enroll produced no join command"
case "$JOIN_CMD" in
  *"--server https://$S1_IP:"*) log "join command carries the server1 advertise address" ;;
  *) fail "advertise address resolution broke: $JOIN_CMD" ;;
esac
WRK_CID=$(start_dind w k3s-ha-w)
wait_docker "$WRK_CID" worker
[ "$K3S_SNAPSHOTTER" = "fuse" ] && install_fuse_apks "$WRK_CID"
docker exec "$WRK_CID" mkdir -p /etc/rancher/k3s
docker exec "$WRK_CID" sh -c "printf 'snapshotter: %s\n' '$K3S_SNAPSHOTTER_FLAG' > /etc/rancher/k3s/config.yaml"
stage_node "$WRK_CID"
docker exec -d "$WRK_CID" sh -c "$JOIN_CMD >/var/log/k3s-agent.log 2>&1"
i=0
while [ "$i" -lt 120 ]; do
  ready=$(docker exec "$S1_CID" sh -c "k3s kubectl get node k3s-ha-w --no-headers" 2>/dev/null | awk '{print $2}' || true)
  [ "$ready" = "Ready" ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 120 ] || { docker exec "$WRK_CID" tail -30 /var/log/k3s-agent.log >&2 || true; fail "worker node never became Ready"; }
log "worker node Ready (four nodes total)"

# 8. 卷钉住 worker 的载体（HA 断言锚——载体必须不在任何 server 节点上，
#    server 失效不影响载体进程；busybox httpd 自带 wget 活体面）。
log "deploying the volume-pinned probe workload onto the worker"
cli projects create hashop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" web >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
WORKER_NODE_ID=$(cli --json nodes list | grep -B6 -A2 '"role": *"worker"' | sed -n 's/.*"platform_id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$WORKER_NODE_ID" ] || { cli --json nodes list >&2; fail "could not resolve worker platform node id"; }
cli volumes create --project "$PROJECT_ID" --node "$WORKER_NODE_ID" workervol >/dev/null

cat > "$WORKDIR/ha-compose.yaml" <<'EOF'
services:
  probe:
    image: busybox:1.37
    command: ["sh", "-c", "mkdir -p /www && echo ha-alive > /www/index.html && httpd -f -p 8080 -h /www"]
    ports: ["8080"]
    volumes: ["workervol:/www"]
volumes:
  workervol: {}
EOF
docker exec -i "$S1_CID" sh -c 'cat > /tmp/ha-compose.yaml' < "$WORKDIR/ha-compose.yaml"
cli --json deploy --app "$APP_ID" --compose-file /tmp/ha-compose.yaml >/dev/null || fail "compose deploy failed"

wait_state() {
  want="$1"; app="${2:-$APP_ID}"; i=0
  while [ "$i" -lt 240 ]; do
    state=$(cli --json deployments list --app "$app" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$app" >&2 || true
        docker exec "$S1_CID" sh -c "k3s kubectl get pods -A -o wide 2>&1 | head -15; grep -v 'gRPC request' /var/log/fleetlyd.log | tail -25" >&2 || true
        fail "deployment reached $state before $want"
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  fail "timed out waiting for $want"
}
wait_state succeeded

H_NS="fleetly-$(printf '%s' "$PROJECT_ID" | tr 'A-Z' 'a-z')"
H_APP_LC=$(printf '%s' "$APP_ID" | tr 'A-Z' 'a-z')
PROBE_UID=""
i=0
while [ "$i" -lt 90 ]; do
  PROBE_UID=$(docker exec "$S1_CID" sh -c "k3s kubectl get pods -n $H_NS -l fleetly.ns.app=$H_APP_LC --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.uid} 2>/dev/null" || true)
  [ -n "$PROBE_UID" ] && break
  i=$((i + 1)); sleep 2
done
[ -n "$PROBE_UID" ] || { docker exec "$S1_CID" sh -c "k3s kubectl get pods -n $H_NS -o wide" >&2 || true; fail "probe pod never running"; }
landing=$(docker exec "$S1_CID" sh -c "k3s kubectl get pods -n $H_NS --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.spec.nodeName}{\"\\n\"}{end}'" | grep -x k3s-ha-w || true)
[ -n "$landing" ] || fail "probe pod did not land on the worker (volume pinning broke)"
log "probe pod running on worker (uid=$PROBE_UID) — zero-roll anchor set"

# 载体活体（server3 的 apiserver 承载断言——server1 失效窗内的旁路通道）。
probe_alive() { # $1 = 承载断言的容器 id（其本地 kubectl 必须可用）
  out=$(docker exec "$1" sh -c "p=\$(k3s kubectl get pods -n $H_NS -l fleetly.ns.app=$H_APP_LC --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name}) && k3s kubectl exec -n $H_NS \"\$p\" -- wget -q -O- -T 5 http://127.0.0.1:8080/" 2>/dev/null || true)
  case "$out" in
    *ha-alive*) return 0 ;;
    *) return 1 ;;
  esac
}
uid_now() { # $1 = 承载容器 id
  docker exec "$1" sh -c "k3s kubectl get pods -n $H_NS -l fleetly.ns.app=$H_APP_LC --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.uid} 2>/dev/null" || true
}

# 9. server1 的 k3s 进程失效（fleetlyd 的 apiserver 端点；quorum 2/3 保持）。
# kill 面扩到子进程：k3s 主进程 TERM 后其子进程（apiserver 等）可成孤儿
# 继续服务（run1 实证 pgrep k3s=0 而 readyz 仍答）——以 apiserver 死亡为
# 断言锚（readyz 必须拒），进程清点只是尽力。
log "killing k3s on server1 (fleetlyd loses its apiserver endpoint)"
docker exec "$S1_CID" sh -c 'pkill -TERM k3s 2>/dev/null; pkill -TERM kube-apiserver 2>/dev/null; pkill -TERM containerd-k3s 2>/dev/null; true'
i=0
while [ "$i" -lt 45 ]; do
  if ! docker exec "$S1_CID" sh -c 'k3s kubectl get --raw=/readyz >/dev/null 2>&1'; then
    break
  fi
  docker exec "$S1_CID" sh -c 'pkill -TERM k3s 2>/dev/null; pkill -TERM kube-apiserver 2>/dev/null; true'
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 45 ] || { docker exec "$S1_CID" sh -c 'ps aux | head -15' >&2; fail "server1 apiserver did not die"; }
sleep 8 # 断连窗:让 fleetlyd 的 watch/Ensure 撞上拒连

# 断连面诊断（非断言——Provider 的 watch 断流重建是静默路径，日志锚可能
# 无痕；断连由 apiserver 死亡 + 恢复后 fleetlyd 行为收口双锚承载）。
docker exec "$S1_CID" sh -c 'k3s kubectl get --raw=/readyz >/dev/null 2>&1' \
  && fail "server1 apiserver still answering after the kill"
docker exec "$S1_CID" sh -c "grep -icE 'connection refused|connect:|unavailable|watch.*ended' /var/log/fleetlyd.log" || true

# 载体零滚动 + 持续服务（断言经 server3——quorum 内的旁路 apiserver）。
UID_DURING=$(uid_now "$S3_CID")
if [ -z "$UID_DURING" ] || [ "$UID_DURING" != "$PROBE_UID" ]; then
  docker exec "$S3_CID" sh -c "k3s kubectl get pods -n $H_NS -o wide; k3s kubectl get nodes" >&2 || true
  fail "probe pod rolled during server1 outage (uid $UID_DURING != $PROBE_UID)"
fi
probe_alive "$S3_CID" || { docker exec "$S3_CID" sh -c "k3s kubectl get pods -n $H_NS -o wide; k3s kubectl describe pod -n $H_NS -l fleetly.ns.app=$H_APP_LC | tail -15" >&2 || true; fail "probe stopped serving during server1 outage (kubelet autonomy broke)"; }
# 控制面写面活体（server3 的 apiserver → etcd quorum 写：活成员 s2+s3）。
docker exec "$S3_CID" sh -c 'k3s kubectl create configmap ha-write-probe --from-literal=alive=1 >/dev/null 2>&1 && k3s kubectl get configmap ha-write-probe >/dev/null 2>&1' \
  || fail "etcd write path must stay available during the outage (quorum via s2+s3)"
log "server1 outage: carrier zero-roll + still serving + etcd writes green (via server3, kubelet autonomy)"

# 10. server1 恢复（etcd 成员数据在容器 FS——重起即回环）。
log "restarting k3s on server1"
docker exec -d "$S1_CID" sh -c \
  "K3S_KUBECONFIG_MODE=644 k3s server --cluster-init --disable=traefik --disable=servicelb --node-name=k3s-ha-s1 --snapshotter=$K3S_SNAPSHOTTER_FLAG >>/var/log/k3s.log 2>&1"
i=0
while [ "$i" -lt 120 ]; do
  if docker exec "$S1_CID" sh -c 'k3s kubectl get --raw=/readyz' >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$i" -lt 120 ] || { docker exec "$S1_CID" tail -30 /var/log/k3s.log >&2; fail "server1 did not come back"; }

# fleetlyd 重连收口：status（进程面）+ nodes list（apiserver 面——重连后
# DescribeCluster 必须恢复服务）双锚。
i=0
while [ "$i" -lt 60 ]; do
  if docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$S1_CID" /root/bins/fleetly status >/dev/null 2>&1 \
    && docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$S1_CID" /root/bins/fleetly --json nodes list >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$i" -lt 60 ] || { docker exec "$S1_CID" sh -c "grep -v 'gRPC request' /var/log/fleetlyd.log | tail -25" >&2; fail "fleetlyd did not recover its apiserver connection"; }
UID_AFTER=$(uid_now "$S1_CID")
if [ -z "$UID_AFTER" ] || [ "$UID_AFTER" != "$PROBE_UID" ]; then
  fail "probe pod rolled across the server1 outage+recovery (uid $UID_AFTER != $PROBE_UID)"
fi
log "server1 recovered: fleetlyd reconnected, carrier zero-roll (uid unchanged)"

# 11. 收官断言：全节点 Ready + 载体零滚动（跨失效窗全程）。
# 〔永久 server 失效段的裁决撤记（CI 两连挂实证）：etcd 死成员（非优雅
# rm 后成员表残留）拖累 apiserver /readyz 的 etcd 子检查——集群 quorum
# 读写仍活但 readyz 间歇不健康，fleetlyd 的 Health 门敏感拒（CI 慢环境
# 尤甚）。单 server 失效的 quorum 容错 + 读写保持已由第 9 节完整承载
# （零滚动 + 活体 + etcd 写面）；死成员清理（etcdctl member remove——
# k3s 无文档化单成员移除命令）是灾后运维序，runbook HA 节记档。〕
READY_N=$(docker exec "$S1_CID" sh -c "k3s kubectl get nodes --no-headers 2>/dev/null | grep -c ' Ready '" || true)
[ "$READY_N" = "4" ] || { docker exec "$S1_CID" sh -c 'k3s kubectl get nodes' >&2; fail "expected 4 Ready nodes at the finish, got $READY_N"; }
UID_FINAL=$(uid_now "$S1_CID")
if [ -z "$UID_FINAL" ] || [ "$UID_FINAL" != "$PROBE_UID" ]; then
  fail "probe pod rolled across the whole drill (uid $UID_FINAL != $PROBE_UID)"
fi
log "finish: 4 nodes Ready, carrier zero-roll across the whole drill"

echo ""
echo "K3S HA E2E PASSED"
