#!/bin/sh
# deploy/cal-inner.sh — T2.25 资源与容量校准内层（docker:29.8.1-dind 容器内
# 执行；宿主编排见 run-calibration.sh）。注入面 $CAL_STAGE（默认 /tmp/cal）：
#   install.sh / fleetlyd / fleetly / hello（probeapp 静态二进制）/
#   probe.tar.gz（probeapp:1 镜像，宿主 docker save|gzip）。
# 产物：/tmp/cal-out/{summary.md,cal.json}（宿主 docker exec cat 读回）。
#
# 阶段（T2.25 验收标准）：
#   P1  安装（--bin-dir --no-systemd）+ 自写配置（ACME/git 关、快引擎参数）
#       + daemon live + bootstrap token + fleetly-ingress 1/1 + buildkit 收敛
#   P2  idle 基线：稳定窗（CAL_SETTLE，默认 120s）后采样——fleetlyd VmRSS
#       多拍（min/avg/max/last）+ dockerd RSS（swarmkit 同进程）+ containerd
#       RSS（如实单列）+ docker stats（fleetly-ingress / fleetly-buildkit 单列）
#   P3  容量压测 50 apps / 200 域名：顺序部署 cap01..cap50（前 20 个每应用
#       2 服务 × 5 域名 = 200 域名；后 30 个单服务无域名 → 共 70 services）
#       ——逐部署时延、全部 succeeded、derived_state=running、域名台账=200、
#       /configs 视图合成非空且含 200 域名、Traefik 路由 200 抽检、内存增量
#   P4  并发构建 2：CAL_BUILDS（默认 5）个独立 dockerfile 构建并发入队 →
#       采样器断言同时在建 ≤2 且出现过 queued → 全部 succeeded、digest 互异
#       （payload 逐构建随机）、镜像逐一可 inspect（无交叉污染）
#   P5  汇总产物（summary.md + cal.json）
#
# 断言风格与 deploy/test-upgrade.sh 一致（NAME: PASS/FAIL + 计数 + finish）。

set -u
# shellcheck disable=SC2034
CAL_FAIL=0

CAL_STAGE="${CAL_STAGE:-/tmp/cal}"
INSTALL_SH="$CAL_STAGE/install.sh"
DLOG="/tmp/cal-fleetlyd.log"
PID_FILE="/var/run/fleetlyd.pid"
OUT="/tmp/cal-out"
HTTP="http://127.0.0.1:8420"

SETTLE="${CAL_SETTLE:-120}"
SAMPLES="${CAL_SAMPLES:-12}"
INTERVAL="${CAL_INTERVAL:-5}"
N_APPS="${CAL_APPS:-50}"
N_DOMAIN_APPS="${CAL_DOMAIN_APPS:-20}"
N_BUILDS="${CAL_BUILDS:-5}"
VERSION="${CAL_VERSION:-v0.1.0-cal}"
DIND_TAG="${CAL_DIND_IMAGE:-docker:29.8.1-dind}"

APPS_DIR="$CAL_STAGE/apps"
BLD_DIR="$CAL_STAGE/builds"
LAT_PSV="$CAL_STAGE/deploy-latency.psv"
SAMP_PSV="$CAL_STAGE/build-samples.psv"

