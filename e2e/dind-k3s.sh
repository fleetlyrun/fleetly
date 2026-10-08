#!/bin/sh
# e2e dind k3s（F4.1/ADR-0052）：第二运行时全链冒烟——dind 容器内 k3s
# 二进制直跑（--disable=traefik --disable=servicelb --snapshotter=native，
# 预研实证形态）→ fleetlyd（runtime.provider=k3s）手起（不走 install.sh——
# 其 swarm init 步骤与 k3s 腿无关）→ 身份链 → app deploy(image) →
# succeeded → k3s 载体断言（Deployment/Service/addressing 名）→ rollback →
# egress:none 强隔离活体实证（跨 ns 拒 + DNS 放行 + 载体标记 + netpol 在场）
# → 网络成员资格入站隔离活体矩阵（ADR-0054：同网通/跨 ns 拒/task 组拒/
# 跨挂通 + peer declare→approve→通→revoke→隔离收敛全环）
# → Database（postgres digest 在线拉 + PVC local-path 绑定）→ 受管 traefik
# （hostPort 80）+ Route 明文端到端。
#
# snapshotter 形态（ADR-0052 决策 8 补录，FLEETLY_E2E_K3S_SNAPSHOTTER 选）：
#   native（缺省）dind 内核拒 nested overlay 的保底形态（WSL2 实证）；首次
#                 解包逐层全拷 ~2 分钟/镜像（预热段承载），载体就绪迟滞分钟级
#   overlayfs     CI 形态（GH Actions runner 原生文件系统，nested overlay 可用）
#   fuse          本机加速形态（fuse-overlayfs 用户态 CoW，实证首容器 2s）——
#                 需 /dev/fuse 直通 + Alpine apk 预载 mount.fuse3 helper
#
# 前置：本机 docker 可用；宿主 HTTPS_PROXY 可选（在线段 = db digest 拉取，
# 存在时透传进 dind——CI 直连环境无代理同样可行）。
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

# k3s 钉版（ADR-0052 决策 8：平台常量单源 + sha256 校验；守卫
# TestK3sPinConstantAndE2EAgree 静态断言本处一致）。
K3S_VERSION="v1.36.5+k3s1"
K3S_SHA256="d73847bcd3c5fccef0115b372e2f9a91f3032dc84bbf71518a4617565294d313"

# snapshotter 选型（见头注三形态；fuse 传入 k3s 的 containerd snapshotter
# 名是 fuse-overlayfs）。
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

# 1. 交叉编译二进制（linux/amd64；纯 Go 零 cgo）。h2cserver 是 from_build
#    段的上传源产物（scratch 基础镜像零外网依赖，h2c e2e 先例同款）。
log "cross-compiling fleetlyd + fleetly + h2cserver (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cserver" ./e2e/h2cserver

# 2. 下载 k3s（版本钉定 + sha256 校验）与 airgap 系统镜像 tar。
log "downloading k3s $K3S_VERSION (pinned, sha256-verified)"
# URL 路径段 + 需转义 %2B；sh 无 bash 参数展开——sed 通道。
K3S_ASSET=$(printf '%s' "$K3S_VERSION" | sed 's/+/%2B/')
# 下载缓存（FLEETLY_E2E_K3S_CACHE 缺省 ~/.cache/fleetly-e2e）：二进制
# 缓存命中仍过 sha256；airgap tar 存在即用（完整性由 k3s import 期
# 检验）——重复跑 e2e 不重下 ~280MB。
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

# 3. 起 dind（privileged + --cgroupns=host，smoke 同款）；宿主代理在场则
#    透传（在线段 = db digest 拉取；127.0.0.1 形态改写 host-gateway）。
log "starting dind container"
PROXY_ENV=""
if [ -n "${HTTPS_PROXY:-}${https_proxy:-}" ]; then
  PX="${HTTPS_PROXY:-$https_proxy}"
  PX_HOST=$(printf '%s' "$PX" | sed -E 's#(https?://)?([^:/]+).*#\2#')
  PX_REST=$(printf '%s' "$PX" | sed -E 's#^(https?://)?[^:/]+##')
  # NO_PROXY 必须显式（实证坑：无 no_proxy 时 k3s/fleetlyd 的 localhost:6443 与集群内
  # 通信全被代理劫持——kubelet/watch/ensure 诡异慢挂）。
  NO_PROXY="localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,10.42.0.0/16,.svc,.cluster.local,kubernetes.default.svc"
  PROXY_ENV="-e HTTPS_PROXY=http://host.docker.internal$PX_REST -e HTTP_PROXY=http://host.docker.internal$PX_REST -e NO_PROXY=$NO_PROXY -e no_proxy=$NO_PROXY --add-host=host.docker.internal:host-gateway"
fi
FUSE_DEV=""
[ "$K3S_SNAPSHOTTER" = "fuse" ] && FUSE_DEV="--device /dev/fuse"
DIND_CID=$(eval docker run -d --privileged --name fleetly-e2e-k3s-"$$" \
  --cgroupns=host \
  $FUSE_DEV \
  $PROXY_ENV \
  -e DOCKER_TLS_CERTDIR= \
  docker:29-dind)

wait_docker() {
  i=0
  while [ "$i" -lt 60 ]; do
    if docker exec "$DIND_CID" docker info >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  fail "dind daemon did not become ready"
}
log "waiting for dind daemon"
wait_docker

# 3b. 构建链传输信任面（ADR-0056 决策 1/2——from_build 全链的前置）：
#     ① dockerd 侧 insecure-registry drop-in（builder 推送受管 zot 用；
#     install.sh 同款 drop-in，k3s 腿 fleetlyd 手起不经 install.sh 故此处
#     复刻）——必须在 k3s 起动前落好并重启容器（k3s 起在 exec -d 里，容器
#     重启会杀掉它；h2c e2e 先例同款时序）。
#     ② 节点侧 k3s registries.yaml（kubelet 拉取明文 registry 的信任面）
#     在 k3s 起动前落位（起动期渲染 containerd hosts.toml）。
#     DIND_IP 在此刻取并在重启后断言保持（既有 fleetlyd env 同源）。
DIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
[ -n "$DIND_IP" ] || fail "could not resolve the dind container IP"
# mkdir 先行：docker:29-dind 镜像无 /etc/docker（install.sh 形态建它——手起
# 腿复刻）。
docker exec "$DIND_CID" mkdir -p /etc/docker
docker exec "$DIND_CID" sh -c "printf '{\"insecure-registries\":[\"$DIND_IP:5000\"]}\n' > /etc/docker/daemon.json"
log "restarting the dind container to load the insecure-registry drop-in (before k3s starts)"
docker restart "$DIND_CID" >/dev/null
wait_docker
RESTARTED_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
if [ "$RESTARTED_IP" != "$DIND_IP" ]; then
  fail "dind ip changed across restart ($DIND_IP -> $RESTARTED_IP); the baked registry/proxy endpoints would break"
fi
case "$(docker exec "$DIND_CID" docker info 2>/dev/null | grep -c ':5000')" in
  0) fail "insecure-registry drop-in did not take effect (no :5000 entry in docker info)" ;;
  *) log "dockerd trusts the managed registry (build push face)" ;;
esac

