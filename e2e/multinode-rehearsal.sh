#!/bin/sh
# e2e/multinode-rehearsal.sh — E1-9 多节点演练的 dind 双节点形态（multi-node
# 设计 §4.2 三断言 + D-MN-1 auto-rotate 端到端；真 VPS 形态复用 W1 journey
# 路径，本脚本为其可 CI 化的演进形态）。
#
# 编排（宿主侧，自足；run-dind-test.sh / e2e/nightly/run.sh 同惯例）：
#   交叉编译 linux/amd64 fleetlyd+fleetly（或复用 MN_BIN_DIR）→ 宿主 bridge
#   上起 mgr + w1 两个特权 dind（私网 10.214.0.0/24，镜像钉 digest）→
#   exec+stdin 注入二进制与配置（禁 docker cp 宿主→dind 方向，e2e/README
#   已知问题）→ mgr: swarm init（advertise=eth0 私网）+ fleetlyd 起服 →
#   三段断言（全部经 mgr dind 内的 docker/fleetly CLI 驱动）：
#
#   A  2 节点拓扑上线（设计 §4.2 断言 A 的 dind 适配）
#      worker join → node Ready → 锚定（platform_id 非空）→ node.joined
#      事件落库 → fleetly-ingress 任务覆盖 worker → D-MN-1 auto-rotate
#      （join token 在锚定后被自动轮换——旧 token 失效、新 token 可 join）
#      + 人工 rotate-token 显式路径（fleetly nodes rotate-token）。
#      【如实跳过，归真 VPS 形态】zot push/pull 经 registry.<base>、三平台
#      子域 DNS verify、manager 全栈 idle <600MB 预算实测——dind 内无
#      base_domain/公网 DNS，无这些输入（下方输出显式 SKIP 行标注）。
#   B  无状态节点 drain（断言 B 的 dind 适配，任务层）
#      whoami 双副本跨双节点 → drain w1 → 任务迁移：Running==2 且全部落
#      mgr（新任务在另一节点 running）→ 回岗恢复。
#      【如实跳过，归真 VPS 形态】「新连接零失败」的外部探测循环（摘 DNS
#      + A 记录重试语义）——dind 断言任务层（任务迁移零滞留）。
#   C  有状态 drain→回岗自动回绑（断言 C，经平台全链驱动）
#      fleetly deploy（compose：命名卷 + fleetly.placement.node 钉 w1 +
#      以 marker 文件为健康门的 healthcheck——数据在、应用才算健康）→
#      部署进入 releasing（健康门按住，drain 必然落在窗口内，确定性）→
#      drain w1 → placement.blocked 事件 + phase=blocked_waiting + 任务
#      PENDING → 写入 marker（卷随节点存活）→ active w1 →
#      placement.recovered 事件 + 部署自动收敛 succeeded + 任务回绑 w1 +
#      marker 读出一致。
#
# 断言风格与 e2e/nightly 一致（MN-x: PASS/FAIL 行 + NL_FAIL 计数 + finish）。
# usage: e2e/multinode-rehearsal.sh
# env:
#   MN_DIND_IMAGE  dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   MN_SKIP_BUILD  1 = 跳过交叉编译，改用 MN_BIN_DIR 下的现成二进制
#   MN_BIN_DIR     MN_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   MN_VERSION     注入的版本串（默认 v0.2.0-rehearsal）
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；台账见 docs/runbooks/image-prepull.md，
#    whoami 于 2026-09-20 经 docker manifest inspect 解析多架构 index digest）。
DIND_IMAGE="${MN_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
ALPINE_IMG='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'
TRAEFIK_IMG='traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2'
WHOAMI_IMG='traefik/whoami:v1.10.4@sha256:02d8fe035f170f91cbb5e458a57f4cefab747436f8244a0eb2d66785fe5e565f'
MN_SKIP_BUILD="${MN_SKIP_BUILD:-0}"
MN_BIN_DIR="${MN_BIN_DIR:-}"
MN_VERSION="${MN_VERSION:-v0.2.0-rehearsal}"

BR_NET=fleetly-rehearsal-br
BR_SUBNET=10.214.0.0/24
MGR_IP=10.214.0.10
MGR=fleetly-rehearsal-mgr
W1=fleetly-rehearsal-w1
W2=fleetly-rehearsal-w2
APP=statefulapp

NL_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