nl() { printf '[cal %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    CAL_FAIL=$((CAL_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then
        pass "$1"
    else
        fail "$1" "${3:-}"
    fi
}
finish() {
    if [ "$CAL_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$CAL_FAIL"
    nl "---- fleetlyd log tail ($DLOG) ----"
    tail -n 50 "$DLOG" 2>/dev/null || true
    exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

http_code() { # <url> [host] — dind 无 curl；busybox wget -S 状态行走 stderr
    if have curl; then
        if [ -n "${2:-}" ]; then
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: $2" "$1" 2>/dev/null || printf '000'
        else
            curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$1" 2>/dev/null || printf '000'
        fi
    elif have wget; then
        if [ -n "${2:-}" ]; then
            wget -q -S -T 5 -O /dev/null --header "Host: $2" "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        else
            wget -q -S -T 5 -O /dev/null "$1" 2>&1 |
                awk 'NR==1 {gsub(/^[[:space:]]*HTTP\/[0-9.]+[[:space:]]*/, ""); print $1; exit}' || printf '000'
        fi
    else
        printf '000'
    fi
}

json_str() { # <json> <key> — 定点字段提取（indent JSON）
    printf '%s' "$1" |
        grep -o "\"$2\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/.*:[[:space:]]*"//; s/"$//'
}
json_num() {
    printf '%s' "$1" |
        grep -o "\"$2\"[[:space:]]*:[[:space:]]*[0-9][0-9]*" |
        head -n 1 |
        sed 's/.*:[[:space:]]*//'
}

cli() { # <args...> — gRPC 面 CLI
    FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN="$TOKEN" /opt/fleetly/bin/fleetly "$@"
}

wait_liveness() { # <budget-s>
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(http_code "$HTTP/healthz/liveness")" = '200' ] && return 0
        sleep 2
    done
    return 1
}
traefik_ready() {
    docker service ls --format '{{.Name}} {{.Replicas}}' 2>/dev/null |
        grep -q 'fleetly-ingress 1/1'
}
wait_traefik() { # <budget-s>
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        traefik_ready && return 0
        sleep 3
    done
    return 1
}
wait_buildkitd() { # <budget-s> — fleetly-buildkit 容器进入 Running（conformance 同款收敛门）
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(docker inspect -f '{{.State.Running}}' fleetly-buildkit 2>/dev/null)" = 'true' ] && return 0
        sleep 5
    done
    return 1
}
derived_states() {
    cli apps list --json 2>/dev/null |
        grep -o '"derived_state": *"[a-z]*"' |
        sed 's/.*: *"//; s/"$//' |
        sort
}
rss_kb() { # <pid> — /proc/<pid>/status VmRSS（kB）
    awk '/^VmRSS:/ {print $2}' "/proc/$1/status" 2>/dev/null | head -n 1
}
pid_of() { # <name> — busybox pidof（进程名匹配）
    pidof "$1" 2>/dev/null | awk '{print $1}'
}

# rss_stats <outfile> <pid> <n> <interval> — 多拍采样，打印 min/avg/max
# （空格分隔）到 stdout，原始拍写 outfile。
rss_stats() {
    _f=$1
    _pid=$2
    _n=$3
    _iv=$4
    : >"$_f"
    _i=0
    while [ "$_i" -lt "$_n" ]; do
        _kb=$(rss_kb "$_pid")
        [ -n "$_kb" ] || return 1
        printf '%s\n' "$_kb" >>"$_f"
        [ "$((_i + 1))" -lt "$_n" ] && sleep "$_iv"
        _i=$((_i + 1))
    done
    _min=$(sort -n "$_f" | head -n 1)
    _max=$(sort -n "$_f" | tail -n 1)
    _avg=$(awk '{s+=$1} END {printf "%.0f", s/NR}' "$_f")
    printf '%s %s %s' "$_min" "$_avg" "$_max"
}

nl "=== T2.25 calibration inner (stage=$CAL_STAGE settle=${SETTLE}s samples=$SAMPLES x ${INTERVAL}s) ==="
T_START=$(date +%s)

# ------------------------------------------------------------ P0 preflight
for _f in "$INSTALL_SH" "$CAL_STAGE/fleetlyd" "$CAL_STAGE/fleetly" \
    "$CAL_STAGE/hello" "$CAL_STAGE/probe.tar.gz"; do
    [ -s "$_f" ] || {
        nl "FATAL: staged file missing: $_f"
        exit 1
    }
done
sed -i 's/\r$//' "$INSTALL_SH" 2>/dev/null || true
sh -n "$INSTALL_SH"
assert "CAL-P0-shn-install" $?
mkdir -p "$OUT" "$APPS_DIR" "$BLD_DIR"

# ------------------------------------------------------------ P1 安装+起服
sh "$INSTALL_SH" --bin-dir "$CAL_STAGE" --no-systemd >"$CAL_STAGE/install.log" 2>&1
RC=$?
assert "CAL-P1-install-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    cat "$CAL_STAGE/install.log" || true
    finish
fi

# 自写配置：ACME 关（离线校准，不签证书）、git 关、快引擎参数（与既有套件同口径）。
mkdir -p /opt/fleetly/etc /var/lib/fleetly
# 预拉平台依赖镜像（cert seed 容器与 Traefik 服务不自动拉镜像——T2.15 已知
# 边界；buildkit 镜像同理为 daemon warm 路径依赖）。并行拉取（网络瓶颈主导）。
docker pull -q alpine:3.20 >/dev/null 2>&1 &
docker pull -q traefik:v3.5 >/dev/null 2>&1 &
docker pull -q moby/buildkit:v0.32.2 >/dev/null 2>&1 &
wait
cat > /opt/fleetly/etc/config.yaml <<EOF
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
  release_timeout_seconds: 120
  observe_seconds: 5
  replicas_below_seconds: 5
  poll_seconds: 1
  drift_interval_seconds: 3600
backup:
  keep: 7
git:
  enabled: false
logging:
  level: info
EOF
assert "CAL-P1-config-written" $?

(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90
assert "CAL-P1-liveness-200" $?
if [ "$(http_code "$HTTP/healthz/liveness")" != '200' ]; then
    tail -n 40 "$DLOG" 2>/dev/null || true
    fail "CAL-P1-aborted-suite" "daemon not live"
    finish
fi
TOKEN=$(grep 'bootstrap admin token' "$DLOG" 2>/dev/null | sed -e 's/.*: //' -e 's/".*//' | head -n 1)
[ -n "$TOKEN" ]
assert "CAL-P1-bootstrap-token" $?
[ -n "$TOKEN" ] || {
    fail "CAL-P1-aborted-suite" "no token"
    finish
}
cli apps list >"$CAL_STAGE/cli.out" 2>&1
assert "CAL-P1-cli-apps-list" $? "$(tail -n 2 "$CAL_STAGE/cli.out")"

wait_traefik 240
assert "CAL-P1-traefik-1-1" $?
wait_buildkitd 600
assert "CAL-P1-buildkitd-running" $?
T_P1=$(date +%s)
nl "P1 done in $((T_P1 - T_START))s"

# ------------------------------------------------------- P2 idle 基线采样
nl "settling ${SETTLE}s (idle baseline window)"
sleep "$SETTLE"

FPID=$(cat "$PID_FILE" 2>/dev/null)
[ -n "$FPID" ] || FPID=$(pid_of fleetlyd)
[ -n "$FPID" ]
assert "CAL-P2-fleetlyd-pid" $? "pid=$FPID"
DPID=$(pid_of dockerd)
CPID=$(pid_of containerd)
[ -n "$CPID" ] || CPID=$(pid_of docker-containerd)
nl "pids: fleetlyd=$FPID dockerd=${DPID:-?} containerd=${CPID:-?}"

FD_STATS=$(rss_stats "$OUT/fleetlyd-idle.samples" "$FPID" "$SAMPLES" "$INTERVAL")
assert "CAL-P2-fleetlyd-rss-sampled" $? "stats=$FD_STATS"
FD_IDLE_MIN=${FD_STATS%% *}
_FD_REST=${FD_STATS#* }
FD_IDLE_AVG=${_FD_REST%% *}
FD_IDLE_MAX=${_FD_REST##* }
nl "fleetlyd idle RSS: min=${FD_IDLE_MIN} avg=${FD_IDLE_AVG} max=${FD_IDLE_MAX} kB"

DK_IDLE=$(rss_kb "$DPID")
CD_IDLE=$(rss_kb "$CPID")
nl "dockerd idle RSS: ${DK_IDLE:-unreadable} kB (swarmkit in-process); containerd: ${CD_IDLE:-unreadable} kB"
[ -n "$DK_IDLE" ]
assert "CAL-P2-dockerd-rss" $?
[ -n "$CD_IDLE" ]
assert "CAL-P2-containerd-rss" $?
# 后续汇总算术用 0 兜底（采样失败已是 FAIL，不让汇总再崩）。
DK_IDLE=${DK_IDLE:-0}
CD_IDLE=${CD_IDLE:-0}
FD_IDLE_MIN=${FD_IDLE_MIN:-0}
FD_IDLE_AVG=${FD_IDLE_AVG:-0}
FD_IDLE_MAX=${FD_IDLE_MAX:-0}

docker stats --no-stream --format '{{.Name}} | {{.CPUPerc}} | {{.MemUsage}}' >"$OUT/stats-idle.psv" 2>/dev/null || true
cat "$OUT/stats-idle.psv" | sed 's/^/stats-idle: /'
grep -q 'fleetly-ingress' "$OUT/stats-idle.psv"
assert "CAL-P2-traefik-in-stats" $?
grep -q 'fleetly-buildkit' "$OUT/stats-idle.psv"
assert "CAL-P2-buildkit-in-stats" $?
T_P2=$(date +%s)
nl "P2 done at t+$((T_P2 - T_START))s"

# --------------------------------------------- P3 容量压测：50 apps/200 域名
docker load <"$CAL_STAGE/probe.tar.gz" >"$CAL_STAGE/load.log" 2>&1
assert "CAL-P3-probe-image-loaded" $?
docker image inspect probeapp:1 >/dev/null 2>&1
assert "CAL-P3-probe-tag" $?

# 压测前内存基点（增量 = P3 后 - 此处）。
FD_PRE=$(rss_kb "$FPID")
DK_PRE=$(rss_kb "$DPID")
FD_PRE=${FD_PRE:-0}
DK_PRE=${DK_PRE:-0}
nl "pre-load RSS: fleetlyd=${FD_PRE}kB dockerd=${DK_PRE}kB"

# 生成 compose：cap01..cap20 = web+api 两服务各 5 域名（10/app）；cap21..50 单 web。
_NN=1
while [ "$_NN" -le "$N_APPS" ]; do
    _app=$(printf 'cap%02d' "$_NN")
    _d=$(printf '%02d' "$_NN")
    _dir="$APPS_DIR/$_app"
    mkdir -p "$_dir"
    if [ "$_NN" -le "$N_DOMAIN_APPS" ]; then
        _dw="d${_d}-1.${_app}.cal.test,d${_d}-2.${_app}.cal.test,d${_d}-3.${_app}.cal.test,d${_d}-4.${_app}.cal.test,d${_d}-5.${_app}.cal.test"
        _da="d${_d}-6.${_app}.cal.test,d${_d}-7.${_app}.cal.test,d${_d}-8.${_app}.cal.test,d${_d}-9.${_app}.cal.test,d${_d}-10.${_app}.cal.test"
        cat >"$_dir/compose.yaml" <<EOF
name: $_app
services:
  web:
    image: probeapp:1
    command: ["/probe", "serve"]
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
    labels:
      fleetly.domains: "$_dw"
  api:
    image: probeapp:1
    command: ["/probe", "serve"]
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
    labels:
      fleetly.domains: "$_da"
EOF
    else
        cat >"$_dir/compose.yaml" <<EOF
name: $_app
services:
  web:
    image: probeapp:1
    command: ["/probe", "serve"]
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
EOF
    fi
    _NN=$((_NN + 1))
done
assert "CAL-P3-compose-generated" $?

# 顺序部署（部署队列吞吐 = 每条 CLI deploy 入队→终态的实测时延）。
: >"$LAT_PSV"
_NN=1
_FAILS=0
while [ "$_NN" -le "$N_APPS" ]; do
    _app=$(printf 'cap%02d' "$_NN")
    _t0=$(date +%s)
    cli deploy --timeout 240s "$APPS_DIR/$_app/compose.yaml" >"$APPS_DIR/$_app/deploy.log" 2>&1
    _rc=$?
    _t1=$(date +%s)
    printf '%s|%s|%s\n' "$_app" "$_rc" "$((_t1 - _t0))" >>"$LAT_PSV"
    if [ "$_rc" -ne 0 ]; then
        _FAILS=$((_FAILS + 1))
        nl "deploy $_app FAILED rc=$_rc: $(tail -n 2 "$APPS_DIR/$_app/deploy.log" | tr '\n' ' ')"
    fi
    [ "$((_NN % 10))" -eq 0 ] && nl "progress: $_NN/$N_APPS deployed ($_FAILS failed so far)"
    _NN=$((_NN + 1))
done
[ "$_FAILS" -eq 0 ]
assert "CAL-P3-all-deploys-rc0" $? "failed=$_FAILS (see $LAT_PSV)"

APP_COUNT=$(cli apps list --json 2>/dev/null | grep -o '"name": *"cap[0-9]*"' | sort -u | wc -l | tr -d ' ')
[ "$APP_COUNT" -eq "$N_APPS" ]
assert "CAL-P3-apps-count-50" $? "apps=$APP_COUNT (want $N_APPS)"

_i=0
while [ "$_i" -lt 60 ]; do
    [ "$(derived_states | sort -u)" = 'running' ] && break
    sleep 2
    _i=$((_i + 1))
done
[ "$(derived_states | sort -u)" = 'running' ]
assert "CAL-P3-all-running" $? "states=($(derived_states | sort -u | paste -sd,))"

SVC_COUNT=$(docker service ls --format '{{.Name}}' 2>/dev/null | grep -cE '^fleetly-cap[0-9]+-(web|api)$')
[ "$SVC_COUNT" -eq 70 ]
assert "CAL-P3-swarm-services-70" $? "services=$SVC_COUNT (want 70 = 20x2 + 30x1)"

# 域名台账计数（前 20 应用各 10 条 = 200）。
DOM_TOTAL=0
_NN=1
: >"$OUT/domains.psv"
while [ "$_NN" -le "$N_DOMAIN_APPS" ]; do
    _app=$(printf 'cap%02d' "$_NN")
    _c=$(cli domains list --json "$_app" 2>/dev/null | grep -o '"domain": *"[^"]*"' | sort -u | wc -l | tr -d ' ')
    printf '%s|%s\n' "$_app" "$_c" >>"$OUT/domains.psv"
    DOM_TOTAL=$((DOM_TOTAL + ${_c:-0}))
    _NN=$((_NN + 1))
done
[ "$DOM_TOTAL" -eq 200 ]
assert "CAL-P3-domain-ledger-200" $? "domains=$DOM_TOTAL (want 200)"

# ingress 视图合成：/configs 非空 + 含 200 域名（无证书 → 仅 web 入口 80 路由）。
ITOK=$(cat /var/lib/fleetly/fleetly-ingress.token 2>/dev/null)
wget -q -T 10 -O "$OUT/configs.json" --header "Authorization: Bearer $ITOK" http://127.0.0.1:8422/configs 2>/dev/null
assert "CAL-P3-configs-fetched" $?
_CFG_BYTES=$(wc -c <"$OUT/configs.json" 2>/dev/null | tr -d ' ')
[ "${_CFG_BYTES:-0}" -gt 1000 ]
assert "CAL-P3-configs-nonempty" $? "bytes=$_CFG_BYTES"
_CFG_DOM=$(grep -o 'cal\.test' "$OUT/configs.json" 2>/dev/null | wc -l | tr -d ' ')
[ "$_CFG_DOM" -eq 200 ]
assert "CAL-P3-configs-domains-200" $? "domain-refs=$_CFG_DOM (want 200)"
# 路由发布面只含带域名应用（无域名应用不产路由——设计语义）：每应用 2 服务
# × 1 个 web 入口 router = 2×20 = 40。
_CFG_ROUTERS=$(grep -o '"fleetly-cap[0-9]*-[a-z]*-web"' "$OUT/configs.json" 2>/dev/null | sort -u | wc -l | tr -d ' ')
_ROUTER_WANT=$((N_DOMAIN_APPS * 2))
[ "$_CFG_ROUTERS" -eq "$_ROUTER_WANT" ]
assert "CAL-P3-configs-routers" $? "routers=$_CFG_ROUTERS (want $_ROUTER_WANT = 2 x $N_DOMAIN_APPS domain apps)"

# Traefik 实际路由抽检（两个应用的 80 入口）。
DOM1='d01-1.cap01.cal.test'
DOM2=$(printf 'cap%02d' "$N_DOMAIN_APPS")
DOM2="d$(printf '%02d' "$N_DOMAIN_APPS")-1.$DOM2.cal.test"
_i=0
while [ "$(http_code "http://127.0.0.1/" "$DOM1")" != '200' ] && [ "$_i" -lt 30 ]; do
    sleep 2
    _i=$((_i + 1))
done
[ "$(http_code "http://127.0.0.1/" "$DOM1")" = '200' ]
assert "CAL-P3-route-200-cap01" $? "code=$(http_code "http://127.0.0.1/" "$DOM1")"
[ "$(http_code "http://127.0.0.1/" "$DOM2")" = '200' ]
assert "CAL-P3-route-200-cap-last" $? "code=$(http_code "http://127.0.0.1/" "$DOM2") dom=$DOM2"

# 压测后内存：fleetlyd 多拍 + dockerd 单拍 + stats。
FD_STATS2=$(rss_stats "$OUT/fleetlyd-loaded.samples" "$FPID" 5 3)
FD_L_MIN=${FD_STATS2%% *}
_FD_REST2=${FD_STATS2#* }
FD_L_AVG=${_FD_REST2%% *}
FD_L_MAX=${_FD_REST2##* }
DK_POST=$(rss_kb "$DPID")
FD_L_MIN=${FD_L_MIN:-0}
FD_L_AVG=${FD_L_AVG:-0}
FD_L_MAX=${FD_L_MAX:-0}
DK_POST=${DK_POST:-0}
docker stats --no-stream --format '{{.Name}} | {{.CPUPerc}} | {{.MemUsage}}' >"$OUT/stats-loaded.psv" 2>/dev/null || true
nl "post-load fleetlyd RSS: min=$FD_L_MIN avg=$FD_L_AVG max=$FD_L_MAX kB; dockerd=$DK_POST kB (pre=$DK_PRE)"
RUN_CTR=$(docker ps -q 2>/dev/null | wc -l | tr -d ' ')
nl "running containers after load: $RUN_CTR"
T_P3=$(date +%s)
nl "P3 done in $((T_P3 - T_P2))s (deploy wall)"

# ------------------------------------------------------ P4 并发构建 = 2
_NN=1
while [ "$_NN" -le "$N_BUILDS" ]; do
    _app=$(printf 'bld%d' "$_NN")
    _src="$BLD_DIR/$_app/src"
    mkdir -p "$_src"
    cp "$CAL_STAGE/hello" "$_src/hello"
    chmod 0755 "$_src/hello"
    # payload 逐构建随机（16MB 单层）→ 构建产物必不同（digest 互异）、单构建
    # 耗时秒级（并发=2 的采样可观测前提）。注意同层内容在同构建内不重复
    # （相同 digest 的并发层写入会触发 buildkit ref 锁 15min 争用——本机实测）。
    dd if=/dev/urandom of="$_src/blob" bs=1M count=16 2>/dev/null
    cat >"$_src/Dockerfile" <<'EOF'
FROM scratch
COPY --chmod=0755 hello /probe
COPY blob /blob
ENTRYPOINT ["/probe", "serve"]
EOF
    cat >"$BLD_DIR/$_app/compose.yaml" <<EOF
name: $_app
services:
  web:
    build:
      context: src
      dockerfile: Dockerfile
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
EOF
    _NN=$((_NN + 1))
done
assert "CAL-P4-build-contexts-generated" $?

# 采样器：0.4s 一轮，逐构建读最新行 status；记录 building 总数与 queued 出现。
: >"$SAMP_PSV"
(
    _run=1
    while [ "$_run" -eq 1 ]; do
        _b=0
        _q=0
        _k=1
        while [ "$_k" -le "$N_BUILDS" ]; do
            _app=$(printf 'bld%d' "$_k")
            _bl=$(cli builds list --limit 1 --json "$_app" 2>/dev/null)
            _st=$(json_str "$_bl" status)
            case "$_st" in
            building) _b=$((_b + 1)) ;;
            queued) _q=$((_q + 1)) ;;
            esac
            _k=$((_k + 1))
        done
        printf '%s|%s|%s\n' "$(date +%s)" "$_b" "$_q" >>"$SAMP_PSV"
        [ -f "$CAL_STAGE/stop-sampler" ] && _run=0
        sleep 0.3
    done
) &
SAMPLER_PID=$!

# 并发入队（fire-and-wait：CLI 等终态，后台并行）。
_NN=1
while [ "$_NN" -le "$N_BUILDS" ]; do
    _app=$(printf 'bld%d' "$_NN")
    (
        cli build --timeout 15m --json "$BLD_DIR/$_app/compose.yaml" >"$BLD_DIR/$_app/build.json" 2>"$BLD_DIR/$_app/build.err"
    ) &
    _NN=$((_NN + 1))
done

# 等全部构建终态（budget 15m）。
_deadline=$(( $(date +%s) + 900 ))
_done=0
while [ "$(date +%s)" -lt "$_deadline" ]; do
    _done=1
    _k=1
    while [ "$_k" -le "$N_BUILDS" ]; do
        _app=$(printf 'bld%d' "$_k")
        _st=$(json_str "$(cat "$BLD_DIR/$_app/build.json" 2>/dev/null || printf '{}')" status)
        case "$_st" in
        succeeded | failed) ;;
        '') _done=0 ;;
        esac
        _k=$((_k + 1))
    done
    [ "$_done" -eq 1 ] && break
    sleep 3
done
touch "$CAL_STAGE/stop-sampler"
wait "$SAMPLER_PID" 2>/dev/null || true

[ "$_done" -eq 1 ]
assert "CAL-P4-all-builds-terminal" $? "done=$_done (budget 900s)"

# 采样结论：max(同时在建) 与 queued 出现。
MAX_B=0
QUEUED_SEEN=0
while IFS= read -r _row; do
    [ -n "${_row:-}" ] || continue
    _bb=$(printf '%s' "$_row" | awk -F'|' '{print $2}')
    _qq=$(printf '%s' "$_row" | awk -F'|' '{print $3}')
    [ "${_bb:-0}" -gt "$MAX_B" ] && MAX_B=$_bb
    [ "${_qq:-0}" -ge 1 ] && QUEUED_SEEN=1
done <"$SAMP_PSV"
SAMPLES_N=$(wc -l <"$SAMP_PSV" | tr -d ' ')
nl "build sampler: rounds=$SAMPLES_N max_building=$MAX_B queued_seen=$QUEUED_SEEN"

[ "$MAX_B" -le 2 ]
assert "CAL-P4-max-concurrent-2" $? "max_building=$MAX_B (want ≤2; queue cap)"
[ "$MAX_B" -eq 2 ]
assert "CAL-P4-concurrency-exactly-2" $? "max_building=$MAX_B (5 builds, cap=2 → 必达 2)"
[ "$QUEUED_SEEN" -eq 1 ]
assert "CAL-P4-queued-observed" $? "queued_seen=$QUEUED_SEEN (want 1: 第三构建排队)"

# 全部 succeeded + digest 互异（无交叉污染）。
: >"$OUT/build-digests.psv"
_k=1
_BOK=0
while [ "$_k" -le "$N_BUILDS" ]; do
    _app=$(printf 'bld%d' "$_k")
    _j=$(cat "$BLD_DIR/$_app/build.json" 2>/dev/null || printf '{}')
    _st=$(json_str "$_j" status)
    _dg=$(json_str "$_j" image_digest)
    [ "$_st" = 'succeeded' ] && _BOK=$((_BOK + 1))
    printf '%s|%s|%s\n' "$_app" "$_st" "$_dg" >>"$OUT/build-digests.psv"
    _ref=$(json_str "$_j" image_ref)
    [ -n "$_ref" ] && docker image inspect "$_ref" >/dev/null 2>&1
    assert "CAL-P4-image-inspect-$_app" $? "ref=$_ref"
    _k=$((_k + 1))
done
[ "$_BOK" -eq "$N_BUILDS" ]
assert "CAL-P4-builds-succeeded" $? "succeeded=$_BOK/[$N_BUILDS]"
_DG_UNIQUE=$(awk -F'|' '{print $3}' "$OUT/build-digests.psv" | sort -u | wc -l | tr -d ' ')
[ "$_DG_UNIQUE" -eq "$N_BUILDS" ]
assert "CAL-P4-digests-unique" $? "unique-digests=$_DG_UNIQUE (want $N_BUILDS)"
T_P4=$(date +%s)
nl "P4 done in $((T_P4 - T_P3))s"

# ------------------------------------------------------------- P5 汇总
L_MIN=$(sort -t'|' -k3 -n "$LAT_PSV" | head -n 1 | awk -F'|' '{print $3}')
L_MAX=$(sort -t'|' -k3 -n "$LAT_PSV" | tail -n 1 | awk -F'|' '{print $3}')
L_AVG=$(awk -F'|' '{s+=$3; n++} END {printf "%.1f", s/n}' "$LAT_PSV")
L_MED=$(sort -t'|' -k3 -n "$LAT_PSV" | awk -F'|' '{a[NR]=$3} END {printf "%.0f", (NR%2 ? a[(NR+1)/2] : (a[NR/2]+a[NR/2+1])/2)}')
TOTAL_WALL=$(( $(date +%s) - T_START ))

{
    printf '## fleetly T2.25 资源与容量校准（dind 实测）\n\n'
    printf '| 项 | 值 |\n|---|---|\n'
    printf '| dind 镜像 | `%s` |\n' "$DIND_TAG"
    printf '| fleetlyd 版本 | `%s` |\n' "$VERSION"
    printf '| idle 稳定窗 | %ss（采样 %s 拍 x %ss） |\n' "$SETTLE" "$SAMPLES" "$INTERVAL"
    printf '| fleetlyd idle RSS min/avg/max | %s / %s / %s kB |\n' "$FD_IDLE_MIN" "$FD_IDLE_AVG" "$FD_IDLE_MAX"
    printf '| dockerd idle RSS（含 swarmkit，同进程） | %s kB |\n' "$DK_IDLE"
    printf '| containerd idle RSS（如实单列） | %s kB |\n' "$CD_IDLE"
    printf '| 控制面合计（fleetlyd+dockerd idle avg） | %s kB |\n' "$((FD_IDLE_AVG + DK_IDLE))"
    printf '| fleetlyd 50apps 后 RSS min/avg/max | %s / %s / %s kB（增量 avg %s kB） |\n' "$FD_L_MIN" "$FD_L_AVG" "$FD_L_MAX" "$((FD_L_AVG - FD_IDLE_AVG))"
    printf '| dockerd 50apps 后 RSS | %s kB（增量 %s kB） |\n' "$DK_POST" "$((DK_POST - DK_PRE))"
    printf '| 压测规模 | %s apps / %s 域名 / %s swarm services / %s 容器在跑 |\n' "$N_APPS" "$DOM_TOTAL" "$SVC_COUNT" "$RUN_CTR"
    printf '| 部署时延 min/med/avg/max | %ss / %ss / %ss / %ss（%s 条） |\n' "$L_MIN" "$L_MED" "$L_AVG" "$L_MAX" "$N_APPS"
    printf '| 域名台账 | %s 条（断言 =200） |\n' "$DOM_TOTAL"
    printf '| /configs 视图 | %s bytes / %s 域名引用 / %s routers |\n' "$_CFG_BYTES" "$_CFG_DOM" "$_CFG_ROUTERS"
    printf '| 并发构建采样 | %s 轮 / max_building=%s / queued_seen=%s |\n' "$SAMPLES_N" "$MAX_B" "$QUEUED_SEEN"
    printf '| 内层套件总墙钟 | %ss |\n' "$TOTAL_WALL"
    printf '\n| 容器（idle 采样时 docker stats） | CPU | 内存 |\n|---|---|---|\n'
    awk -F ' \\| ' '{printf "| `%s` | %s | %s |\n", $1, $2, $3}' "$OUT/stats-idle.psv"
    printf '\n| 容器（50apps 后 docker stats） | CPU | 内存 |\n|---|---|---|\n'
    awk -F ' \\| ' '{printf "| `%s` | %s | %s |\n", $1, $2, $3}' "$OUT/stats-loaded.psv"
    printf '\n| 构建 | 终态 | digest |\n|---|---|---|\n'
    awk -F'|' '{printf "| %s | %s | `%s` |\n", $1, $2, $3}' "$OUT/build-digests.psv"
    printf '\n| 应用 | 域名台账行数 |\n|---|---|\n'
    awk -F'|' '{printf "| %s | %s |\n", $1, $2}' "$OUT/domains.psv"
} >"$OUT/summary.md"

{
    printf '{\n'
    printf '  "sampled_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '  "dind_image": "%s",\n' "$DIND_TAG"
    printf '  "version": "%s",\n' "$VERSION"
    printf '  "settle_seconds": %s, "samples": %s, "interval_seconds": %s,\n' "$SETTLE" "$SAMPLES" "$INTERVAL"
    printf '  "idle": {"fleetlyd_rss_kb": {"min": %s, "avg": %s, "max": %s}, "dockerd_rss_kb": %s, "containerd_rss_kb": %s},\n' \
        "$FD_IDLE_MIN" "$FD_IDLE_AVG" "$FD_IDLE_MAX" "${DK_IDLE:-0}" "${CD_IDLE:-0}"
    printf '  "loaded_50apps": {"fleetlyd_rss_kb": {"min": %s, "avg": %s, "max": %s}, "dockerd_rss_kb": %s, "apps": %s, "domains": %s, "services": %s},\n' \
        "$FD_L_MIN" "$FD_L_AVG" "$FD_L_MAX" "$DK_POST" "$N_APPS" "$DOM_TOTAL" "$SVC_COUNT"
    printf '  "deploy_latency_seconds": {"min": %s, "median": %s, "avg": %s, "max": %s},\n' \
        "$L_MIN" "$L_MED" "$L_AVG" "$L_MAX"
    printf '  "ingress_view": {"bytes": %s, "domain_refs": %s, "routers": %s},\n' \
        "${_CFG_BYTES:-0}" "${_CFG_DOM:-0}" "${_CFG_ROUTERS:-0}"
    printf '  "build_concurrency": {"samples": %s, "max_building": %s, "queued_seen": %s, "succeeded": %s},\n' \
        "$SAMPLES_N" "$MAX_B" "$QUEUED_SEEN" "$_BOK"
    printf '  "wall_seconds_total": %s\n' "$TOTAL_WALL"
    printf '}\n'
} >"$OUT/cal.json"

nl '--- summary ---'
cat "$OUT/summary.md"
nl "CAL-INNER-DONE wall=${TOTAL_WALL}s"
finish