# fuse 形态预载段：k3s 的 fuse-overlayfs snapshotter 走内核 mount 通道调
# /sbin/mount.fuse3 + /sbin/mount.fuse-overlayfs（k3s 自带 fuse-overlayfs
# 二进制，helper 与 libfuse3 缺位——首试坑实锤）；dind 容器内 apk 经代理
# 不可用（libfetch 与本环境代理不兼容），宿主 curl 拉 apk 经 stdin 注入
# 离线装（版本随 v3.24 通道钉定，与镜像 tag 同级钉法）。
if [ "$K3S_SNAPSHOTTER" = "fuse" ]; then
  log "installing fuse-overlayfs helpers (four apks)"
  APK_BASE="https://dl-cdn.alpinelinux.org/alpine/v3.24"
  for apk in \
    main/x86_64/fuse-common-3.18.3-r0.apk \
    main/x86_64/fuse3-libs-3.18.3-r0.apk \
    main/x86_64/fuse3-3.18.3-r0.apk \
    community/x86_64/fuse-overlayfs-1.16-r0.apk; do
    name=$(basename "$apk")
    # 下载缓存（k3s 缓存目录同库——alpine CDN 经代理有瞬态抖动，重跑不重下；
    # CI 的 actions cache 路径恰同此目录，双保险）。
    if [ -s "$K3S_CACHE_DIR/$name" ]; then
      cp "$K3S_CACHE_DIR/$name" "$WORKDIR/$name"
    else
      curl -sL --retry 3 -o "$WORKDIR/$name" "$APK_BASE/$apk" || { echo "FATAL: apk fetch failed: $name" >&2; exit 1; }
      cp "$WORKDIR/$name" "$K3S_CACHE_DIR/$name"
    fi
    docker exec -i "$DIND_CID" sh -c "cat > /tmp/$name" < "$WORKDIR/$name"
  done
  docker exec "$DIND_CID" sh -c \
    'cd /tmp && apk add --allow-untrusted ./fuse-common-*.apk ./fuse3-libs-*.apk ./fuse3-*.apk ./fuse-overlayfs-*.apk >/dev/null 2>&1' \
    || { echo "FATAL: fuse helper apk install failed" >&2; exit 1; }
  docker exec "$DIND_CID" sh -c '
    { [ -e /sbin/mount.fuse3 ] || [ -e /usr/sbin/mount.fuse3 ]; } || { echo "mount.fuse3 helper missing after install" >&2; exit 1; }
    command -v fuse-overlayfs >/dev/null 2>&1 || { echo "fuse-overlayfs binary missing after install" >&2; exit 1; }
  ' || { echo "FATAL: fuse helpers not present after apk install" >&2; exit 1; }
fi

# 4. 镜像预载（k3s airgap 官方通道：tar 落 /var/lib/rancher/k3s/agent/images/
#    ——k3s 起动时自动 import，用 server 配置的 snapshotter。预研坑三连：
#    ①overlayfs snapshotter 在 dind 不可用 → server --snapshotter=native；
#    ②ctr images import 客户端缺省 overlayfs（不随 server 配置）→ 显式
#    snapshotter 旗标又撞 unpacker 平台面；③k3s 起动后 docker cp 被 mount
#    遮蔽。airgap 目录 + 起动前 stdin 注入一并绕开全部三条）。
log "staging images for k3s auto-import (airgap channel)"
docker exec "$DIND_CID" mkdir -p /var/lib/rancher/k3s/agent/images
docker exec -i "$DIND_CID" sh -c 'cat > /var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar' < "$WORKDIR/k3s-airgap.tar"
# postgres digest 与 dbtemplate.postgresImageDigest 同源钉版（守卫
# TestPostgresDigestPinAgreement 双向保鲜）——db 段在线拉经 containerd 代理
# 通道不稳（12 分钟窗超时实证），airgap 预载即零在线拉。pull 按 tag@digest
# 验真后回填 tag 再 save：digest-only 的 docker save 产物无 RepoTag，ctr
# airgap 导入 0 张（Imported 0 images 实证）；tag 形态入库后 kubelet 按
# name 命中本地（digest 同体——拉取即验过）。
PG_DIGEST="sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652"
docker image inspect "postgres@$PG_DIGEST" >/dev/null 2>&1 \
  || docker image pull "postgres:17-bookworm@$PG_DIGEST" >/dev/null
docker image tag "postgres@$PG_DIGEST" postgres:17-bookworm >/dev/null 2>&1 || true
i=0
# 受管栈全量镜像（与 staging 生产形态对齐——此前 e2e 只配 Proxy 端点，受管
# 栈实际只部署 traefik，zot/VL/VM/cadvisor 从未在 e2e 过面；staging k3s
# 落地实证咬出的覆盖缺口，ADR-0055 实录）。镜像钉版 = Provider 常量单一
# 真源（zot/VL/VM/cadvisor 各 provider 的 Image 常量）。
for img in nginx:1.27 busybox:1.37 traefik:v3.5.4 postgres:17-bookworm \
  ghcr.io/project-zot/zot-minimal:v2.1.21 \
  victoriametrics/victoria-logs:v1.52.0 \
  victoriametrics/victoria-metrics:v1.152.0 \
  gcr.io/cadvisor/cadvisor:v0.55.1; do
  docker image inspect "$img" >/dev/null 2>&1 || docker image pull "$img" >/dev/null
  name=$(printf '%s' "$img" | sed 's#/#-#g')
  docker image save "$img" | docker exec -i "$DIND_CID" sh -c "cat > /var/lib/rancher/k3s/agent/images/app-$name.tar"
  i=$((i + 1))
done

# 5. 注入 k3s 并起动（snapshotter 形态见头注三选一）。registries.yaml 先行
#    落位（k3s 起动期渲染 containerd hosts.toml——kubelet 拉受管 zot 明文
#    registry 的节点级信任面，ADR-0056 决策 1；mirror 键 = 镜像引用 host，
#    不改写 host 名——认证面恒走 pod 级 imagePullSecrets，两层正交）。
log "staging k3s registries.yaml (plain-HTTP mirror for the managed registry)"
docker exec "$DIND_CID" mkdir -p /etc/rancher/k3s
docker exec -i "$DIND_CID" sh -c "cat > /etc/rancher/k3s/registries.yaml" <<EOF
mirrors:
  "$DIND_IP:5000":
    endpoint:
      - "http://$DIND_IP:5000"
EOF
docker exec "$DIND_CID" grep -q "endpoint" /etc/rancher/k3s/registries.yaml \
  || fail "registries.yaml staging failed"
log "injecting and starting k3s server"
docker cp "$WORKDIR/k3s" "$DIND_CID":/usr/local/bin/k3s
docker exec "$DIND_CID" chmod +x /usr/local/bin/k3s
docker exec -d "$DIND_CID" sh -c \
  "K3S_KUBECONFIG_MODE=644 k3s server --disable=traefik --disable=servicelb --node-name=k3s-e2e-0 --snapshotter=$K3S_SNAPSHOTTER_FLAG >/var/log/k3s.log 2>&1"
i=0
while [ "$i" -lt 90 ]; do
  if docker exec "$DIND_CID" sh -c 'k3s kubectl get --raw=/readyz' >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
[ "$i" -lt 90 ] || { docker exec "$DIND_CID" tail -30 /var/log/k3s.log >&2; fail "k3s did not become ready"; }
# CNI 就绪窗（实证坑：k3s /readyz ≠ pod 网络就绪——flannel 初始化
# ~1-2 分钟，立刻部署会让 pod 滞留 ContainerCreating 吃掉 L1 窗；以
# coreDNS Running 为 CNI 活体锚）。
log "waiting for CNI readiness (coreDNS running)"
i=0
while [ "$i" -lt 90 ]; do
  cni=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath={.items[0].status.phase} 2>/dev/null" || true)
  cidr=$(docker exec "$DIND_CID" sh -c "k3s kubectl get nodes -o jsonpath={.items[0].spec.podCIDR} 2>/dev/null" || true)
  [ "$cni" = "Running" ] && [ -n "$cidr" ] && [ "$cidr" != " " ] && break
  i=$((i + 1)); sleep 2
