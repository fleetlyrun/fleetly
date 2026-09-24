#!/bin/sh
# e2e/databases.sh — E4 数据库托管端到端（单节点 dind 形态；设计
# docs/design/2026-09-20-managed-databases.md §4 验收步 3-8 的真机闭环，
# Console 面由 vitest 锚点测试承担）：
#
# 编排（宿主侧，自足；s3-rustfs.sh 同骨架）：
#   交叉编译 linux/amd64 fleetlyd+fleetly（或复用 DB_BIN_DIR）→ 宿主 bridge
#   上起一个特权 dind（私网 10.216.0.0/24，镜像钉 digest）→ swarm init +
#   fleetlyd 起服 → 全部断言经 dind 内的 docker/fleetly CLI 驱动：
#
#   D1  建库受理（create → provisioning）+ 健康门收敛 ready（真 postgres
#       容器过 pg_isready 健康门）。
#   D2  卷登记 + 放置钉住（volume active + placement 非空）。
#   D3  RustFS 备份目标收敛（s3 set --mode rustfs → fleetly-rustfs running
#       → 探针 put→get→delete 通过——库备份走同一 restic 目标）。
#   D4  引用 app 部署（compose label fleetly.databases + dbtools 镜像作 app
#       镜像：psql 起表写行后 sleep——部署 succeeded = 真连接读写成功）。
#   D5  注入断言（FLEETLY_DB_PG_PROD_{URL,HOST,PASSWORD} 在容器 env 实在）。
#   D6  手动备份受理 → 台账 verify_status=verified（真 restic 往返）。
#   D7  破坏性清空（psql DELETE）→ 行断言 0。
#   D8  原地恢复（confirm 两段式）→ 停库重放完成 → 行断言 1（备份时刻态）。
#   D9  凭据轮换（confirm 两段式）→ 指纹变化 + 引用 app 自动重部署 succeeded
#       + app 经新凭据再写一行（psql count=2 = 新凭据端到端可用）。
#   D10 平台密钥库（secrets set → external secret 声明部署 → /run/secrets
#       内容断言）。
#   D11 secret 移除后引用方再部署诚实失败（E_SECRET_NOT_FOUND）。
#   D12 secret 重设 → 重部署恢复 + /run/secrets 再断言。
#   D13 暂停/恢复（suspend → paused + replicas 0/1 → resume → ready）。
#   D14 删除守卫（引用在册 → E_DB_REFERENCED 409 诚实拒绝）。
#   D15 解除引用重部署后删除（默认留卷）→ 实例 reap 消失 + 服务移除 +
#       数据卷 orphaned 保留。
#
# 断言风格与 e2e/s3-rustfs.sh 一致（D*: PASS/FAIL 行 + NL_FAIL 计数 + finish）。
# usage: e2e/databases.sh
# env:
#   DB_DIND_IMAGE  dind 镜像（默认 docker:29.8.1-dind，钉 digest 与 CI 一致）
#   DB_SKIP_BUILD  1 = 跳过交叉编译，改用 DB_BIN_DIR 下的现成二进制
#   DB_BIN_DIR     DB_SKIP_BUILD=1 时的二进制来源（需含 fleetlyd 与 fleetly）
#   DB_VERSION     注入的版本串（默认 v0.2.0-db-e2e）
#
# 私有镜像注意（dbtools）：备份 job 与 dbapp 的运行载体 ghcr.io/fleetlyrun/
# dbtools 是 PRIVATE ghcr 包，引用为 tag@digest 钉定形态（CI 首推
# 2026-09-23 完成）——**必须** DB_GHCR_USER/DB_GHCR_TOKEN（packages:read）
# 供 dind 内登录直拉（真实 pull 落 RepoDigests；本地构建/导入解析不了
# digest 引用——W4 实测教训，无凭据腿已随 digest 收紧退役为 fatal）；
# CI（nightly databases-e2e）恒有 GITHUB_TOKEN。
set -u
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

# ── 镜像钉 digest（T0-V2.3 供应链；台账见 docs/runbooks/image-prepull.md，
# postgres/redis/restic 与 internal/dbtemplate、Dockerfile.dbtools 同源钉定；
# dbtools 本体 = CI 首推后的 tag@digest 钉定引用，与 Go 常量逐字一致）。
DIND_IMAGE="${DB_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"
ALPINE_IMG='alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc'
# curl helper（v0.3 fixture：REST 注册/铸 PAT 面——auth.sh 同源钉版）。
CURL_IMAGE='curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69'
PG_IMG='postgres:16@sha256:a3b7f434b2dc57ce85a67e171163eb8ab1a1ebcb39d27484661f26b1dfbe30d6'
REDIS_IMG='redis:7@sha256:c6eabf748fc7a61dbb5a705c78bcf3d6377b1127a97d0ce965c11c44ba46896f'
RESTIC_IMG='restic/restic:0.19.1@sha256:136600b6ff6843d61d355f7f71f460a166429f35de6fd11b568fece3c9a4d510'
# dbtools（私有 ghcr 包）：internal/database DefaultDatabaseToolsImage 同串。
DBTOOLS_IMG='ghcr.io/fleetlyrun/dbtools:v0.2.1-dbtools.1@sha256:472e8a5dd6b7ab2722caa996f18fec956203f99aafec0dd4c10cb82d262e866b'
DB_SKIP_BUILD="${DB_SKIP_BUILD:-0}"
DB_BIN_DIR="${DB_BIN_DIR:-}"
DB_VERSION="${DB_VERSION:-v0.2.0-db-e2e}"