nl() { printf '[rehearsal %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    NL_FAIL=$((NL_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
skip() { nl "$1: SKIP ${2:-}"; }
fatal() { nl "FATAL $*"; exit 1; }
finish() {
    if [ "$NL_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$NL_FAIL"
    exit 1
}

# poll_until <cap-s> <sh-test...> — 每 2s 重试一条宿主侧测试命令直到 rc=0。
poll_until() {
    cap=$1
    shift
    i=0
    while [ "$i" -lt "$cap" ]; do
        if "$@"; then return 0; fi
        i=$((i + 2))
        sleep 2
    done
    return 1
}

m() { docker exec "$MGR" "$@"; }       # mgr dind 内直跑
msh() { docker exec "$MGR" sh -c "$*"; } # mgr dind 内跑 shell 段
w1sh() { docker exec "$W1" sh -c "$*"; }
w2sh() { docker exec "$W2" sh -c "$*"; }
# fcli <args...> — mgr dind 内的 fleetly CLI（gRPC 面 + bootstrap token）。
fcli() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_PROJECT="$FOUNDER_PROJECT" -e FLEETLY_TOKEN="$MR_TOKEN" \
        "$MGR" /opt/fleetly/bin/fleetly "$@"
}
# events_grep <pattern> — 现抓事件流快照（busybox timeout 掐断 follow 流）
# 并在快照中检索。watch 是长驻流，快照即「迄今全部事件」。
events_grep() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$MR_TOKEN" \
        "$MGR" sh -c 'timeout 4 /opt/fleetly/bin/fleetly events watch --since-seq 0 > /tmp/mr-events.txt 2>/dev/null; exit 0'
    m grep -q "$1" /tmp/mr-events.txt
}
# ready_ge / ready_eq <n> — mgr 视角 Ready 节点数断言底料。
ready_ge() {
    n=$(msh "docker node ls --format '{{.Status}}' | grep -c '^Ready'" 2>/dev/null)
    [ "${n:-0}" -ge "$1" ]
}
ready_eq() {
    n=$(msh "docker node ls --format '{{.Status}}' | grep -c '^Ready'" 2>/dev/null)
    [ "${n:-0}" -eq "$1" ]
}

cleanup() {
    for d in $SUITE_DINDS; do
        docker rm -f "$d" >/dev/null 2>&1 || true
    done
    if [ -n "$ACTIVE_NET" ]; then
        docker network rm "$ACTIVE_NET" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP"
}
on_exit() {
    rc=$?
    if [ "$rc" -ne 0 ] && [ -n "$SUITE_DINDS" ]; then
        nl "suite RED (rc=$rc) — dumping mgr dind log tail"
        docker logs "$MGR" --tail 120 2>&1 | tail -60 || true
    fi
    cleanup
    exit "$rc"
}
trap on_exit EXIT INT TERM

# stage <dind> <local-file> <remote-path> — exec+stdin 直传 + 两侧体积校验 +
# 文本脚本 CR 剥离（run.sh 同款：busybox ash 不执行 CRLF；Windows 检出风险面）。
stage() {
    d=$1
    f=$2
    r=$3
    docker exec -i "$d" sh -c "cat > '$r'" <"$f" || fatal "staging $r"
    hsz=$(wc -c <"$f" | tr -d ' ')
    gsz=$(docker exec "$d" sh -c "wc -c < '$r'" | tr -d ' ')
    [ "$hsz" = "$gsz" ] || fatal "size mismatch for $r: host=$hsz dind=$gsz"
    case "$r" in
    *.sh | *.yaml | *.yml)
        docker exec "$d" sed -i 's/\r$//' "$r" || fatal "strip CR from $r"
        ;;
    esac
}

posix_path() { printf '%s' "$1" | tr '\\' '/'; }
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——原生 go.exe 在 MSYS_NO_PATHCONV=1
# 下拿不到映射，会把产物写到别的盘符；经 cygpath -m 归一为 C:/… 混合形态
# （sh/go/docker 三方都认，run-dind-test.sh 同款）。非 Cygwin 宿主原样归一。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || fatal 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || fatal 'docker not on PATH'
command -v go >/dev/null 2>&1 || MN_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$MN_SKIP_BUILD" != '1' ]; then
    nl "cross-compiling linux/amd64 fleetlyd+fleetly ($MN_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$MN_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$MN_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    MN_BIN_DIR="$TMP"