done
[ "$cni" = "Running" ] && [ -n "$cidr" ] || { docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n kube-system; k3s kubectl get nodes -o wide" >&2; fail "CNI did not become ready (coreDNS/podCIDR stuck)"; }
log "k3s ready (CNI up)"

# 运行时预热（实证坑：native snapshotter 在 dind overlay 上首次解包应用
# 镜像 ~2 分钟（unpacking will be sequential——逐层完整拷贝），kubelet 的
# 2 分钟 CreateContainer 超时先于解包完成即 CreateContainerError；部署前
# 用一次性 pod 把解包成本花掉，正式部署命中已解包层秒起）。
log "warming container runtime (first-unpack of app images)"
docker exec "$DIND_CID" sh -c '
  k3s kubectl run warm-a --image=busybox:1.37 --restart=Never --command -- true >/dev/null 2>&1 || true
  k3s kubectl run warm-b --image=nginx:1.27 --restart=Never --command -- true >/dev/null 2>&1 || true
  i=0
  while [ $i -lt 120 ]; do
    done_a=$(k3s kubectl get pod warm-a -o jsonpath={.status.phase} 2>/dev/null || echo Pending)
    done_b=$(k3s kubectl get pod warm-b -o jsonpath={.status.phase} 2>/dev/null || echo Pending)
    case "$done_a$done_b" in
      SucceededSucceeded|SucceededFailed|FailedSucceeded|FailedFailed) exit 0;;
    esac
    i=$((i+1)); sleep 2
  done
'
docker exec "$DIND_CID" sh -c 'k3s kubectl delete pod warm-a warm-b --force --grace-period=0 >/dev/null 2>&1 || true'
log "runtime warmed"

# db digest 预拉重试环（坑四连：①模板钉的是多架构 index digest（639ab7ce…），
# docker save 的 tar 只含 amd64 manifest——airgap 导入挂的是 manifest digest
# （13e49e17…），kubelet 按 index digest 拉不命中本地；②ctr images pull
# 客户端解包撞 "no unpack platforms defined"（坑录 #2，--platform 也无用）；
# ③裸名/tag@digest 的 ref ctr 解析即 "invalid port"——须 FQ digest 形态；
# ④经代理单次拉取会挂起。正解 = ctr content fetch：纯取内容不解包（解包
# 留给 kubelet 触发的服务端 CRI 拉，用 server 配置的 snapshotter），content
# store 断点续传，bounded timeout + 重试环磨完，之后 kubelet 零网络命中）。
log "pre-pulling postgres digest (retry loop through flaky proxy)"
docker exec -e PG_REF="docker.io/library/postgres@$PG_DIGEST" "$DIND_CID" sh -c '
  i=0; while [ $i -lt 40 ]; do
    timeout 90 k3s ctr content fetch --platform linux/amd64 "$PG_REF" >/dev/null 2>&1 && exit 0
    i=$((i+1)); sleep 5
  done
  echo "postgres pre-pull did not succeed after $i attempts" >&2; exit 1' \
  || fail "postgres digest pre-pull failed"

# 6. fleetlyd 手起（k3s Provider；不经 install.sh 的 swarm init 形态）。
#    二进制经 exec stdin 注入（k3s 起动后 docker cp 被 mount 遮蔽——同上）。
log "starting fleetlyd (runtime.provider=k3s)"
docker exec "$DIND_CID" mkdir -p /root/bins
docker exec -i "$DIND_CID" sh -c 'cat > /root/bins/fleetlyd && chmod +x /root/bins/fleetlyd' < "$WORKDIR/bins/fleetlyd"
docker exec -i "$DIND_CID" sh -c 'cat > /root/bins/fleetly && chmod +x /root/bins/fleetly' < "$WORKDIR/bins/fleetly"
# 受管 traefik 的 providers.http 配置端点（h2c e2e 先例：受管 Proxy 需
# FLEETLY_PROXY_CONFIG_ENDPOINT 指向控制面 :9082——缺它即 "proxy provider
# unavailable; route publishing disabled"，受管域零部署，run16 实证）。
# dind 容器 IP：traefik pod 从集群网经节点回连控制面用。
DIND_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$DIND_CID")
docker exec "$DIND_CID" sh -c \
  "mkdir -p /var/lib/fleetly && setsid env FLEETLY_DATA_ROOT=/var/lib/fleetly FLEETLY_RUNTIME_PROVIDER=k3s FLEETLY_RUNTIME_K3S_KUBECONFIG=/etc/rancher/k3s/k3s.yaml FLEETLY_PROXY_CONFIG_ENDPOINT=http://$DIND_IP:9082/proxy/config FLEETLY_REGISTRY_ADDR=$DIND_IP:5000 FLEETLY_LOGGING_ADDR=$DIND_IP:9428 FLEETLY_METRICS_ADDR=$DIND_IP:8428 /root/bins/fleetlyd >>/var/log/fleetlyd.log 2>&1 </dev/null &"
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

# 6b. RBAC 最小权限断言（ADR-0053 决策 4，ADR-0052 挂账 6）：fleetlyd 起
#     动自举专用 SA——工作身份必须非 admin：在册动词 yes、越权动词 no
#     （断言打 k3s admin 面 impersonate 该 SA；充分性由本腿全链承载——
#     全部载体操作都在该身份下完成）。
log "verifying least-privilege service account (fleetly-manager)"
docker exec "$DIND_CID" sh -c '
  sa=system:serviceaccount:fleetly-system:fleetly-manager
  [ "$(k3s kubectl auth can-i create deployments.apps --as=$sa 2>/dev/null)" = "yes" ] || { echo "SA must be allowed to create deployments" >&2; exit 1; }
  [ "$(k3s kubectl auth can-i get pods/log --as=$sa 2>/dev/null)" = "yes" ] || { echo "SA must be allowed to get pod logs (subresource name is pods/log, singular; a plural-form rule silently grants nothing — staging k3s live finding, ADR-0055)" >&2; exit 1; }
  [ "$(k3s kubectl auth can-i list networkpolicies.networking.k8s.io --as=$sa 2>/dev/null)" = "yes" ] || { echo "SA must be allowed to list network policies (membership isolation convergence, ADR-0054)" >&2; exit 1; }
  [ "$(k3s kubectl auth can-i create clusterroles.rbac.authorization.k8s.io --as=$sa 2>/dev/null)" = "no" ] || { echo "SA must NOT be allowed to create cluster roles" >&2; exit 1; }
  [ "$(k3s kubectl auth can-i delete namespaces --as=$sa 2>/dev/null)" = "yes" ] || { echo "SA must be allowed to delete namespaces (drained-namespace teardown, ADR-0056)" >&2; exit 1; }
  [ "$(k3s kubectl auth can-i delete nodes --as=$sa 2>/dev/null)" = "no" ] || { echo "SA must NOT be allowed to delete nodes" >&2; exit 1; }
  k3s kubectl get clusterrole fleetly-manager >/dev/null 2>&1 || { echo "fleetly-manager ClusterRole missing" >&2; exit 1; }
' || fail "RBAC least-privilege convergence failed"
log "RBAC green (in-role yes, out-of-role no, cluster role present)"

# 7. 身份链（smoke 同款：bootstrap → init 铸 CLI token）。
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

# 8. app 部署全链（image 直投 + 端口声明——Route 面前置）。
log "creating project + app + deploying nginx"
cli projects create shop >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli apps create --project "$PROJECT_ID" web >/dev/null
APP_ID=$(cli --json apps list --project "$PROJECT_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
DEP_ID=$(cli --json deploy --app "$APP_ID" --image nginx:1.27 --port 80 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)

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
        docker exec "$DIND_CID" sh -c 'k3s kubectl get pods -A 2>&1; k3s kubectl describe pods -A --field-selector=status.phase!=Running 2>&1 | grep -A10 Events: | tail -30; k3s kubectl get events --sort-by=.lastTimestamp 2>&1 | tail -15' >&2 || true
        docker exec "$DIND_CID" sh -c "grep -v 'gRPC request' /var/log/fleetlyd.log | tail -30" >&2 || true
        fail "deployment reached $state before $want"
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  fail "timed out waiting for $want"
}
wait_state succeeded
log "deployment succeeded on k3s"