BR_NET=fleetly-db-br
BR_SUBNET=10.216.0.0/24
DIND=fleetly-db-e2e-dind
DB=pg-prod
# v0.3 三段库族命名（rbac-teams §4.3）：fleetly-db-<team>-<prj>-<name>-<svc>。
# 在 FOUNDER_TEAM/FOUNDER_PRJ 赋值后展开（消费点都在 suspend 段之后）。
DBSVC=''
DBAPP=dbapp
SECAPP=secapp
SECSVC=agent
SECRET_NAME=app_token
SECRET_VALUE="db-e2e-token-$(date +%s)"

NL_FAIL=0
SUITE_DINDS=''
ACTIVE_NET=''

nl() { printf '[db-e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    NL_FAIL=$((NL_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "${3:-}"; fi
}
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

m() { docker exec "$DIND" "$@"; }         # dind 内直跑
msh() { docker exec "$DIND" sh -c "$*"; } # dind 内跑 shell 段
# fcli <args...> — dind 内的 fleetly CLI（gRPC 面 + bootstrap token）。
fcli() {
    docker exec -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" \
        -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
        "$DIND" /opt/fleetly/bin/fleetly "$@"
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
        nl "suite RED (rc=$rc) — dumping dind + fleetlyd log tails"
        docker logs "$DIND" --tail 40 2>&1 | tail -20 || true
        # fleetlyd 日志按信号词过滤（轮询类 INFO 会淹没尾部——取 warn/error
        # 与 backup/restore/dbjob 相关行，job 失败原文在此）。
        docker exec "$DIND" sh -c "grep -E 'database: backup|database: restore|database: upgrade|dbjob|\\\"level\\\":\\\"WARN\\\"|\\\"level\\\":\\\"ERROR\\\"' /tmp/db-fleetlyd.log 2>/dev/null | tail -40" || true
        if [ "${DB_KEEP_DIND:-0}" = "1" ]; then
            nl "DB_KEEP_DIND=1 — dind $DIND kept for forensics (remove with: docker rm -f $DIND)"
            return
        fi
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
    *.sh | *.yaml | *.yml | *Dockerfile*)
        docker exec "$d" sed -i 's/\r$//' "$r" || fatal "strip CR from $r"
        ;;
    esac
}