else
    MN_BIN_DIR="${MN_BIN_DIR:?MN_SKIP_BUILD=1 requires MN_BIN_DIR}"
    nl "using prebuilt binaries from $MN_BIN_DIR"
fi
[ -f "$MN_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $MN_BIN_DIR"
[ -f "$MN_BIN_DIR/fleetly" ] || fatal "fleetly missing in $MN_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
# 防御性清扫：上次崩溃残留会污染断言（幽灵容器/幽灵 swarm 成员）。
leftovers=$(docker ps -aq --filter name=fleetly-rehearsal- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    nl "WARN removing leftover rehearsal containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

dind_up() { # <name> <ip> <hostname>
    n=$1
    docker rm -f "$n" >/dev/null 2>&1 || true
    docker run -d --name "$n" --privileged --hostname "$3" \
        --network "$BR_NET" --ip "$2" "$DIND_IMAGE" >/dev/null ||
        fatal "docker run $n"
    SUITE_DINDS="$SUITE_DINDS $n"
    i=0
    while ! docker exec "$n" docker info >/dev/null 2>&1; do
        i=$((i + 2))
        if [ "$i" -ge 90 ]; then
            docker logs "$n" --tail 40 || true
            fatal "inner dockerd of $n not ready within 90s"
        fi
        sleep 2
    done
    nl "dind $n ready (engine $(docker exec "$n" docker version --format '{{.Server.Version}}' 2>/dev/null))"
}

dind_up "$MGR" "$MGR_IP" mgr
dind_up "$W1" 10.214.0.11 w1

nl 'staging binaries + config (exec+stdin)'
for d in "$MGR" "$W1"; do
    docker exec "$d" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
    stage "$d" "$MN_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
    stage "$d" "$MN_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
    docker exec "$d" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'
done

# fleetlyd 配置：离线 dind 形态——ACME 关闭位、git 面关闭、join.token_rotate
# 显式 auto（D-MN-1 缺省即 auto，写出作台账）；base_domain 留空 = join 向导
# 的 DNS/zot 断言面在 dind 无输入（SKIP 标注归真 VPS 形态）。
cat >"$TMP/config.yaml" <<'EOF'
addr: "0.0.0.0:8420"
grpc:
  addr: "127.0.0.1:8421"
state:
  db_path: "/var/lib/fleetly/fleetly.db"
secrets:
  key_path: "/var/lib/fleetly/fleetly.key"
build:
  cache_dir: "/var/lib/fleetly/build-cache"
  artifacts_dir: "/var/lib/fleetly/build-artifacts"
logs:
  dir: "/var/lib/fleetly/fleetly-logs"
ingress:
  token_file: "/var/lib/fleetly/fleetly-ingress.token"
  cert_dir: "/var/lib/fleetly/fleetly-certs"
  acme:
    enabled: false
engine:
  deploy_timeout_seconds: 300
  observe_seconds: 5
  replicas_below_seconds: 5
  poll_seconds: 1
  drift_interval_seconds: 3600
git:
  enabled: false
join:
  token_rotate: "auto"
logging:
  level: info
EOF
stage "$MGR" "$TMP/config.yaml" /opt/fleetly/etc/config.yaml

nl 'pre-pulling fixture images on both nodes (pinned digests)'
for d in "$MGR" "$W1"; do
    for img in "$ALPINE_IMG" "$TRAEFIK_IMG" "$WHOAMI_IMG"; do
        docker exec "$d" docker pull -q "$img" >/dev/null || fatal "pull $img on $d"
    done
done

# ───────────────────────────────────────────── A: 2 节点拓扑上线
nl '=== A: two-node topology (join, anchor, node.joined, token chain) ==='
m docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'
T0=$(m docker swarm join-token -q worker) || fatal 'read join token'
[ -n "$T0" ] || fatal 'empty join token'
nl "T0 (pre-join worker token) captured"

m sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/mr-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
MR_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if m sh -c 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        MR_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$MR_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/mr-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
MR_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
[ -n "$MR_TOKEN" ] || fatal 'empty bootstrap token'
nl 'fleetlyd live (liveness 200), bootstrap token read'
# ── v0.3 归属管道 fixture（rbac-teams §2.1/§2.3/§3.4）：注册 founder（首
# 用户 = 平台管理员 + 个人队 + 默认项目 default）→ 会话自服务铸用户 PAT
#（admin scope——founder 是平台管理员可达集；CLI 不消费会话 cookie）。
# 首次部署/建库经 FLEETLY_PROJECT=founder/default 显式携带项目归属；fcli
# 统一 env 注入。bootstrap token 已随首用户注册按设计吊销弃用。
CURLER="$MGR-curl"
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" sleep 100000 >/dev/null ||
    fatal "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://$MGR_IP:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'
docker exec "$CURLER" curl -s -c /tmp/jar -X POST "http://$MGR_IP:8420/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"founder@e2e.test","password":"founder-pass-1","display_name":"Founder"}' \
    >/dev/null || fatal 'founder register'
# W2-S5 夹具修正：凭据改铸 **machine 令牌**（平台级凭据 = 资源面 admin
# 等价，rbac-teams §2.3；W2-S4 起平台管理员在资源面被 ResolvePermission
# 短路为只读——founder 的用户 PAT 已不能再承担部署/资源写）。
MR_TOKEN=$(docker exec "$CURLER" curl -s -b /tmp/jar -X POST "http://$MGR_IP:8420/v1/tokens" \
    -H 'Content-Type: application/json' \
    -d '{"machine":true,"note":"e2e machine token","scopes":["admin"]}' | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$MR_TOKEN" ] || fatal 'machine token mint failed'
# curl helper 用毕即除（fixture 只承担注册与铸 token；避免钉住 bridge 网络影响后续套件）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
FOUNDER_TEAM=founder
FOUNDER_PRJ=default
FOUNDER_PROJECT="$FOUNDER_TEAM/$FOUNDER_PRJ"
# v0.3 三段命名（rbac-teams §4.3）：服务名 fleetly-<team>-<prj>-<app>-<svc>。
# 必须在 FOUNDER_TEAM/FOUNDER_PRJ 赋值后展开（卷名族公式不变——卷引用不带
# 前缀）。
MR_DB_SVC="fleetly-$FOUNDER_TEAM-$FOUNDER_PRJ-$APP-db"
nl 'founder registered (platform admin); PAT minted; project context '"'"'"$FOUNDER_PROJECT"'"'"''


W1_JOIN=$(w1sh "docker swarm join $MGR_IP:2377 --token $T0" >/dev/null 2>&1; echo $?)
assert "MN-A1 WORKER_JOIN_ACCEPTED" $([ "$W1_JOIN" -eq 0 ] && echo 0 || echo 1) "rc=$W1_JOIN"

if poll_until 60 ready_ge 2; then
    assert "MN-A2 CLUSTER_TWO_NODES_READY" 0
else
    m docker node ls || true
    assert "MN-A2 CLUSTER_TWO_NODES_READY" 1 "never 2 Ready within 60s"
fi

# 锚定（multi-node §2.7）：w1 被铸造平台 ID → nodes 观测缓存出现第二个
# platform_id（mgr 首拍已锚定自身）。
anchored_count() {
    n=$(fcli nodes list --json 2>/dev/null | grep -c '"platform_id"')
    [ "${n:-0}" -ge 2 ]
}
if poll_until 120 anchored_count; then
    assert "MN-A3 WORKER_ANCHORED_PLATFORM_ID" 0
else
    fcli nodes list --json || true
    assert "MN-A3 WORKER_ANCHORED_PLATFORM_ID" 1 "second platform_id never appeared within 120s"
fi

if poll_until 120 events_grep node.joined; then
    assert "MN-A4 NODE_JOINED_EVENT_PERSISTED" 0
else
    msh 'grep -c node.joined /tmp/mr-events.txt' || true
    assert "MN-A4 NODE_JOINED_EVENT_PERSISTED" 1 "node.joined missing from event stream"
fi

# D-MN-1 auto-rotate（缺省 auto）：锚定完成后 worker join token 被自动轮换
# ——黑盒证据 = 现读 token ≠ 入群时用的 T0。失败不重试、下拍有新锚定才再
# 触发，故预算给足（事件拍 ~1s / 全量拍 30s 两条路都要覆盖）。
token_changed() {
    cur=$(m docker swarm join-token -q worker 2>/dev/null)
    [ -n "$cur" ] && [ "$cur" != "$T0" ]
}
if poll_until 120 token_changed; then
    assert "MN-A5 AUTO_ROTATE_FIRED_AFTER_ANCHORING" 0
else
    assert "MN-A5 AUTO_ROTATE_FIRED_AFTER_ANCHORING" 1 "join token unchanged 120s after anchoring (auto-rotate chain broken?)"
fi
T1=$(m docker swarm join-token -q worker) || fatal 'read rotated token'

# join 向导完成判据之一：fleetly-ingress（global）任务在 worker 上 running。
ingress_on_w1() {
    msh "docker service ps fleetly-ingress --format '{{.Node}} {{.CurrentState}}' | grep -q '^w1 Running'"
}
if poll_until 240 ingress_on_w1; then
    assert "MN-A6 INGRESS_TASK_RUNNING_ON_WORKER" 0
else
    m docker service ps fleetly-ingress --no-trunc || true
    assert "MN-A6 INGRESS_TASK_RUNNING_ON_WORKER" 1 "fleetly-ingress task never ran on w1"
fi

# 人工显式轮换路径（E1-8 CLI）：旧 token（T1）即刻失效、新 token 可用。
ROT_OUT=$(fcli nodes rotate-token --json 2>&1)
T2=$(printf '%s' "$ROT_OUT" | grep -o '"token": *"[^"]*"' | head -n 1 | sed 's/.*: *"//; s/"$//')
[ -n "$T2" ] && [ "$T2" != "$T1" ]
assert "MN-A7 MANUAL_ROTATE_CHANGES_TOKEN" $?
nl "T1 (post auto-rotate) and T2 (manual rotate) differ; T2 issued by fleetly nodes rotate-token"

dind_up "$W2" 10.214.0.12 w2
w2sh "docker swarm join $MGR_IP:2377 --token $T1" >/dev/null 2>&1
W2_OLD_RC=$?
[ "$W2_OLD_RC" -ne 0 ]
assert "MN-A8 OLD_TOKEN_JOIN_REJECTED" $? "stale token join rc=$W2_OLD_RC (must be rejected)"
w2sh "docker swarm join $MGR_IP:2377 --token $T2" >/dev/null 2>&1
W2_NEW_RC=$?
[ "$W2_NEW_RC" -eq 0 ]
assert "MN-A9 ROTATED_TOKEN_JOIN_ACCEPTED" $? "rotated token join rc=$W2_NEW_RC"
poll_until 60 ready_ge 2 || true
# w2 用后即弃（B/C 断言回到纯双节点）：leave → 清 mgr 侧 Down 幽灵 → 删容器。
w2sh 'docker swarm leave --force' >/dev/null 2>&1 || true
i=0
until msh "docker node rm --force \$(docker node ls --quiet --filter hostname=w2) 2>/dev/null" >/dev/null 2>&1; do
    i=$((i + 2))
    [ "$i" -ge 30 ] && break
    sleep 2
done
docker rm -f "$W2" >/dev/null 2>&1 || true
if poll_until 60 ready_eq 2; then
    assert "MN-A10 W2_REMOVED_BACK_TO_TWO_NODES" 0
else
    m docker node ls || true
    assert "MN-A10 W2_REMOVED_BACK_TO_TWO_NODES" 1 "cluster did not return to exactly 2 Ready nodes"
fi

nl "SKIP MN-A-EXT zot/registry + platform subdomain DNS verify + idle budget: dind has no base_domain/public DNS — belongs to the real VPS form (design §4.2 assertion A tail)"

# ───────────────────────────────────── B: 无状态 drain（任务层）
nl '=== B: stateless drain (task-level dind form) ==='
msh 'docker network create --driver overlay mrnet' >/dev/null 2>&1 || fatal 'overlay create'
m sh -c "docker service create --name mrwhoami --network mrnet --replicas 2 $WHOAMI_IMG" >/dev/null ||
    fatal 'create mrwhoami'
whoami_running() {
    n=$(msh "docker service ps mrwhoami --format '{{.CurrentState}}' | grep -c '^Running'" 2>/dev/null)
    [ "${n:-0}" -eq 2 ]
}
if poll_until 120 whoami_running; then
    assert "MN-B1 WHOAMI_TWO_REPLICAS_RUNNING" 0
else
    m docker service ps mrwhoami --no-trunc || true
    assert "MN-B1 WHOAMI_TWO_REPLICAS_RUNNING" 1 "never 2 running tasks within 120s"
fi
whoami_spread() {
    n=$(msh "docker service ps mrwhoami --format '{{.Node}} {{.CurrentState}}' | grep ' Running' | cut -d' ' -f1 | sort -u | wc -l" 2>/dev/null)
    [ "${n:-0}" -eq 2 ]
}
if poll_until 60 whoami_spread; then
    assert "MN-B2 WHOAMI_SPREAD_BOTH_NODES" 0
else
    m docker service ps mrwhoami || true
    assert "MN-B2 WHOAMI_SPREAD_BOTH_NODES" 1 "replicas did not spread across both nodes"
fi

nl 'draining w1 (maintenance window, swarm-level drain)'
m docker node update --availability drain w1 >/dev/null
# 断言 dind 适配口径：任务迁移 = Running==2 且全部落 mgr（新任务在另一节点
# running）；「新连接零失败」外部探测归真 VPS 形态（输出 SKIP 行）。
whoami_all_on_mgr() {
    n=$(msh "docker service ps mrwhoami --format '{{.Node}} {{.CurrentState}}' | grep -c '^mgr Running'" 2>/dev/null)
    t=$(msh "docker service ps mrwhoami --format '{{.CurrentState}}' | grep -c '^Running'" 2>/dev/null)
    [ "${n:-0}" -eq 2 ] && [ "${t:-0}" -eq 2 ]
}
if poll_until 180 whoami_all_on_mgr; then
    assert "MN-B3 DRAIN_MIGRATES_TASKS_TO_OTHER_NODE" 0
else
    m docker service ps mrwhoami --no-trunc || true
    assert "MN-B3 DRAIN_MIGRATES_TASKS_TO_OTHER_NODE" 1 "tasks did not fully migrate to mgr within 180s"
fi
skip "MN-B-EXT zero-failed-new-connection external probe: DNS-removal + external probe loop is the real VPS form; dind asserts the task layer (design §4.2 assertion B note)"
m docker node update --availability active w1 >/dev/null
nl "note: no auto-rebalance back to w1 (Swarm semantics; design §4.2 assertion B tail)"
m docker service rm mrwhoami >/dev/null 2>&1 || true
msh 'docker network rm mrnet' >/dev/null 2>&1 || true
# ───────────────────────── C: 有状态 drain→回岗（经平台全链）
nl '=== C: stateful drain -> rebind (platform-driven full chain) ==='
# compose：命名卷 + placement 钉 w1 + 「marker 文件 = 健康门」healthcheck
# ——数据在、应用才算健康：marker 缺位时任务永不 healthy，部署按住
# releasing，drain 因此确定性地落在观测窗口内（placement.blocked 的触发域，
# engine watchBoundNode 只在 releasing 全程生效）。
cat >"$TMP/stateful-compose.yaml" <<EOF
name: $APP
services:
  db:
    image: $ALPINE_IMG
    command: ["sleep", "31536000"]
    volumes:
      - "statedata:/data"
    healthcheck:
      test: ["CMD-SHELL", "test -f /data/marker.txt"]
      interval: 2s
      timeout: 1s
      retries: 1000
      start_period: 0s
    labels:
      fleetly.placement.node: "w1"
volumes:
  statedata:
    driver: local
EOF
stage "$MGR" "$TMP/stateful-compose.yaml" /opt/fleetly/stateful-compose.yaml

docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$MR_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$MGR" sh -c '/opt/fleetly/bin/fleetly deploy --timeout 600s /opt/fleetly/stateful-compose.yaml > /tmp/mr-c-deploy.log 2>&1'
# C1：部署进入 releasing（preparing 的 Preflight 已放行——此刻 drain 必然
# 落在 watchBoundNode 的观测域）。
deploy_status() {
    fcli deployments list --json "$APP" 2>/dev/null | grep -o '"status": *"[a-z]*"' | head -n 1
}
deploy_in_releasing() {
    [ "$(deploy_status)" = '"status": "releasing"' ]
}
if poll_until 90 deploy_in_releasing; then
    assert "MN-C1 DEPLOY_IN_RELEASING" 0
else
    fcli deployments list --json "$APP" 2>/dev/null || true
    msh 'cat /tmp/mr-c-deploy.log' || true
    assert "MN-C1 DEPLOY_IN_RELEASING" 1 "deployment never reached releasing (status=$(deploy_status))"
fi
# C2：任务已起在 w1（healthcheck 按住 → 停在 Starting/Unhealthy，永不切流）。
task_on_w1() {
    msh "docker service ps $MR_DB_SVC --format '{{.Node}} {{.CurrentState}}' | grep -q '^w1 '" 2>/dev/null
}
if poll_until 90 task_on_w1; then
    assert "MN-C2 TASK_STARTED_ON_W1" 0
else
    msh "docker service ps $MR_DB_SVC --no-trunc" || true
    assert "MN-C2 TASK_STARTED_ON_W1" 1 "db task never appeared on w1"
fi

nl 'draining w1 (blocked phase: platform sees drained binding node during releasing)'
m docker node update --availability drain w1 >/dev/null
if poll_until 90 events_grep placement.blocked; then
    assert "MN-C3 PLACEMENT_BLOCKED_EVENT" 0
else
    assert "MN-C3 PLACEMENT_BLOCKED_EVENT" 1 "placement.blocked never emitted within 90s"
fi
deploy_blocked() {
    fcli deployments list --json "$APP" 2>/dev/null | grep -q '"phase": *"blocked_waiting"'
}
if poll_until 60 deploy_blocked; then
    assert "MN-C4 DEPLOYMENT_BLOCKED_WAITING" 0
else
    assert "MN-C4 DEPLOYMENT_BLOCKED_WAITING" 1 "phase never became blocked_waiting"
fi
blocked_task_shape() {
    r=$(msh "docker service ps $MR_DB_SVC --format '{{.CurrentState}}'" 2>/dev/null)
    run=$(printf '%s' "$r" | grep -c '^Running')
    pend=$(printf '%s' "$r" | grep -c 'Pending')
    [ "${run:-0}" -eq 0 ] && [ "${pend:-0}" -ge 1 ]
}
if poll_until 60 blocked_task_shape; then
    assert "MN-C5 TASK_PENDING_WHILE_BLOCKED" 0
else
    msh "docker service ps $MR_DB_SVC" || true
    assert "MN-C5 TASK_PENDING_WHILE_BLOCKED" 1 "expected 0 Running + >=1 Pending under drain"
fi

# marker 写入卷（卷随 w1 本地存活；此时平台任务 PENDING，卷空闲可写）。
MR_VOL=$(w1sh "docker volume ls --format '{{.Name}}' | grep '^fleetly-$APP-statedata-' | head -n 1" | tr -d '\r')
[ -n "$MR_VOL" ] || {
    w1sh 'docker volume ls' || true
    fatal 'platform volume not found on w1'
}
MARKER="mrk-$(date +%s)"
w1sh "docker run --rm -v $MR_VOL:/data $ALPINE_IMG sh -c \"printf %s $MARKER > /data/marker.txt\"" || fatal 'marker write'
nl "marker '$MARKER' written into volume $MR_VOL on w1"

nl 'activating w1 (expected: placement.recovered + auto rebind + data intact)'
m docker node update --availability active w1 >/dev/null
if poll_until 120 events_grep placement.recovered; then
    assert "MN-C6 PLACEMENT_RECOVERED_EVENT" 0
else
    assert "MN-C6 PLACEMENT_RECOVERED_EVENT" 1 "placement.recovered never emitted within 120s"
fi
deploy_succeeded() {
    [ "$(deploy_status)" = '"status": "succeeded"' ]
}
if poll_until 300 deploy_succeeded; then
    assert "MN-C7 REDEPLOY_CONVERGED_SUCCEEDED" 0
else
    fcli deployments list --json "$APP" 2>/dev/null || true
    msh 'cat /tmp/mr-c-deploy.log' || true
    assert "MN-C7 REDEPLOY_CONVERGED_SUCCEEDED" 1 "deployment never succeeded after recovery (status=$(deploy_status))"
fi
task_back_on_w1() {
    msh "docker service ps $MR_DB_SVC --format '{{.Node}} {{.CurrentState}}' | grep -q '^w1 Running'" 2>/dev/null
}
if poll_until 120 task_back_on_w1; then
    assert "MN-C8 TASK_REBOUND_TO_W1" 0
else
    msh "docker service ps $MR_DB_SVC --no-trunc" || true
    assert "MN-C8 TASK_REBOUND_TO_W1" 1 "task never back Running on w1"
fi
GOT=$(w1sh "docker run --rm -v $MR_VOL:/data $ALPINE_IMG cat /data/marker.txt" 2>/dev/null | tr -d '\r\n')
[ "$GOT" = "$MARKER" ]
assert "MN-C9 MARKER_INTACT_AFTER_ROUNDTRIP" $? "want=$MARKER got=$GOT"

msh "docker service rm $MR_DB_SVC" >/dev/null 2>&1 || true
finish