# k3s 载体断言：Deployment ready（label 定位——ns 名 = fleetly-<projectID>、
# 载体名 = fleetly-<appID>-<proc>，ULID 均不硬编码；label 值是 sanitize 后
# 的实体 ID（与 swarm 公式同构），不是实体名——首跑实锤 =web 查空）。
P1_NS="fleetly-$(printf '%s' "$PROJECT_ID" | tr 'A-Z' 'a-z')"
P1_APP_LC=$(printf '%s' "$APP_ID" | tr 'A-Z' 'a-z')
log "verifying k3s carriers (ns=$P1_NS app=$P1_APP_LC)"
docker exec -e APP_SEL="$P1_APP_LC" "$DIND_CID" sh -c '
  i=0
  while [ $i -lt 150 ]; do
    ready=$(k3s kubectl get pods -A -l "fleetly.ns.app=$APP_SEL" --no-headers 2>/dev/null | grep -c "1/1" || echo 0)
    [ "$ready" = "1" ] && break
    i=$((i+1)); sleep 2
  done
  [ "$ready" = "1" ] || { k3s kubectl get deployments -A | grep fleetly >&2; exit 1; }
'
docker exec "$DIND_CID" sh -c "k3s kubectl get svc web -n $P1_NS >/dev/null && k3s kubectl get svc web-web -n $P1_NS >/dev/null" \
  || fail "addressing services (bare + full process names) missing"
log "k3s carriers + addressing services verified"

# 9. 受管域全绿（traefik/zot/VL/VM/cadvisor 五域——首跑曾只等 traefik，
#    cadvisor 的材料卷冲突在 dind 同样 RunContainerError 却无人看,staging
#    真机才咬出;断言面 = ns 内全部 pod Running）+ Route 明文端到端。前置
#    于 rollback/egress/db 段:route 链只依赖 app+traefik(fleetlyd 起动即
#    部署),早断言早失败——run21 实证尾段时点单节点上多载体挤压会让
#    后端不可达(502),语义验证不应与容量压力耦合。
log "managed stack readiness (five domains) + plaintext route drill"
i=0
while [ "$i" -lt 150 ]; do
  # grep -c 零匹配/查无 ns 均为非零退出——|| true 防 set -e 静默击穿
  #（dash 对 n=$(失败命令) 即死，run14 实证日志戛然而止无 FATAL）。
  total=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n fleetly-system --no-headers 2>/dev/null | grep -c ." || true)
  running=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n fleetly-system --no-headers 2>/dev/null | grep -c Running" || true)
  [ "$total" -ge 5 ] && [ "$running" = "$total" ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 150 ] || {
  docker exec "$DIND_CID" sh -c 'k3s kubectl get all -n fleetly-system 2>&1; k3s kubectl describe pods -n fleetly-system --field-selector=status.phase!=Running 2>&1 | grep -A8 Events: | tail -25' >&2
  docker exec "$DIND_CID" sh -c "grep -iE 'managed|traefik|ensure' /var/log/fleetlyd.log | grep -v gRPC | tail -25" >&2 || true
  fail "managed stack did not come up all-Running on k3s (five domains expected)"
}
log "managed stack all-Running in fleetly-system (traefik/zot/VL/VM/cadvisor)"

# --tls none 必须显式:route TLS 缺省语义是 auto(ACME 求解)——sslip 域名
# 无效邮箱形态 ACME 必败,明文 80 上无路由即 404(h2c e2e 先例 + 聚焦探针
# 实证 traefik 日志 ACME invalidContact)。
cli routes create --project "$PROJECT_ID" --app "$APP_ID" --process web --port 80 \
  --host k3s-e2e.127.0.0.1.sslip.io --protocol http --tls none >/dev/null
# hostPort 探测打节点 IP 而非 127.0.0.1：flannel 的 hostPort 由 portmap
# DNAT 承载，不覆盖 loopback 流量（swarm 的 routing mesh 相反，ingress
# 监听 0.0.0.0 含 lo——跨 Runtime 的探测形态差异）。
i=0
while [ "$i" -lt 60 ]; do
  code=$(docker exec "$DIND_CID" sh -c "wget -q -O /dev/null -T 5 --header='Host: k3s-e2e.127.0.0.1.sslip.io' http://$DIND_IP/ && echo ok" 2>/dev/null || true)
  [ "$code" = "ok" ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 60 ] || {
  docker exec "$DIND_CID" sh -c "
    echo '--- route/proxy log lines'; grep -iE 'route|proxy|traefik|publish' /var/log/fleetlyd.log | grep -v gRPC | tail -20
    echo '--- config served'; wget -q -O- -T 5 http://$DIND_IP:9082/proxy/config 2>&1 | head -40; echo
    echo '--- wget status'; wget -S -O /dev/null -T 5 --header='Host: k3s-e2e.127.0.0.1.sslip.io' http://$DIND_IP/ 2>&1 | head -10
    echo '--- pods all'; k3s kubectl get pods -A 2>&1 | head -12
    echo '--- traefik logs'; k3s kubectl logs -n fleetly-system \$(k3s kubectl get pods -n fleetly-system --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name}) --tail=15 2>&1 | tail -15
  " >&2 || true
  fail "route 200 via managed traefik (hostPort 80) not reachable"
}
log "route end-to-end green (traefik hostPort 80 -> app service backend)"