posix_path() { printf '%s' "$1" | tr '\\' '/'; }
SELF=$(posix_path "$0")
ROOT=$(CDPATH= cd -- "$(dirname -- "$SELF")/.." && pwd)
# mktemp 可能返回 MSYS 虚拟路径（/tmp/…）——原生 go.exe 在 MSYS_NO_PATHCONV=1
# 下拿不到映射；经 cygpath -m 归一为 C:/… 混合形态（sh/go/docker 三方都认）。
TMP=$(posix_path "$(mktemp -d)")
case "$TMP" in
/*)
    if command -v cygpath >/dev/null 2>&1; then
        TMP=$(cygpath -m "$TMP") || fatal 'cygpath -m on tmpdir'
    fi
    ;;
esac

command -v docker >/dev/null 2>&1 || fatal 'docker not on PATH'
command -v go >/dev/null 2>&1 || DB_SKIP_BUILD=1

# ─────────────────────────────────────────────────────────────── 构建
if [ "$DB_SKIP_BUILD" != '1' ]; then
    nl "cross-compiling linux/amd64 fleetlyd+fleetly ($DB_VERSION)"
    (
        cd "$ROOT" &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$DB_VERSION" \
                -o "$TMP/fleetlyd" ./cmd/fleetlyd &&
            GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                go build -trimpath -ldflags "-s -w -X main.version=$DB_VERSION" \
                -o "$TMP/fleetly" ./cmd/fleetly
    ) || fatal 'go build failed'
    DB_BIN_DIR="$TMP"
else
    DB_BIN_DIR="${DB_BIN_DIR:?DB_SKIP_BUILD=1 requires DB_BIN_DIR}"
    nl "using prebuilt binaries from $DB_BIN_DIR"
fi
[ -f "$DB_BIN_DIR/fleetlyd" ] || fatal "fleetlyd missing in $DB_BIN_DIR"
[ -f "$DB_BIN_DIR/fleetly" ] || fatal "fleetly missing in $DB_BIN_DIR"

# ─────────────────────────────────────────────────────── dind 编排准备
# 防御性清扫：上次崩溃残留会污染断言。
leftovers=$(docker ps -aq --filter name=fleetly-db-e2e- 2>/dev/null || true)
if [ -n "$leftovers" ]; then
    nl "WARN removing leftover db-e2e containers: $leftovers"
    echo "$leftovers" | xargs docker rm -f >/dev/null 2>&1 || true
fi
docker network rm "$BR_NET" >/dev/null 2>&1 || true
docker network create -d bridge --subnet "$BR_SUBNET" "$BR_NET" >/dev/null || fatal "create $BR_NET"
ACTIVE_NET="$BR_NET"

docker rm -f "$DIND" >/dev/null 2>&1 || true
docker run -d --name "$DIND" --privileged --hostname mgr \
    --network "$BR_NET" --ip 10.216.0.10 "$DIND_IMAGE" >/dev/null ||
    fatal "docker run $DIND"
SUITE_DINDS="$DIND"
i=0
while ! docker exec "$DIND" docker info >/dev/null 2>&1; do
    i=$((i + 2))
    if [ "$i" -ge 90 ]; then
        docker logs "$DIND" --tail 40 || true
        fatal 'inner dockerd not ready within 90s'
    fi
    sleep 2
done
nl "dind $DIND ready (engine $(docker exec "$DIND" docker version --format '{{.Server.Version}}' 2>/dev/null))"

nl 'staging binaries + config (exec+stdin)'
docker exec "$DIND" mkdir -p /opt/fleetly/bin /opt/fleetly/etc /var/lib/fleetly || fatal 'mkdir stage'
stage "$DIND" "$DB_BIN_DIR/fleetlyd" /opt/fleetly/bin/fleetlyd
stage "$DIND" "$DB_BIN_DIR/fleetly" /opt/fleetly/bin/fleetly
docker exec "$DIND" chmod +x /opt/fleetly/bin/fleetlyd /opt/fleetly/bin/fleetly || fatal 'chmod'

# fleetlyd 配置：离线 dind 形态——ACME/git 关闭、base_domain 留空（与
# s3-rustfs.sh 同形；备份目标 = 本 dind 内的托管 RustFS）。
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
logging:
  level: info
EOF
stage "$DIND" "$TMP/config.yaml" /opt/fleetly/etc/config.yaml

nl 'pre-pulling fixture images (public pinned digests; 3 attempts each)'
# PG/REDIS/RESTIC 同时是本地构建 dbtools（无凭据腿）的基底/COPY 来源——
# 预拉后 dind 内 build 离线可解析 FROM 与 COPY --from。
for img in "$ALPINE_IMG" "$PG_IMG" "$REDIS_IMG" "$RESTIC_IMG"; do
    ok=0
    for attempt in 1 2 3; do
        if docker exec "$DIND" docker pull -q "$img" >/dev/null; then
            ok=1
            break
        fi
        nl "pull attempt $attempt failed for $img (transient registry/network flake)"
        sleep 5
    done
    [ "$ok" -eq 1 ] || fatal "pull $img (3 attempts exhausted)"
done

# dbtools（私有 ghcr 包）：有凭据 → dind 内登录 + 直拉真镜像（tag@digest
# 全引用——真实 pull 才落 RepoDigests；CI 首推 2026-09-23 已完成）。无凭据
# → 诚实 fatal：digest 钉定引用的本地解析依赖真实 pull 的 RepoDigests，
# 本地构建同名 tag 解析不了（docker 29.8.1 实测，W4 头注教训）——中间态
# 的本地构建腿随 digest 收紧一并退役。
if [ -n "${DB_GHCR_USER:-}" ] && [ -n "${DB_GHCR_TOKEN:-}" ]; then
    docker exec -e GHCR_USER="$DB_GHCR_USER" -e GHCR_TOKEN="$DB_GHCR_TOKEN" \
        "$DIND" sh -c 'printf %s "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin >/dev/null' ||
        fatal 'docker login ghcr.io (inside dind) failed'
    docker exec "$DIND" docker pull -q "$DBTOOLS_IMG" >/dev/null ||
        fatal "pull $DBTOOLS_IMG (inside dind) failed — check the ghcr credential and its packages:read access to this private image"
    docker exec "$DIND" docker logout ghcr.io >/dev/null 2>&1 || true
    nl "dbtools image pulled inside dind ($DBTOOLS_IMG)"
else
    fatal "dbtools ($DBTOOLS_IMG) is a PRIVATE ghcr package with a digest-pinned reference: set DB_GHCR_USER/DB_GHCR_TOKEN (a ghcr credential with packages:read on fleetlyrun/dbtools) so the dind can pull it directly — local builds cannot satisfy a digest pin (no RepoDigest). In CI the databases-e2e job passes GITHUB_TOKEN automatically."
fi

docker exec "$DIND" docker swarm init --advertise-addr eth0 >/dev/null || fatal 'swarm init'

docker exec "$DIND" sh -c 'cd /var/lib/fleetly && nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml > /tmp/db-fleetlyd.log 2>&1 & echo $! > /var/run/fleetlyd.pid'
DB_LIVE=0
i=0
while [ "$i" -lt 90 ]; do
    if m sh -c 'wget -q -T 3 -O /dev/null http://127.0.0.1:8420/healthz/liveness' 2>/dev/null; then
        DB_LIVE=1
        break
    fi
    i=$((i + 2))
    sleep 2
done
[ "$DB_LIVE" -eq 1 ] || {
    msh 'tail -40 /tmp/db-fleetlyd.log' || true
    fatal 'fleetlyd not live within 90s'
}
DB_TOKEN=$(m sh -c 'cat /var/lib/fleetly/bootstrap-token') || fatal 'read bootstrap token'
[ -n "$DB_TOKEN" ] || fatal 'empty bootstrap token'
nl 'fleetlyd live (liveness 200), bootstrap token read'

# ── v0.3 归属管道 fixture（rbac-teams §2.1/§2.3/§3.4）：注册 founder（首
# 用户 = 平台管理员 + 个人队 + 默认项目 default）→ 会话自服务铸用户 PAT
#（admin scope——founder 是平台管理员可达集；CLI 不消费会话 cookie）。
# 首次部署/建库经 FLEETLY_PROJECT=founder/default 显式携带项目归属；raw
# docker exec 驱动的 deploy 同样注入该 env。bootstrap token 弃用。
CURLER="$DIND-curl"
docker rm -f "$CURLER" >/dev/null 2>&1 || true
docker run -d --name "$CURLER" --network "$BR_NET" "$CURL_IMAGE" sleep 100000 >/dev/null ||
    fatal "docker run $CURLER"
docker exec "$CURLER" curl -s -o /dev/null "http://10.216.0.10:8420/healthz/liveness" ||
    fatal 'curl helper cannot reach the REST face'
docker exec "$CURLER" curl -s -c /tmp/jar -X POST "http://10.216.0.10:8420/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d '{"email":"founder@e2e.test","password":"founder-pass-1","display_name":"Founder"}' \
    >/dev/null || fatal 'founder register'
# W2-S5 夹具修正：凭据改铸 **machine 令牌**（平台级凭据 = 资源面 admin
# 等价，rbac-teams §2.3；W2-S4 起平台管理员在资源面被 ResolvePermission
# 短路为只读——founder 的用户 PAT 已不能再承担建库/部署等资源写）。
DB_TOKEN=$(docker exec "$CURLER" curl -s -b /tmp/jar -X POST "http://10.216.0.10:8420/v1/tokens" \
    -H 'Content-Type: application/json' \
    -d '{"machine":true,"note":"e2e machine token","scopes":["admin"]}' | grep -oE '"token": ?"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$DB_TOKEN" ] || fatal 'machine token mint failed'
# curl helper 用毕即除（fixture 只承担注册与铸 token；避免钉住 bridge 网络影响后续套件）。
docker rm -f "$CURLER" >/dev/null 2>&1 || true
FOUNDER_TEAM=founder
FOUNDER_PRJ=default
DBSVC="fleetly-db-$FOUNDER_TEAM-$FOUNDER_PRJ-$DB-postgres"
FOUNDER_PROJECT="$FOUNDER_TEAM/$FOUNDER_PRJ"
nl 'founder registered (platform admin); machine token minted; project context '"$FOUNDER_PROJECT"

# app 容器 id（每次重部署后变化——逐次现查；服务名 = 三段命名公式）。
dbapp_ctr() {
    msh "docker ps -q --filter label=com.docker.swarm.service.name=fleetly-$FOUNDER_TEAM-$FOUNDER_PRJ-$DBAPP-writer | head -n 1" | tr -d '\r'
}
secapp_ctr() {
    msh "docker ps -q --filter label=com.docker.swarm.service.name=fleetly-$FOUNDER_TEAM-$FOUNDER_PRJ-$SECAPP-$SECSVC | head -n 1" | tr -d '\r'
}
# db_psql <sql> — 经 dbapp 容器的 psql（走注入的 FLEETLY_DB_*_URL——注入链
# 与凭据正确性的端到端证据，不用宿主直连库容器）。
db_psql() { # <sql>
    CTR=$(dbapp_ctr)
    [ -n "$CTR" ] || return 1
    msh "docker exec $CTR sh -c 'psql \$FLEETLY_DB_PG_PROD_URL -t -A -q -c \"$1\"'" 2>/dev/null |
        tr -d '\r' | tr -d ' '
}
db_rowcount() {
    db_psql 'SELECT count(*) FROM w4'
}
# db_fingerprint — 实例当前凭据指纹（show --json 的脱敏投影）。
db_fingerprint() {
    fcli databases show --json "$DB" 2>/dev/null |
        grep -o '"password_fingerprint": *"[0-9a-f]*"' | head -n 1
}
# latest_dep_status <app> — 该 app 最新一行部署的 status（deploymentJSON 字
# 段序 id,app,kind,status——首个 "status" 必属最新行；重部署轮询不能用
# 「列表里出现过 succeeded」判据，历史行会提前放行）。
latest_dep_status() {
    fcli deployments list --json "$1" 2>/dev/null |
        grep -o '"status": *"[^"]*"' | head -n 1
}

# ───────────────── D1: 建库受理 + 健康门收敛（真 postgres 容器）
nl "=== D1: create $DB -> provisioning -> ready (health gate through real postgres) ==="
D1_OUT=$(fcli databases create --json --template postgres-16 "$DB" 2>&1)
case "$D1_OUT" in
*'"status": "provisioning"'*) assert "DB-D1 CREATE_ACCEPTED_PROVISIONING" 0 ;;
*) fail "DB-D1 CREATE_ACCEPTED_PROVISIONING" "create output: $(printf '%s' "$D1_OUT" | tail -3)" ;;
esac

db_ready() {
    fcli databases show --json "$DB" 2>/dev/null | grep -q '"status": "ready"'
}
if poll_until 300 db_ready; then
    assert "DB-D2 HEALTH_GATE_READY" 0
else
    fcli databases show --json "$DB" || true
    assert "DB-D2 HEALTH_GATE_READY" 1 "instance never became ready within 300s"
fi

# ───────────────── D2: 卷登记 + 放置钉住
nl '=== D3: volume registered + placement pinned ==='
SHOW_JSON=$(fcli databases show --json "$DB" 2>/dev/null)
printf '%s' "$SHOW_JSON" | grep -q '"name": "fleetly-db-'"$DB"'-data-' &&
    printf '%s' "$SHOW_JSON" | grep -q '"status": "active"' &&
    printf '%s' "$SHOW_JSON" | grep -q '"placement": "[^"]'
assert "DB-D3 VOLUME_REGISTERED_PLACEMENT_PINNED" $? \
    "show json must carry fleetly-db-$DB-data-* volume (active) and a non-empty placement"

# ───────────────── D4(D3 in design): RustFS 备份目标收敛（库备份同目标）
nl '=== D4: rustfs backup target converges (managed rustfs + probe) ==='
fcli s3 set --mode rustfs >/dev/null 2>&1 || fatal 's3 set --mode rustfs'
rustfs_running() {
    msh "docker service ps fleetly-rustfs --format '{{.CurrentState}}' | grep -q '^Running'" 2>/dev/null
}
if poll_until 240 rustfs_running; then
    assert "DB-D4 RUSTFS_TARGET_RUNNING" 0
else
    m docker service ps fleetly-rustfs --no-trunc || true
    assert "DB-D4 RUSTFS_TARGET_RUNNING" 1 "fleetly-rustfs never ran within 240s"
fi
s3_test_ok() {
    fcli s3 test 2>/dev/null | grep -q 'connection test ok'
}
if poll_until 120 s3_test_ok; then
    assert "DB-D5 RUSTFS_PROBE_OK" 0
else
    fcli s3 test || true
    assert "DB-D5 RUSTFS_PROBE_OK" 1 "backup target probe never passed within 120s"
fi

# 库备份与控制面状态备份共享同一 restic repo——repo 口令由控制面上传轨
# 首次上传时惰性生成。库备份前置夹具：先做一次控制面备份（结论 upload
# ok = 口令在位 + 目标真实可写），否则库备份按设计诚实拒绝（可行动错误
# 文本指到此步）。
nl 'fixture: control-plane backup (provisions the shared restic repo password)'
fcli backups create >/dev/null 2>&1 || true
control_plane_backup_ok() {
    fcli backups list --json 2>/dev/null | grep -q '"upload_status": *"ok"'
}
if poll_until 300 control_plane_backup_ok; then
    nl 'fixture ready: repo password provisioned (control-plane backup upload ok)'
else
    fcli backups list --json || true
    fatal 'control-plane backup never reached upload ok within 300s (repo password provisioning gate)'
fi

# ───────────────── D5(D4): 引用 app 部署（label + dbtools psql 引导）
nl '=== D6: referencing app deploys (fleetly.databases label, psql bootstrap) ==='
cat >"$TMP/dbapp-compose.yaml" <<EOF
name: $DBAPP
services:
  writer:
    image: $DBTOOLS_IMG
    command: ["sh", "-c", "psql \"\$FLEETLY_DB_PG_PROD_URL\" -c 'CREATE TABLE IF NOT EXISTS w4 (id int)' && psql \"\$FLEETLY_DB_PG_PROD_URL\" -c 'INSERT INTO w4 VALUES (1)' && sleep infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 2s
      timeout: 1s
      retries: 100
      start_period: 0s
    labels:
      fleetly.databases: "$DB"
EOF
stage "$DIND" "$TMP/dbapp-compose.yaml" /opt/fleetly/dbapp-compose.yaml

DBAPP_DEPLOY_LOG=/tmp/db-dbapp-deploy.log
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c "/opt/fleetly/bin/fleetly deploy --timeout 300s /opt/fleetly/dbapp-compose.yaml > $DBAPP_DEPLOY_LOG 2>&1"
# dbapp_succeeded — dbapp 最新部署 succeeded（重部署轮询共用）。
dbapp_succeeded() {
    [ "$(latest_dep_status "$DBAPP")" = '"status": "succeeded"' ]
}
# secapp_succeeded — secapp 最新部署 succeeded（密钥库场景三次部署共用）。
secapp_succeeded() {
    [ "$(latest_dep_status "$SECAPP")" = '"status": "succeeded"' ]
}
if poll_until 300 dbapp_succeeded; then
    assert "DB-D6 APP_DEPLOY_SUCCEEDED" 0
else
    fcli deployments list --json "$DBAPP" || true
    msh "cat $DBAPP_DEPLOY_LOG" || true
    assert "DB-D6 APP_DEPLOY_SUCCEEDED" 1 "referencing app never succeeded (psql bootstrap or plan failed)"
fi

# ───────────────── D6(D5): 注入断言（容器 env 实在）
nl '=== D7: injected FLEETLY_DB_* env in the app container ==='
CTR=$(dbapp_ctr)
[ -n "$CTR" ] || fatal 'app container not found'
URL=$(msh "docker exec $CTR printenv FLEETLY_DB_PG_PROD_URL" | tr -d '\r')
HOSTV=$(msh "docker exec $CTR printenv FLEETLY_DB_PG_PROD_HOST" | tr -d '\r')
PW=$(msh "docker exec $CTR printenv FLEETLY_DB_PG_PROD_PASSWORD" | tr -d '\r')
case "$URL" in
postgres://fleetly:*@pg-prod:5432/pg_prod) INJ=0 ;;
*) INJ=1 ;;
esac
[ "$HOSTV" = "pg-prod" ] && [ -n "$PW" ] && [ "$INJ" = "0" ]
assert "DB-D7 INJECTED_ENV_VALUES" $? "url=$URL host=$HOSTV pw_set=$([ -n "$PW" ] && echo yes || echo no)"

# 引导行在库（psql 走注入 URL——整链真连接）。
CNT=$(db_rowcount)
[ "$CNT" = "1" ]
assert "DB-D8 BOOTSTRAP_ROW_COUNT" $? "want 1 row after the app bootstrap insert, got '$CNT'"

# ───────────────── D7(D6): 手动备份 → verified
nl '=== D9: manual backup -> ledger verify_status=verified (real restic roundtrip) ==='
fcli databases backup "$DB" >/dev/null 2>&1 || fatal 'databases backup (trigger)'
backup_verified() {
    fcli databases backups --json "$DB" 2>/dev/null | grep -q '"verify_status": *"verified"'
}
if poll_until 300 backup_verified; then
    assert "DB-D9 BACKUP_VERIFIED" 0
else
    fcli databases backups --json "$DB" || true
    assert "DB-D9 BACKUP_VERIFIED" 1 "no verified backup row within 300s"
fi

# ───────────────── D8(D7): 破坏性清空 → 恢复 → 行回来
# 恢复 = 单 job（v0.2.1-dbtools.1 debian 基底：dbtools 内 restic 取回 +
# 临时 postgres 重放；W4 双 job 形态随跨 libc 重放风险消除而回退）。断言
# 语义不变：恢复后行集回到备份时点（点时语义）+ 实例 ready。
nl '=== D10: destructive clear, then in-place restore brings the rows back ==='
db_psql 'DELETE FROM w4' >/dev/null || fatal 'psql DELETE'
CNT=$(db_rowcount)
[ "$CNT" = "0" ]
assert "DB-D10 DESTRUCTIVE_CLEAR" $? "after DELETE the count must be 0, got '$CNT'"

SNAP=$(fcli databases backups --json "$DB" 2>/dev/null |
    grep -o '"snapshot": *"[^"]*"' | head -n 1 | sed 's/.*: *"//; s/"$//')
[ -n "$SNAP" ] || fatal 'no snapshot id in the ledger'
# 受理轮询而非一次尝试：备份操作的互斥哨兵覆盖到 prune 尾部（台账 verified
# 行先于 endOp 可见）——受理撞在途操作（409 族）时随 prune 收口自然放行。
# 单 job 恢复 + debian 基底下每任务一次 registry 探测，时序比 alpine 时代
# 后移数秒，一次尝试不再稳收。
restore_accepted() {
    fcli databases restore --snapshot "$SNAP" --confirm "$DB" "$DB" >/dev/null 2>&1
}
if ! poll_until 60 restore_accepted; then
    fcli databases restore --snapshot "$SNAP" --confirm "$DB" "$DB" 2>&1 | tail -3 || true
    fatal 'databases restore (accept) never accepted within 60s'
fi
restore_done() {
    db_ready && [ "$(db_rowcount)" = "1" ]
}
if poll_until 420 restore_done; then
    assert "DB-D11 RESTORE_ROWS_BACK" 0
else
    fcli databases show --json "$DB" || true
    assert "DB-D11 RESTORE_ROWS_BACK" 1 "instance never returned to ready with 1 row within 420s"
fi

# ───────────────── D9(D5): 轮换 → 引用 app 自动重部署 → 新凭据可用
nl '=== D12: rotate credentials -> fingerprint changes, app auto-redeploys, new credential writes ==='
FP1=$(db_fingerprint)
BEFORE_ID=$(fcli deployments list --json "$DBAPP" 2>/dev/null | grep -o '"id": *"[^"]*"' | head -n 1)
ROT_OUT=$(fcli databases rotate --confirm "$DB" "$DB" 2>&1)
case "$ROT_OUT" in
*fingerprint*dbapp*) ROT_OK=0 ;;
*fingerprint*) ROT_OK=0 ;;
*) ROT_OK=1 ;;
esac
assert "DB-D12 ROTATE_ACCEPTED" "$ROT_OK" "rotate output: $(printf '%s' "$ROT_OUT" | tail -3)"
FP2=$(db_fingerprint)
[ -n "$FP1" ] && [ -n "$FP2" ] && [ "$FP1" != "$FP2" ]
assert "DB-D13 FINGERPRINT_CHANGED" $? "fp1=$FP1 fp2=$FP2"

redeployed_succeeded() {
    LATEST=$(fcli deployments list --json "$DBAPP" 2>/dev/null | grep -o '"id": *"[^"]*"' | head -n 1)
    [ -n "$LATEST" ] && [ "$LATEST" != "$BEFORE_ID" ] &&
        [ "$(latest_dep_status "$DBAPP")" = '"status": "succeeded"' ]
}
if poll_until 300 redeployed_succeeded; then
    assert "DB-D14 APP_AUTO_REDEPLOYED" 0
else
    fcli deployments list --json "$DBAPP" || true
    assert "DB-D14 APP_AUTO_REDEPLOYED" 1 "referencing app was not redeployed after rotation"
fi
# 引导命令在新任务上重跑（CREATE IF NOT EXISTS + INSERT）——经新凭据再写
# 一行：count=2 = 轮换后端到端写路径可用。重部署容器刚替换时 exec 可能暂
# 无目标容器（count 空）——轮询待新任务就绪。
count_two() {
    [ "$(db_rowcount)" = "2" ]
}
if poll_until 120 count_two; then
    assert "DB-D15 NEW_CREDENTIAL_WRITES" 0
else
    assert "DB-D15 NEW_CREDENTIAL_WRITES" 1 "want 2 rows after the redeployed app inserts via the NEW credential, got '$(db_rowcount)'"
fi

# ───────────────── D10-D12(§2.7): 平台密钥库 + external secret 部署
nl '=== D16: platform secret set -> external secret mounted at /run/secrets ==='
# 先建 app（SetSecret 以 app 存在为前置——404 族诚实拒绝），再加 secret
# 声明重部署（external: true 从平台密钥库解析）。
cat >"$TMP/secapp-compose.yaml" <<EOF
name: $SECAPP
services:
  $SECSVC:
    image: $ALPINE_IMG
    command: ["sleep", "31536000"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 2s
      timeout: 1s
      retries: 100
      start_period: 0s
EOF
stage "$DIND" "$TMP/secapp-compose.yaml" /opt/fleetly/secapp-compose.yaml
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c "/opt/fleetly/bin/fleetly deploy --timeout 300s /opt/fleetly/secapp-compose.yaml > /tmp/db-secapp-deploy0.log 2>&1"
if poll_until 300 secapp_succeeded; then
    nl 'fixture ready: secapp exists (secret-free bootstrap deploy)'
else
    fcli deployments list --json "$SECAPP" || true
    msh 'cat /tmp/db-secapp-deploy0.log' || true
    fatal 'secapp bootstrap deploy never succeeded'
fi
fcli secrets set --value "$SECRET_VALUE" "$SECAPP" "$SECRET_NAME" >/dev/null 2>&1 || fatal 'secrets set'
cat >"$TMP/secapp-compose.yaml" <<EOF
name: $SECAPP
services:
  $SECSVC:
    image: $ALPINE_IMG
    command: ["sleep", "31536000"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 2s
      timeout: 1s
      retries: 100
      start_period: 0s
    secrets: ["$SECRET_NAME"]
secrets:
  $SECRET_NAME:
    external: true
EOF
stage "$DIND" "$TMP/secapp-compose.yaml" /opt/fleetly/secapp-compose.yaml
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c "/opt/fleetly/bin/fleetly deploy --timeout 300s /opt/fleetly/secapp-compose.yaml > /tmp/db-secapp-deploy.log 2>&1"
if poll_until 300 secapp_succeeded; then
    assert "DB-D16 SECAPP_DEPLOY_SUCCEEDED" 0
else
    fcli deployments list --json "$SECAPP" || true
    msh 'cat /tmp/db-secapp-deploy.log' || true
    assert "DB-D16 SECAPP_DEPLOY_SUCCEEDED" 1 "secret-declaring app never succeeded"
fi
SCTR=$(secapp_ctr)
GOT=$(msh "docker exec $SCTR cat /run/secrets/$SECRET_NAME" 2>/dev/null | tr -d '\r')
[ "$GOT" = "$SECRET_VALUE" ]
assert "DB-D17 SECRET_MOUNTED_VERBATIM" $? "want the verbatim secret value at /run/secrets/$SECRET_NAME, got '$GOT'"

# ───────────────── D11: 移除 secret → 引用方再部署诚实失败
nl '=== D17: removed secret -> referencing deploy fails honestly (E_SECRET_NOT_FOUND) ==='
fcli secrets rm "$SECAPP" "$SECRET_NAME" >/dev/null 2>&1 || fatal 'secrets rm'
MISS_OUT=$(fcli deploy --timeout 300s /opt/fleetly/secapp-compose.yaml 2>&1)
case "$MISS_OUT" in
*E_SECRET_NOT_FOUND*) assert "DB-D18 SECRET_REMOVED_HONEST_FAIL" 0 ;;
*) fail "DB-D18 SECRET_REMOVED_HONEST_FAIL" "redeploy output missing E_SECRET_NOT_FOUND: $(printf '%s' "$MISS_OUT" | tail -3)" ;;
esac

# ───────────────── D12: 重设 → 重部署恢复
nl '=== D18: secret re-set -> redeploy recovers, mount re-asserted ==='
fcli secrets set --value "$SECRET_VALUE" "$SECAPP" "$SECRET_NAME" >/dev/null 2>&1 || fatal 'secrets set (re)'
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c "/opt/fleetly/bin/fleetly deploy --timeout 300s /opt/fleetly/secapp-compose.yaml > /tmp/db-secapp-deploy2.log 2>&1"
if poll_until 300 secapp_succeeded; then
    assert "DB-D19 SECRET_RESET_REDEPLOY_OK" 0
else
    fcli deployments list --json "$SECAPP" || true
    assert "DB-D19 SECRET_RESET_REDEPLOY_OK" 1 "redeploy after re-set never succeeded"
fi
SCTR=$(secapp_ctr)
GOT=$(msh "docker exec $SCTR cat /run/secrets/$SECRET_NAME" 2>/dev/null | tr -d '\r')
[ "$GOT" = "$SECRET_VALUE" ]
assert "DB-D20 SECRET_REMOUNTED" $? "want the secret remounted verbatim after recovery, got '$GOT'"

# ───────────────── D13(§2.3): 暂停/恢复
nl '=== D21: suspend -> paused + replicas 0/0; resume -> ready ==='
fcli databases suspend "$DB" >/dev/null 2>&1 || fatal 'databases suspend'
# swarm 的 Replicas 列 = desired/running：suspend 把期望副本收敛为 0，收敛
# 完成的显示即 0/0（不是 0/1——那是「spec 仍为 1、运行 0」的中间态口径）。
suspend_settled() {
    fcli databases show --json "$DB" 2>/dev/null | grep -q '"status": "paused"' &&
        msh "docker service ls --filter name=$DBSVC --format '{{.Replicas}}'" 2>/dev/null | tr -d '\r' | grep -q '^0/0$'
}
if poll_until 300 suspend_settled; then
    assert "DB-D21 SUSPEND_PAUSED_SCALE0" 0
else
    fcli databases show --json "$DB" || true
    msh "docker service ls --filter name=$DBSVC" || true
    assert "DB-D21 SUSPEND_PAUSED_SCALE0" 1 "suspend never settled at paused + 0/0 replicas within 300s"
fi
fcli databases resume "$DB" >/dev/null 2>&1 || fatal 'databases resume'
if poll_until 300 db_ready; then
    assert "DB-D22 RESUME_READY" 0
else
    fcli databases show --json "$DB" || true
    assert "DB-D22 RESUME_READY" 1 "resume never converged back to ready"
fi

# ───────────────── D14-D15(§2.3): 删除守卫 + 解除引用后删除（默认留卷）
nl '=== D23: delete guard -> E_DB_REFERENCED while referenced ==='
GUARD_OUT=$(fcli databases delete "$DB" 2>&1)
case "$GUARD_OUT" in
*E_DB_REFERENCED*) assert "DB-D23 DELETE_GUARD_409" 0 ;;
*) fail "DB-D23 DELETE_GUARD_409" "delete output missing E_DB_REFERENCED: $(printf '%s' "$GUARD_OUT" | tail -3)" ;;
esac

nl '=== D24: unreference + redeploy, then delete (volume kept as orphaned) ==='
cat >"$TMP/dbapp-compose2.yaml" <<EOF
name: $DBAPP
services:
  writer:
    image: $ALPINE_IMG
    command: ["sleep", "31536000"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 2s
      timeout: 1s
      retries: 100
      start_period: 0s
EOF
stage "$DIND" "$TMP/dbapp-compose2.yaml" /opt/fleetly/dbapp-compose2.yaml
docker exec -d -e FLEETLY_ADDR=127.0.0.1:8421 -e FLEETLY_TOKEN="$DB_TOKEN" -e FLEETLY_PROJECT="$FOUNDER_PROJECT" \
    "$DIND" sh -c "/opt/fleetly/bin/fleetly deploy --timeout 300s --confirm-destructive /opt/fleetly/dbapp-compose2.yaml > /tmp/db-dbapp-deploy2.log 2>&1"
if poll_until 300 dbapp_succeeded; then
    assert "DB-D24 UNREFERENCED_REDEPLOY_OK" 0
else
    fcli deployments list --json "$DBAPP" || true
    msh 'cat /tmp/db-dbapp-deploy2.log' || true
    assert "DB-D24 UNREFERENCED_REDEPLOY_OK" 1 "label-removed redeploy never succeeded"
fi

fcli databases delete "$DB" >/dev/null 2>&1 || fatal 'databases delete (accept)'
db_gone() {
    fcli databases list --json 2>/dev/null | grep -qv "\"name\": *\"$DB\""
}
if poll_until 240 db_gone; then
    assert "DB-D25 INSTANCE_REAPED" 0
else
    fcli databases list --json || true
    assert "DB-D25 INSTANCE_REAPED" 1 "instance still listed 240s after delete acceptance"
fi
service_gone() {
    msh "docker service ls --format '{{.Name}}'" 2>/dev/null | grep -q "^$DBSVC$"
    [ $? -ne 0 ]
}
if poll_until 120 service_gone; then
    assert "DB-D26 DB_SERVICE_REMOVED" 0
else
    m docker service ls || true
    assert "DB-D26 DB_SERVICE_REMOVED" 1 "managed database service still present after reap"
fi
msh "docker volume ls --format '{{.Name}}'" 2>/dev/null | grep -q "^fleetly-db-$DB-data-"
assert "DB-D27 DATA_VOLUME_ORPHANED_RETAINED" $? \
    "default delete keeps the data volume (orphaned) — explicit --delete-volumes is the only discard path"

finish