# 10. rollback = Revision Replay（k3s 上同链）。单 Revision 场景：先落第二
#    部署（--env 变化）制造双 Revision 基线，再显式回滚到 R1。
log "rollback via revision replay"
R1=$(cli --json revisions list --app "$APP_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli deploy --app "$APP_ID" --image nginx:1.27 --port 80 --env ROLL=2 >/dev/null
wait_state succeeded
cli rollback --app "$APP_ID" --to "$R1" --wait >/dev/null
log "rollback green"

# 11. egress:none 强隔离活体实证（ADR-0052 决策 6 的 e2e 锚）：
#     - 项目 shop 声明 egress:none 网络 + busybox 载体挂之
#     - 载体标记 fleetly.egress=true + ns 内 netpol 在场
#     - 活体：跨 ns（shop2 的 nginx）不可达；DNS（kube-system）放行
log "egress:none strong isolation drill"
cli networks create --project "$PROJECT_ID" --egress-none isolated >/dev/null
# compose 受控子集：顶层 networks 声明被拒（白名单只收 services/volumes，
# 网络实体走平台 API——cli networks create 在先）；service 级 networks
# 按名引用平台网络（egress 属性真源在 networks 表，投影期解析——ADR-0052
# 决策 6）。web 服务保进夹具：deploy 是整 App 新 Revision，只带 worker
# 会把 web 收敛掉（应用语义连续性 + 终态完整性）。
cat > "$WORKDIR/egress-compose.yaml" <<'EOF'
services:
  web:
    image: nginx:1.27
    ports: ["80"]
  worker:
    image: busybox:1.37
    command: ["sleep", "3600"]
    networks: [isolated]
EOF
docker exec -i "$DIND_CID" sh -c 'cat > /tmp/egress-compose.yaml' < "$WORKDIR/egress-compose.yaml"
EGRESS_DEP=$(cli --json deploy --app "$APP_ID" --compose-file /tmp/egress-compose.yaml | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$EGRESS_DEP" ] || fail "egress compose deploy failed"
# compose 部署同链 L1 门（run3/4 实证非确定性：标记在而 netpol 缺/标记缺
# 两种死法——不 wait 部署态会把失败吞进后面的轮询窗）。
wait_state succeeded

cli projects create shop2 >/dev/null
PROJECT2_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | tail -1)
P2_NS="fleetly-$(printf '%s' "$PROJECT2_ID" | tr 'A-Z' 'a-z')"
cli apps create --project "$PROJECT2_ID" web >/dev/null
APP2_ID=$(cli --json apps list --project "$PROJECT2_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
cli deploy --app "$APP2_ID" --image nginx:1.27 --port 80 >/dev/null

wait_pod_label() {
  # 等 Running 而非对象在场：活体探针随后 exec 进该 pod，Pending 期 exec
  # 必失败（label 在 pod 模板里，对象一创建选择器即命中——计数是竞态）。
  i=0
  while [ "$i" -lt 120 ]; do
    n=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P1_NS -l fleetly.egress=true --no-headers 2>/dev/null | grep -c Running" || true)
    [ "$n" -ge 1 ] && return 0
    i=$((i + 1)); sleep 2
  done
  return 1
}
wait_pod_label || {
  docker exec "$DIND_CID" sh -c "
    echo '--- pods'; k3s kubectl get pods -n $P1_NS -o wide --show-labels 2>&1 | head -8
    echo '--- deploy'; k3s kubectl get deploy -n $P1_NS -o wide 2>&1
    echo '--- events'; k3s kubectl get events -n $P1_NS --sort-by=.lastTimestamp 2>&1 | tail -10
    echo '--- fleetlyd tail'; grep -v 'gRPC request' /var/log/fleetlyd.log | tail -40
  " >&2 || true
  fail "egress carrier label missing (projection->carrier chain broke)"
}
docker exec "$DIND_CID" sh -c "k3s kubectl get netpol fleetly-egress-deny -n $P1_NS >/dev/null" \
  || {
    docker exec "$DIND_CID" sh -c "
      echo '--- netpol -A'; k3s kubectl get netpol -A 2>&1
      echo '--- pods labels'; k3s kubectl get pods -n $P1_NS -o wide --show-labels 2>&1 | head -8
      echo '--- deploy template labels'; k3s kubectl get deploy -n $P1_NS -o jsonpath='{range .items[*]}{.metadata.name}{\" gen=\"}{.metadata.labels.fleetly-generation}{\" tmpl=\"}{.spec.template.metadata.labels}{\"\\n\"}{end}' 2>&1
      echo '--- events'; k3s kubectl get events -n $P1_NS --sort-by=.lastTimestamp 2>&1 | tail -10
      echo '--- fleetlyd egress lines'; grep -i egress /var/log/fleetlyd.log | tail -15
    " >&2 || true
    fail "egress deny NetworkPolicy missing in project namespace"
  }
log "egress carrier marker + NetworkPolicy present"

# 活体：跨 ns 不可达（deny）+ DNS 放行（allowlist 面）。探针一律 FQDN——
# busybox nslookup/wget 不走 search list，短名形态假阴（DNS 步）与假阳
# （deny 步：DNS 挂也会让 wget 失败、断言空过）双坑。
docker exec "$DIND_CID" sh -c "
  pod=\$(k3s kubectl get pods -n $P1_NS -l fleetly.egress=true --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name})
  [ -n \"\$pod\" ] || { echo 'no running egress pod for live probe' >&2; exit 1; }
  # DNS 放行：放行集承载服务发现。
  if ! k3s kubectl exec -n $P1_NS \"\$pod\" -- nslookup kubernetes.default.svc.cluster.local >/dev/null 2>&1; then
    echo 'DNS must stay allowed for egress carriers; diagnostics:' >&2
    k3s kubectl exec -n $P1_NS \"\$pod\" -- nslookup kubernetes.default.svc.cluster.local 2>&1 >&2 || true
    k3s kubectl exec -n $P1_NS \"\$pod\" -- cat /etc/resolv.conf >&2 || true
    webpod=\$(k3s kubectl get pods -n $P1_NS -l fleetly.ns.app=$P1_APP_LC --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name})
    echo '--- control pod (no egress label) same probe:' >&2
    k3s kubectl exec -n $P1_NS \"\$webpod\" -- nslookup kubernetes.default.svc.cluster.local >&2 2>&1 || true
    exit 1
  fi
  # 跨 ns 出站：shop2 的 nginx Service 必须不可达（FQDN——DNS 已在上步实证）。
  if k3s kubectl exec -n $P1_NS \"\$pod\" -- wget -q -O /dev/null -T 5 http://web.$P2_NS.svc.cluster.local/ >/dev/null 2>&1; then
    echo 'cross-namespace egress must be denied by NetworkPolicy' >&2
    exit 1
  fi
" || fail "egress isolation live probe failed"
log "egress live probe green (DNS allowed, cross-namespace denied)"

# 12. 网络成员资格入站隔离（ADR-0054 决策 1 的 e2e 锚）：
#     - 成员 policy 在场（每网一条组级聚合）+ 载体成员资格 label
#     - 活体矩阵（探针一律 busybox wget，deny = netpol 落丢包 → -T 超时）：
#       default 成员 → 同网 web 通 / 跨 ns（shop2）拒 / task 组拒；
#       task pod（taskgrp-pipe）→ 项目服务拒；跨挂 bridge → task 组内通
#     - peer drill：declare→approve→挂靠部署→跨项目通（grant policy 在场）
#       →revoke→engine isolate 环收敛→拒（grant policy 消失）
log "network membership isolation drill (ADR-0054)"
docker exec "$DIND_CID" sh -c "k3s kubectl get netpol -n $P1_NS --no-headers 2>/dev/null | grep -c fleetly-netisolate-" \
  | grep -qv '^0$' || fail "membership policies missing in project namespace"
log "membership policies present (per-network, group-level aggregation)"

# drill 夹具：web（nginx/default）+ probe（default 探针）+ bridge（跨挂
# taskGroup:pipe）。task 先行——bridge 的组内通探针要目标在场。
TASK_ID=$(cli --json tasks create --project "$PROJECT_ID" --name netgrp-drill \
  --form one-shot --image busybox:1.37 --network-group pipe \
  --command sh --command '-c' --command 'mkdir -p /www && echo task-ok > /www/index.html && httpd -f -p 8080 -h /www' \
  | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$TASK_ID" ] || fail "task create failed"
TASK_LC=$(printf '%s' "$TASK_ID" | tr 'A-Z' 'a-z')
i=0
while [ "$i" -lt 120 ]; do
  n=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P1_NS -l fleetly.ns.task=$TASK_LC --no-headers 2>/dev/null | grep -c Running" || true)
  [ "$n" -ge 1 ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 120 ] || { docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P1_NS -o wide 2>&1 | head -10" >&2 || true; fail "task run pod never running"; }
log "task run pod running (network group pipe, httpd on 8080)"

cat > "$WORKDIR/netisolate-compose.yaml" <<EOF
services:
  web:
    image: nginx:1.27
    ports: ["80"]
  probe:
    image: busybox:1.37
    command: ["sleep", "3600"]
    networks: ["default"]
  bridge:
    image: busybox:1.37
    command: ["sleep", "3600"]
    networks: ["taskGroup:pipe"]
EOF
docker exec -i "$DIND_CID" sh -c 'cat > /tmp/netisolate-compose.yaml' < "$WORKDIR/netisolate-compose.yaml"
cli --json deploy --app "$APP_ID" --compose-file /tmp/netisolate-compose.yaml >/dev/null || fail "netisolate compose deploy failed"
wait_state succeeded

P2_APP_LC=$(printf '%s' "$APP2_ID" | tr 'A-Z' 'a-z')
wait_app_pod() {
  ns="$1"; applc="$2"; i=0
  while [ "$i" -lt 120 ]; do
    n=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $ns -l fleetly.ns.app=$applc --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -c Running" || true)
    [ "$n" -ge 1 ] && return 0
    i=$((i + 1)); sleep 2
  done
  return 1
}
wait_app_pod "$P1_NS" "$P1_APP_LC" || fail "drill web pod never running"
wait_app_pod "$P2_NS" "$P2_APP_LC" || fail "shop2 web pod never running"
# 进程名等待（probe/bridge/consumer 是部署后新增载体——succeeded 只是 L1 门，
# pod 就绪有调度窗）。
wait_process_pod() {
  ns="$1"; proc="$2"; i=0
  while [ "$i" -lt 120 ]; do
    n=$(docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $ns -l fleetly.process=$proc --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -c Running" || true)
    [ "$n" -ge 1 ] && return 0
    i=$((i + 1)); sleep 2
  done
  return 1
}
wait_process_pod "$P1_NS" probe || fail "probe pod never running"
wait_process_pod "$P1_NS" bridge || fail "bridge pod never running"
probe_pod() {
  docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $1 -l fleetly.process=$2 --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name}"
}
PROBE_POD=$(probe_pod "$P1_NS" probe)
BRIDGE_POD=$(probe_pod "$P1_NS" bridge)
[ -n "$PROBE_POD" ] && [ -n "$BRIDGE_POD" ] || fail "probe/bridge pods missing"
TASK_FQDN="task-$TASK_LC.$P1_NS.svc.cluster.local"

# 活体矩阵（must-pass 用 if ! …；must-deny 用 if … ——deny 的表现形态是
# wget -T 超时（netpol 落丢包），非快速拒绝）。探针带两拍重试（瞬时 DNS 窗
# 误判防护）；终态失败时落全景诊断（nslookup/resolv.conf/svc/netpol/CoreDNS）。
docker exec "$DIND_CID" sh -c "
  wget_probe() {
    p=\$1; url=\$2; k=0
    while [ \$k -lt 3 ]; do
      if k3s kubectl exec -n $P1_NS \"\$p\" -- wget -q -O /dev/null -T 5 \"\$url\"; then
        return 0
      fi
      k=\$((k + 1)); sleep 3
    done
    return 1
  }
  diag() {
    echo '--- pod resolv.conf' >&2
    k3s kubectl exec -n $P1_NS \"\$1\" -- cat /etc/resolv.conf >&2 2>&1 || true
    echo '--- nslookup target' >&2
    k3s kubectl exec -n $P1_NS \"\$1\" -- nslookup web.$P1_NS.svc.cluster.local >&2 2>&1 || true
    echo '--- nslookup kubernetes.default' >&2
    k3s kubectl exec -n $P1_NS \"\$1\" -- nslookup kubernetes.default.svc.cluster.local >&2 2>&1 || true
    echo '--- services' >&2
    k3s kubectl get svc -n $P1_NS >&2 2>&1 || true
    echo '--- pods wide' >&2
    k3s kubectl get pods -n $P1_NS -o wide >&2 2>&1 || true
    echo '--- netpol all namespaces' >&2
    k3s kubectl get netpol -A >&2 2>&1 || true
    echo '--- coredns pods' >&2
    k3s kubectl get pods -n kube-system -l k8s-app=kube-dns -o wide >&2 2>&1 || true
  }
  diag_deny() {
    echo '--- netpol -A' >&2
    k3s kubectl get netpol -A >&2 2>&1 || true
    echo '--- target ns default policy yaml' >&2
    k3s kubectl get netpol -n $P2_NS -o yaml 2>&1 | grep -vE 'creationTimestamp|resourceVersion|uid|generation|managedFields' >&2 || true
    echo '--- target pods labels' >&2
    k3s kubectl get pods -n $P2_NS --show-labels >&2 2>&1 || true
    echo '--- target pod direct-IP probe (bypass service VIP)' >&2
    tip=\$(k3s kubectl get pods -n $P2_NS --field-selector=status.phase=Running -o jsonpath={.items[0].status.podIP})
    k3s kubectl exec -n $P1_NS $PROBE_POD -- wget -q -O /dev/null -T 3 http://\$tip/ 2>&1 >&2 || true
  }
  if ! wget_probe $PROBE_POD http://web.$P1_NS.svc.cluster.local/; then
    echo 'same-network member must reach web' >&2; diag $PROBE_POD; exit 1
  fi
  if wget_probe $PROBE_POD http://web.$P2_NS.svc.cluster.local/; then
    echo 'cross-namespace must be denied by membership isolation' >&2; diag_deny; exit 1
  fi
  if wget_probe $PROBE_POD http://$TASK_FQDN:8080/; then
    echo 'task group must be isolated from project network members' >&2; exit 1
  fi
  if ! wget_probe $BRIDGE_POD http://$TASK_FQDN:8080/; then
    echo 'cross-attached app process must reach the task group' >&2; diag $BRIDGE_POD; exit 1
  fi
  taskpod=\$(k3s kubectl get pods -n $P1_NS -l fleetly.ns.task=$TASK_LC --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name})
  if wget_probe \"\$taskpod\" http://web.$P1_NS.svc.cluster.local/; then
    echo 'task pod must not reach project services (group-only attachment)' >&2; exit 1
  fi
" || fail "membership isolation live matrix failed"
log "membership isolation live matrix green (same-net allow, cross-ns/task-group deny, cross-attach allow)"

# peer drill：shop2（挂靠方）挂 shop 的 default 网（接收方）。
P1_NET_ID=$(cli --json networks list --project "$PROJECT_ID" | grep -B3 '"name": *"default"' | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$P1_NET_ID" ] || fail "shop default network id not resolved"
PEER_ID=$(cli --json networks declare --network "$P1_NET_ID" --project "$PROJECT2_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$PEER_ID" ] || fail "peer declare failed"
cli networks approve "$PEER_ID" >/dev/null || fail "peer approve failed"
log "peer declared and approved (shop2 -> shop default)"

cat > "$WORKDIR/peer-compose.yaml" <<EOF
services:
  web:
    image: nginx:1.27
    ports: ["80"]
  consumer:
    image: busybox:1.37
    command: ["sleep", "3600"]
    networks: ["project:$PROJECT_ID/default"]
EOF
docker exec -i "$DIND_CID" sh -c 'cat > /tmp/peer-compose.yaml' < "$WORKDIR/peer-compose.yaml"
cli --json deploy --app "$APP2_ID" --compose-file /tmp/peer-compose.yaml >/dev/null || fail "peer compose deploy failed"
wait_process_pod "$P2_NS" consumer || fail "peer consumer pod never running"
CONSUMER_POD=$(probe_pod "$P2_NS" consumer)
[ -n "$CONSUMER_POD" ] || fail "consumer pod missing"
docker exec "$DIND_CID" sh -c "k3s kubectl get netpol -n $P1_NS --no-headers 2>/dev/null | grep -q fleetly-peer-" \
  || fail "peer grant policy missing in receiving namespace"
docker exec "$DIND_CID" sh -c "
  if ! k3s kubectl exec -n $P2_NS $CONSUMER_POD -- wget -q -O /dev/null -T 5 http://web.$P1_NS.svc.cluster.local/; then
    echo 'approved peer must reach the receiving project network' >&2; exit 1
  fi
" || fail "peer attachment live probe failed"
log "peer grant green (approved cross-project attachment reachable, grant policy present)"

# 撤销：engine isolate 环（A.4）剥离引用 → 挂靠方 Ensure 收敛删双侧 policy。
# 轮询内重解析 consumer pod 名——剥离改 pod 模板（引用 label 消失）触发
# 滚动替换，旧名 exec 会假阴（pod 不存在 ≠ 隔离生效）。
cli networks revoke "$PEER_ID" >/dev/null || fail "peer revoke failed"
i=0
while [ "$i" -lt 60 ]; do
  cp=$(probe_pod "$P2_NS" consumer)
  if [ -z "$cp" ]; then
    i=$((i + 1)); sleep 2; continue
  fi
  if docker exec "$DIND_CID" sh -c "k3s kubectl exec -n $P2_NS $cp -- wget -q -O /dev/null -T 3 http://web.$P1_NS.svc.cluster.local/" >/dev/null 2>&1; then
    i=$((i + 1)); sleep 2; continue
  fi
  break
done
[ "$i" -lt 60 ] || fail "peer isolation never converged after revoke (still reachable)"
docker exec "$DIND_CID" sh -c "k3s kubectl get netpol -n $P1_NS --no-headers 2>/dev/null | grep -q fleetly-peer-" \
  && fail "peer grant policy must be converged away after revoke"
log "peer revoke isolation green (unreachable + grant converged away)"

# 12b. grant 残留卫生清扫（ADR-0055 决策 3 的 e2e 锚）：再声明→批准→挂靠
#     部署（grant 在场）→ 删挂靠方 App+项目（Remove 不清他 ns 的自有 grant
#     ——残留面）→ retention janitor 拍（10min 节拍）后接收方 ns 的 grant
#     被清扫。断言带界轮询（janitor 相位不可控,预算 13min 窗）。
log "peer grant hygiene drill (declare -> grant present -> declarer project deleted -> grant swept)"
PEER2_ID=$(cli --json networks declare --network "$P1_NET_ID" --project "$PROJECT2_ID" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$PEER2_ID" ] || fail "hygiene drill: peer re-declare failed"
cli networks approve "$PEER2_ID" >/dev/null || fail "hygiene drill: peer approve failed"
docker exec -i "$DIND_CID" sh -c 'cat > /tmp/peer-compose.yaml' < "$WORKDIR/peer-compose.yaml"
cli --json deploy --app "$APP2_ID" --compose-file /tmp/peer-compose.yaml >/dev/null || fail "hygiene drill: peer compose deploy failed"
# 等的必须是挂靠方自己的 App（wait_state 缺省 $APP_ID 是 shop 的——首跑
# 实锤：等错 App 即刻假绿，随后 apps delete 撞 E_CONFLICT 活跃部署拒）。
wait_state succeeded "$APP2_ID"
wait_process_pod "$P2_NS" consumer || fail "hygiene drill: consumer pod never running"
docker exec "$DIND_CID" sh -c "k3s kubectl get netpol -n $P1_NS --no-headers 2>/dev/null | grep -q fleetly-peer-" \
  || fail "hygiene drill: peer grant policy missing before declarer teardown"
log "hygiene drill: grant present in receiving namespace (declarer live)"
# 删挂靠方 App + 项目（活跃 App 会挡项目删除——先拆 App 再删项目；delete
# 动词走旗标非位置参数——verbs_structure 契约）。
cli apps delete --app "$APP2_ID" >/dev/null || fail "hygiene drill: apps delete failed"
cli projects delete --project "$PROJECT2_ID" >/dev/null || fail "hygiene drill: projects delete failed"
log "hygiene drill: declarer app+project deleted (grant now orphaned in receiving namespace)"
i=0
while [ "$i" -lt 78 ]; do
  n=$(docker exec "$DIND_CID" sh -c "k3s kubectl get netpol -n $P1_NS --no-headers 2>/dev/null | grep -c fleetly-peer-" || true)
  [ "$n" = "0" ] && break
  i=$((i + 1)); sleep 10
done
[ "$i" -lt 78 ] || {
  docker exec "$DIND_CID" sh -c "grep -i 'orphan grant' /var/log/fleetlyd.log | tail -5; k3s kubectl get netpol -A 2>&1 | grep peer" >&2 || true
  fail "hygiene drill: orphaned peer grant was not swept by the retention janitor"
}
log "peer grant hygiene green (orphaned grant swept after declarer project deletion)"

# 空域收尾断言（ADR-0056 决策 5）：挂靠方项目删除（级联已拆全部域载体，
# 无 PVC）→ 项目 ns 随最后域 Remove 收尾删除。ns 删除是异步收敛——带界
# 轮询；已进 terminating/消失均算收口（kubectl get ns 查不到即断言通过）。
i=0
while [ "$i" -lt 60 ]; do
  if ! docker exec "$DIND_CID" sh -c "k3s kubectl get ns $P2_NS >/dev/null 2>&1"; then
    break
  fi
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 60 ] || { docker exec "$DIND_CID" sh -c "k3s kubectl get ns $P2_NS -o yaml 2>&1 | tail -8; k3s kubectl get all -n $P2_NS 2>&1" >&2 || true; fail "hygiene drill: project namespace $P2_NS was not torn down after project deletion"; }
log "drained-namespace teardown green (empty project namespace removed)"

# 13. Database：postgres（digest 引用）+ PVC local-path 绑定（digest 预拉
#     已前置，零在线拉）。
log "database drill (postgres + PVC)"
DB_ID=$(cli --json databases create --project "$PROJECT_ID" --engine postgres pgold | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$DB_ID" ] || fail "database create failed"
i=0
while [ "$i" -lt 360 ]; do
  # pretty JSON：id 与 status 不同行——grep -A 窗口锚 id 行再取 status
  #（单行 sed 永不匹配 = 空转 12 分钟超时的裸死教训）。
  status=$(cli --json databases list --project "$PROJECT_ID" | grep -A10 "\"id\": *\"$DB_ID\"" | sed -n 's/.*"status": *"\([^"]*\)".*/\1/p' | head -1)
  [ "$status" = "running" ] && break
  [ "$status" = "failed" ] && fail "database reached failed"
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 360 ] || fail "database did not reach running"
docker exec "$DIND_CID" sh -c "k3s kubectl get pvc -n $P1_NS | grep -q fleetly-vol-" \
  || fail "database PVC not bound"
log "database running with bound PVC"

# 13b. from_build 构建链（ADR-0056 决策 1/2 的 e2e 锚——k3s 形态首次全链）：
#      CLI --from-dir 上传源 → engine 构建（builder 连控制面节点 daemon 内嵌
#      buildkit）→ solve 产物入 daemon 镜像库 → ImagePush 推受管 zot（per-Project
#      凭证；daemon 侧 insecure-registry 信任面）→ digest 冻结 → 下发引用
#      <addr>/<projectID>/<appID>@sha256 → kubelet 拉起（节点侧 registries.yaml
#      明文信任面 + pod imagePullSecrets 认证面）→ Route 活体。scratch 基础
#      镜像零外网依赖（h2c e2e 先例同款源）。
log "from_build chain drill (dockerfile builder -> managed zot -> kubelet pull)"
docker exec "$DIND_CID" mkdir -p /root/h2csrc
docker exec -i "$DIND_CID" sh -c 'cat > /root/h2csrc/h2cserver && chmod +x /root/h2csrc/h2cserver' < "$WORKDIR/bins/h2cserver"
docker exec "$DIND_CID" sh -c \
  'printf "FROM scratch\nCOPY h2cserver /h2cserver\nEXPOSE 8080\nENTRYPOINT [\"/h2cserver\"]\n" > /root/h2csrc/Dockerfile'
cli apps create --project "$PROJECT_ID" buildapp >/dev/null
BUILD_APP_ID=$(cli --json apps list --project "$PROJECT_ID" \
  | grep -B3 '"buildapp"' | grep '"id"' | head -1 \
  | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')
[ -n "$BUILD_APP_ID" ] || fail "from_build drill: buildapp app id not resolved"
cli --json deploy --app "$BUILD_APP_ID" --from-dir /root/h2csrc --builder dockerfile --port 8080 >/dev/null \
  || fail "from_build drill: deploy --from-dir failed"
wait_state succeeded "$BUILD_APP_ID"
log "from_build deployment succeeded (build -> push -> release chain green)"

# Build 行断言：succeeded + digest 冻结（Revision 级单产物）。
BUILD_STATE=$(cli --json builds list --app "$BUILD_APP_ID" \
  | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1)
BUILD_DIGEST=$(cli --json builds list --app "$BUILD_APP_ID" \
  | sed -n 's/.*"digest": *"\(sha256:[a-f0-9]*\)".*/\1/p' | head -1)
[ "$BUILD_STATE" = "succeeded" ] || fail "from_build drill: build state is $BUILD_STATE (want succeeded)"
[ -n "$BUILD_DIGEST" ] || fail "from_build drill: build digest missing on the build row"

# 载体镜像断言：pod image 引用 = 受管仓库 digest 形态（repo 双段小写
# buildRepo 公式 + 本批 digest；该镜像只可能在受管 zot——airgap 预载清单
# 不含它，Running 即证明 kubelet 经明文信任面拉取成功）。
BUILD_LC=$(printf '%s' "$BUILD_APP_ID" | tr 'A-Z' 'a-z')
P1_PROJ_LC=$(printf '%s' "$PROJECT_ID" | tr 'A-Z' 'a-z')
BIMAGE=""
i=0
while [ "$i" -lt 90 ]; do
  BIMAGE=$(docker exec "$DIND_CID" sh -c \
    "k3s kubectl get pods -n $P1_NS -l fleetly.ns.app=$BUILD_LC --field-selector=status.phase=Running -o jsonpath={.items[0].spec.containers[0].image} 2>/dev/null" || true)
  [ -n "$BIMAGE" ] && break
  i=$((i + 1)); sleep 2
done
[ -n "$BIMAGE" ] || { docker exec "$DIND_CID" sh -c "k3s kubectl get pods -n $P1_NS --show-labels 2>&1 | tail -6" >&2 || true; fail "from_build drill: carrier pod never running"; }
WANT_PREFIX="$DIND_IP:5000/$P1_PROJ_LC/$BUILD_LC@"
case "$BIMAGE" in
  "$WANT_PREFIX"sha256:*) log "carrier image is the managed-registry digest ref ($BIMAGE)" ;;
  *) fail "from_build drill: carrier image is $BIMAGE (want $WANT_PREFIX sha256:...)" ;;
esac

# Route 活体：受管 traefik → buildapp 后端（h2cserver 回显 PROTO-LINE）。
cli routes create --project "$PROJECT_ID" --host frombuild.127.0.0.1.sslip.io \
  --app "$BUILD_APP_ID" --process web --port 8080 --protocol http --tls none >/dev/null
i=0
FBODY=""
while [ "$i" -lt 60 ]; do
  FBODY=$(docker exec "$DIND_CID" sh -c "wget -q -O- -T 5 --header='Host: frombuild.127.0.0.1.sslip.io' http://$DIND_IP/ 2>/dev/null" || true)
  case "$FBODY" in
    *PROTO-LINE*) break ;;
  esac
  i=$((i + 1)); sleep 2
done
case "$FBODY" in
  *PROTO-LINE*) log "from_build route live probe green (built artifact serving traffic)" ;;
  *) docker exec "$DIND_CID" sh -c "
    echo '--- fleetlyd build/push lines'; grep -iE 'build|push|registry' /var/log/fleetlyd.log | grep -v gRPC | tail -20
    echo '--- pod describe'; k3s kubectl describe pods -n $P1_NS -l fleetly.ns.app=$BUILD_LC 2>&1 | grep -A8 Events: | tail -12
    echo '--- traefik log tail'; k3s kubectl logs -n fleetly-system \$(k3s kubectl get pods -n fleetly-system -l app=registry 2>/dev/null | head -1) --tail=5 2>/dev/null || true
  " >&2 || true
     fail "from_build drill: route probe never returned the built artifact body" ;;
esac

# 14. exec 子面（ADR-0053 决策 1 集中形态的 e2e 锚）：回环 per-node 注册
#     （relay_online）→ 单命令退出码/输出透传 → shell 会话（stdin EOF 收口）
#     → 审计行。SPDY 通道 + engine 会话路由全链经真 apiserver。
log "exec leg (central form: loopback relay + apiserver SPDY)"
i=0
while [ "$i" -lt 60 ]; do
  n=$(cli --json nodes list | grep -c '"relay_online": *true' || true)
  [ "$n" -ge 1 ] && break
  i=$((i + 1)); sleep 2
done
[ "$i" -lt 60 ] || { docker exec "$DIND_CID" sh -c "grep -i relay /var/log/fleetlyd.log | tail -10" >&2 || true; fail "node relay_online never came up (central loopback agent)"; }
log "node relay_online (central loopback agent registered)"

# 退出码与输出透传（|| rc=$? 形态护住 set -e——非零退出码是被测语义）。
EXEC_OUT=$(cli exec "$APP_ID/web" -- /bin/echo store-exec-ok 2>&1) \
  || fail "fleetly exec failed: $EXEC_OUT"
case "$EXEC_OUT" in *store-exec-ok*) log "exec output passthrough green" ;; *) fail "exec output missing: $EXEC_OUT" ;; esac
rc=0
cli exec "$APP_ID/web" -- /bin/sh -c 'exit 23' >/dev/null 2>&1 || rc=$?
[ "$rc" = "23" ] || fail "exec exit code passthrough failed (expected 23, got $rc)"
log "exec exit code passthrough green (23)"

# shell 会话（TTY 形态）：显式 argv 自退出——PTY 会话的 stdin EOF 不终止
# 载体进程（k8s 通道的 stdin 关闭 ≠ swarm hijack 关闭收口；契约允许会话
# 活到 idle timeout，显式 argv 是非交互形态的确定性通道）。头行在 stderr
# （swarm 腿同锚），命令体在 stdout——合并流双锚。带两拍重试：PTY 输出
# 排空与会话收口存在时序窗（N6 批实录一次 flake——exec/relay 链零改动、
# 单命令形态全绿，重试即绿）。
SHELL_ALL=""
i=0
while [ "$i" -lt 3 ]; do
  SHELL_ALL=$(cli shell "$APP_ID/web" -- sh -c 'echo session-body-ok' 2>&1)
  case "$SHELL_ALL" in
    *session-body-ok*) break ;;
  esac
  i=$((i + 1)); sleep 3
done
case "$SHELL_ALL" in
  *"# session"*) : ;;
  *) fail "shell session header missing, output: $SHELL_ALL" ;;
esac
case "$SHELL_ALL" in
  *session-body-ok*) log "shell session opened, argv executed, exited cleanly" ;;
  *) fail "shell command body missing, output: $SHELL_ALL" ;;
esac

# exec.session_opened 事件（安全可见性面——events follow 双形态的回读锚）。
# 缺省 limit=100 从头取（AfterSeq 0 的前 100 条）——from_build 段加入后
# 全腿事件量过百，高 seq 的 exec 事件被挤出窗（run5/6 连挂实证，非落库
# 异步）；--limit 1000（服务端钳制上限）盖全腿事件量，另带三拍重试窗。
i=0
while [ "$i" -lt 3 ]; do
  n=$(cli --json events list --limit 1000 2>/dev/null | grep -c 'exec.session_opened' || true)
  [ "$n" -ge 1 ] && break
  i=$((i + 1)); sleep 3
done
[ "$n" -ge 1 ] || fail "exec.session_opened events missing"
log "exec events present"

echo ""
echo "K3S E2E PASSED"
