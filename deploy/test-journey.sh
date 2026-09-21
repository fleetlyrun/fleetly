#!/bin/sh
# deploy/test-journey.sh — T2.26 v0.1 端到端验收旅程内层（docker:29.8.1-dind
# 容器内执行；宿主编排见 run-journey-test.sh）。注入面 $JK_STAGE（默认
# /tmp/journey）：install.sh / fleetlyd / fleetly / hello / probe.tar.gz /
# console-dist/（Console SPA 现成构建产物）。产物 /tmp/journey-out/
# {summary.md,journey.json}（宿主 docker exec cat 读回）。
#
# 场景（架构 §4.2 验收段：干净 VPS 一条命令安装 → 已解析域名 20 分钟内
# git push 部署拿到 HTTPS（DNS 传播不计入）→ UI 可见日志与配置 → 一键回
# 滚；dind 版 = hosts/pebble 代演 DNS 与 ACME，链路真实）：
#   J1  干净安装（--bin-dir --no-systemd）+ 安装报告输出（计时 t_install）
#   J2  旅程装配：自写配置（ACME=Pebble + ca_pool、git 0.0.0.0:8424、
#       console.static_dir）+ Pebble 起 CA（ghcr.io/letsencrypt/pebble，
#       根证书经 docker cp 容器→dind 方向取出——非禁用的宿主→dind 注入
#       方向）+ daemon live + token + git key 注册 + hosts 注入（计时外，
#       DNS/装配准备不计入 ≤20min 判定）
#   J3  git push 部署：scratch 仓库（journeyapp：web=probeapp 服务 +
#       fleetly.domains=app.journey.test + sidecar 日志源服务）→ push main
#       → 部署 succeeded（t_deploy1）
#   J4  HTTPS 200：curl --cacert <pebble root> https://app.journey.test/ →
#       200 "ok"（HTTP-01 挑战经 Traefik 反代到控制面，T2-6 链路）；断言
#       证书链 issuer = pebble minica 根 + 域名台账 cert_sha256 就绪；
#       **CRITICAL = t_https - T0 ≤ 1200s 硬断言**（t_https = HTTPS 200 时刻）
#   J5  UI 可见：/ui/ 200 + JS 资源 200 + REST /v1/apps（Bearer）带部署数据
#       + logs history 有行（sidecar 心跳）+ env set 后 pending 可见
#   J6  一键回滚：push v2 → succeeded → revisions 取 seq=1 → rollback --to
#       → succeeded + kind=rollback 部署行 revision_id=旧版（快照重放断言）
#       + derived_state=running
#   J7  信任闭环引用：backups list 台账 verified（T2-9b 已落档，不重复演练）
#   J8  计时汇总（各阶段秒表 + TOTAL ≤ 1200s 断言）
#
# 断言风格与 deploy/test-upgrade.sh 一致（NAME: PASS/FAIL + 计数 + finish）。

set -u
# shellcheck disable=SC2034
JK_FAIL=0

JK_STAGE="${JK_STAGE:-/tmp/journey}"
INSTALL_SH="$JK_STAGE/install.sh"
DLOG="/tmp/j-fleetlyd.log"
PID_FILE="/var/run/fleetlyd.pid"
OUT="/tmp/journey-out"
HTTP="http://127.0.0.1:8420"

DOMAIN='app.journey.test'
APP='journeyapp'
GIT_URL="ssh://git@127.0.0.1:8424/${APP}.git"
SRC_DIR="/tmp/journey-src"
PEBBLE_NAME='pebble-journey'
# 镜像钉 digest（T0-V2.3 供应链）：tag 保留作可读性，digest 为准；解析命令
# docker buildx imagetools inspect（多架构 index）。
PEBBLE_IMG='ghcr.io/letsencrypt/pebble:latest@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199'
VERSION="${JK_VERSION:-v0.1.0-journey}"
DIND_TAG="${JK_DIND_IMAGE:-docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0}"

nl() { printf '[jk %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    JK_FAIL=$((JK_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then
        pass "$1"
    else
        fail "$1" "${3:-}"
    fi
}
finish() {
    if [ "$JK_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$JK_FAIL"
    nl "---- fleetlyd log tail ($DLOG) ----"
    tail -n 60 "$DLOG" 2>/dev/null || true
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

json_str() { # <json> <key>
    printf '%s' "$1" |
        grep -o "\"$2\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/.*:[[:space:]]*"//; s/"$//'
}
json_num() { # <json> <key>
    printf '%s' "$1" |
        grep -o "\"$2\"[[:space:]]*:[[:space:]]*[0-9][0-9]*" |
        head -n 1 |
        sed 's/.*:[[:space:]]*//'
}

cli() { # <args...>
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
derived_state() { # <app> — 首 derived_state
    cli apps get --json "$1" 2>/dev/null |
        grep -o '"derived_state": *"[a-z]*"' |
        head -n 1 |
        sed 's/.*: *"//; s/"$//'
}
wait_app_running() { # <app> <budget-s>
    _deadline=$(( $(date +%s) + $2 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        [ "$(derived_state "$1")" = 'running' ] && return 0
        sleep 2
    done
    return 1
}
# deploy_row_for_sha <app> <sha> — git 来源部署行的终态扫描（段落扫描）。
deploy_row_for_sha() {
    cli deployments list --limit 10 --json "$1" 2>/dev/null |
        awk -v sha="$2" 'BEGIN {RS = "}"} $0 ~ ("\"source_git_sha\": \"" sha "\"") && $0 ~ /"status": *"(succeeded|failed|cancelled)"/ {print; exit}'
}
wait_git_deploy() { # <app> <sha> <budget-s> → 0=成功 1=失败/超时
    _deadline=$(( $(date +%s) + $3 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        _row=$(deploy_row_for_sha "$1" "$2")
        case "$_row" in
        *'"status": "succeeded"'*) return 0 ;;
        *'"status": "failed"'* | *'"status": "cancelled"'*) return 1 ;;
        esac
        sleep 3
    done
    return 1
}
revision_id_for_seq() { # <app> <seq> — revisions 台账 seq → id
    cli revisions list --json "$1" 2>/dev/null |
        awk -v sq="$2" 'BEGIN {RS = "}"} $0 ~ ("\"seq\": " sq "[,}]") && $0 ~ /"id": *"[^"]*"/ {
            if (match($0, /"id": *"[^"]*"/)) { print substr($0, RSTART, RLENGTH); exit }
        }' |
        sed 's/.*: *"//; s/"$//'
}
revision_field_for_seq() { # <app> <seq> <field> — revisions 台账 seq → 任意字符串字段
    cli revisions list --json "$1" 2>/dev/null |
        awk -v sq="$2" -v fldpat="$3" 'BEGIN {RS = "}"} $0 ~ ("\"seq\": " sq "[,}]") {
            if (match($0, "\"" fldpat "\": *\"[^\"]*\"")) { print substr($0, RSTART, RLENGTH); exit }
        }' |
        sed 's/.*: *"//; s/"$//'
}

nl "=== T2.26 journey inner (stage=$JK_STAGE) ==="
T0=$(date +%s)

# ------------------------------------------------------------ J0 preflight
for _f in "$INSTALL_SH" "$JK_STAGE/fleetlyd" "$JK_STAGE/fleetly" \
    "$JK_STAGE/hello" "$JK_STAGE/probe.tar.gz" "$JK_STAGE/console-dist/index.html"; do
    [ -s "$_f" ] || {
        nl "FATAL: staged file missing: $_f"
        exit 1
    }
done
sed -i 's/\r$//' "$INSTALL_SH" 2>/dev/null || true
sh -n "$INSTALL_SH"
assert "J0-shn-install" $?
mkdir -p "$OUT" /var/lib/fleetly /root/.ssh
command -v git >/dev/null 2>&1
assert "J0-git-present" $?
command -v ssh-keygen >/dev/null 2>&1
assert "J0-ssh-keygen-present" $?

# ------------------------------------------------ J1 干净安装（一条命令形态）
T_INSTALL0=$(date +%s)
sh "$INSTALL_SH" --bin-dir "$JK_STAGE" --no-systemd >"$JK_STAGE/install.log" 2>&1
RC=$?
T_INSTALL=$(date +%s)
assert "J1-install-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    cat "$JK_STAGE/install.log" || true
    finish
fi
grep -q 'engine gate' "$JK_STAGE/install.log"
assert "J1-report-engine-gate" $?
grep -q 'swarm initialized' "$JK_STAGE/install.log"
assert "J1-report-swarm-init" $?
grep -q 'port exposure' "$JK_STAGE/install.log"
assert "J1-report-port-exposure" $?
grep -q 'bootstrap token' "$JK_STAGE/install.log"
assert "J1-report-token-hint" $?
[ -x /opt/fleetly/bin/fleetlyd ]
assert "J1-fleetlyd-installed" $?
[ "$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null)" = 'active' ]
assert "J1-swarm-active" $?
nl "J1 install wall: $((T_INSTALL - T_INSTALL0))s"

# ------------------------------------------------ J2 旅程装配（计时外准备）
T_PREP0=$(date +%s)
mkdir -p /opt/fleetly/etc
# 预拉平台依赖镜像（cert seed 与 Traefik 服务不自动拉镜像——T2.15 已知边界）
# 与旅程依赖（pebble CA、curl 探针、sidecar 基镜像）。全部钉 digest（T0-V2.3）。
docker pull -q alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc >/dev/null 2>&1 || true
docker pull -q traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2 >/dev/null 2>&1 || true
docker pull -q "$PEBBLE_IMG" >/dev/null 2>&1
assert "J2-pebble-image-pulled" $?
docker pull -q curlimages/curl:latest@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 >/dev/null 2>&1
assert "J2-curl-image-pulled" $?

# Pebble 根证书 + 默认配置：docker create + docker cp（容器→dind 方向拷贝；
# 宿主→dind 的 docker cp 注入才是被禁路径）。根证书同时喂给 fleetlyd
#（ca_pool_file）与 HTTPS 探针（-CAfile）。
docker rm -f pebble-extract >/dev/null 2>&1 || true
docker create --name pebble-extract "$PEBBLE_IMG" >/dev/null 2>&1
assert "J2-pebble-extract-ctr" $?
docker cp pebble-extract:/test/certs/pebble.minica.pem /var/lib/fleetly/pebble-root.pem >/dev/null 2>&1
assert "J2-pebble-root-extracted" $?
docker cp pebble-extract:/test/config/pebble-config.json /tmp/pebble-default.json >/dev/null 2>&1
assert "J2-pebble-config-extracted" $?
docker rm -f pebble-extract >/dev/null 2>&1 || true
openssl x509 -in /var/lib/fleetly/pebble-root.pem -noout -subject >/tmp/j-pebble-root-subject.txt 2>/dev/null
assert "J2-pebble-root-parses" $? "$(cat /tmp/j-pebble-root-subject.txt 2>/dev/null)"
nl "pebble root: $(cat /tmp/j-pebble-root-subject.txt 2>/dev/null)"

# Pebble 默认 va.httpPort=5002（故意不按 RFC 打 80 的测试口径）——改写为 80，
# HTTP-01 挑战才能经 Traefik（80 入口）反代回控制面应答端点。
sed 's/"httpPort":[[:space:]]*[0-9][0-9]*/"httpPort": 80/' /tmp/pebble-default.json >/tmp/pebble-cfg.json
grep -q '"httpPort":[[:space:]]*80' /tmp/pebble-cfg.json
assert "J2-pebble-httpport-80" $? "$(grep -o '"httpPort"[^,]*' /tmp/pebble-cfg.json 2>/dev/null | head -n 1)"

# dind 自身 IP（hosts 代演 DNS 的事实地址；pebble VA 也要能解析到 Traefik 80）。
DIND_IP=$(hostname -i 2>/dev/null | awk '{print $1}')
[ -n "$DIND_IP" ]
assert "J2-dind-ip" $? "ip=$DIND_IP"
printf '%s %s\n' "$DIND_IP" "$DOMAIN" >>/etc/hosts

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
    enabled: true
    ca_dir_url: "https://127.0.0.1:14000/dir"
    ca_pool_file: "/var/lib/fleetly/pebble-root.pem"
    email: "ops@journey.test"
engine:
  deploy_timeout_seconds: 180
  observe_seconds: 5
  replicas_below_seconds: 5
  poll_seconds: 1
  drift_interval_seconds: 3600
backup:
  keep: 7
git:
  enabled: true
  addr: "0.0.0.0:8424"
  root: "/var/lib/fleetly/git"
console:
  static_dir: "/var/lib/fleetly/console-dist"
logging:
  level: info
EOF
assert "J2-config-written" $?
grep -q 'static_dir: "/var/lib/fleetly/console-dist"' /opt/fleetly/etc/config.yaml
assert "J2-config-console-static" $?

# Console SPA 现成产物落位（static_dir 指向数据根；daemon 装配期 fail-fast
# 于缺 index.html——先落位再起服）。
mkdir -p /var/lib/fleetly/console-dist
cp -r "$JK_STAGE/console-dist/." /var/lib/fleetly/console-dist/
assert "J2-console-dist-installed" $?
[ -f /var/lib/fleetly/console-dist/index.html ]
assert "J2-console-index-present" $?

(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90
assert "J2-liveness-200" $?
if [ "$(http_code "$HTTP/healthz/liveness")" != '200' ]; then
    tail -n 40 "$DLOG" 2>/dev/null || true
    fail "J2-aborted-suite" "daemon not live"
    finish
fi
# B5：token 本体不再进日志——首启写入 <数据根>/bootstrap-token（0600）。
TOKEN=$(cat /var/lib/fleetly/bootstrap-token 2>/dev/null)
[ -n "$TOKEN" ]
assert "J2-bootstrap-token" $?
[ -n "$TOKEN" ] || {
    fail "J2-aborted-suite" "no token"
    finish
}
cli apps list >"$JK_STAGE/cli.out" 2>&1
assert "J2-cli-apps-list" $? "$(tail -n 2 "$JK_STAGE/cli.out")"

# git push 身份：SSH keypair → 平台注册公钥（git keys add）。
ssh-keygen -t ed25519 -N '' -f /root/.ssh/journey_key -C journey-test >/dev/null 2>&1
assert "J2-ssh-keygen" $?
cli git keys add --note journey /root/.ssh/journey_key.pub >"$JK_STAGE/gitkey.log" 2>&1
assert "J2-git-key-registered" $? "$(tail -n 2 "$JK_STAGE/gitkey.log")"
grep -q 'fingerprint' "$JK_STAGE/gitkey.log"
assert "J2-git-key-fingerprint-echoed" $?

# Pebble CA 起服（--add-host 让 pebble VA 能把挑战打到 Traefik 80）。
docker rm -f "$PEBBLE_NAME" >/dev/null 2>&1 || true
docker run -d --name "$PEBBLE_NAME" -p 14000:14000 \
    -e PEBBLE_VA_NOSLEEP=1 \
    --add-host "$DOMAIN:$DIND_IP" \
    -v /tmp/pebble-cfg.json:/test/config/pebble-config.json \
    "$PEBBLE_IMG" >/dev/null 2>&1
assert "J2-pebble-running" $?
_deadline=$(( $(date +%s) + 60 ))
_pebble_up=0
while [ "$(date +%s)" -lt "$_deadline" ]; do
    if wget -q -T 5 -O /tmp/j-pebble-dir.json --no-check-certificate https://127.0.0.1:14000/dir 2>/dev/null; then
        _pebble_up=1
        break
    fi
    sleep 2
done
[ "$_pebble_up" -eq 1 ]
assert "J2-pebble-dir-endpoint" $?
grep -q 'newAccount' /tmp/j-pebble-dir.json 2>/dev/null
assert "J2-pebble-dir-newaccount" $?

# 说明：pebble 的 ACME 链根（leaf ← Pebble Intermediate CA ← Pebble Root CA）
# 为启动期随机生成且本版本镜像无 /root-cert 端点可取（实测 404）——镜像内
# minica 根只服务于 pebble 自身 14000 监听证书（lego ↔ pebble API TLS 的
# 信任锚，即上方 ca_pool_file；签发已在 J3/J4 实证）。对 443 服务的证书的
# 信任断言改走「服务链 ≡ 平台账签发链」的等同性断言（见 J4）。
nl "pebble listening cert issuer (minica): $(openssl s_client -connect 127.0.0.1:14000 -showcerts </dev/null 2>/dev/null | grep -o 'i:CN[^,]*' | head -n 1)"

docker load <"$JK_STAGE/probe.tar.gz" >"$JK_STAGE/load.log" 2>&1
assert "J2-probe-image-loaded" $?
docker image inspect probeapp:1 >/dev/null 2>&1
assert "J2-probe-tag" $?

wait_traefik 240
assert "J2-traefik-1-1" $?
T_PREP=$(date +%s)
nl "J2 prep wall (excluded from 20min judging): $((T_PREP - T_PREP0))s"

# ------------------------------------------- J3 git push 部署（v1）→ succeeded
T_PUSH0=$(date +%s)
mkdir -p "$SRC_DIR"
cat >"$SRC_DIR/compose.yaml" <<'EOF'
name: journeyapp
services:
  web:
    image: probeapp:1
    command: ["/probe", "serve"]
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
    labels:
      fleetly.domains: "app.journey.test"
  sidecar:
    # 钉 digest（T0-V2.3 供应链）：fixture 自带 sidecar 的镜像引用同受门禁。
    image: alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
    command: ["/bin/sh", "-c", "i=0; while true; do i=$((i+1)); echo journey-sidecar-heartbeat-v1-$i; sleep 5; done"]
EOF
(
    cd "$SRC_DIR" &&
        git init -b main >/dev/null 2>&1 &&
        git config user.email journey@test.local &&
        git config user.name journey &&
        git add -A >/dev/null &&
        git commit -m 'journey v1' >/dev/null
)
assert "J3-v1-committed" $?
GIT_SSH_COMMAND='ssh -i /root/.ssh/journey_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' \
    git -C "$SRC_DIR" push "$GIT_URL" main:main >"$JK_STAGE/push1.log" 2>&1
assert "J3-git-push-rc0" $? "$(tail -n 3 "$JK_STAGE/push1.log")"
SHA1=$(git -C "$SRC_DIR" rev-parse HEAD)
[ "${#SHA1}" -eq 40 ]
assert "J3-sha1-40hex" $? "sha1=$SHA1"

wait_git_deploy "$APP" "$SHA1" 300
assert "J3-deploy1-succeeded" $? "sha=$SHA1 (deployments list)"
T_DEPLOY1=$(date +%s)
nl "J3 push-to-deployed wall: $((T_DEPLOY1 - T_PUSH0))s"
wait_app_running "$APP" 120
assert "J3-app-running" $? "state=$(derived_state "$APP")"
# git 部署行回显 sha/ref（来源可追溯）。
_DROW=$(deploy_row_for_sha "$APP" "$SHA1")
printf '%s' "$_DROW" | grep -q "\"source_git_ref\": \"refs/heads/main\""
assert "J3-deploy1-git-ref-echoed" $?

# ----------------------------------------------------- J4 HTTPS 200（硬指标）
T_HTTPS0=$(date +%s)

# 4a. HTTP 路由先行检查点（Traefik 80 入口反代应用——TLS/签发问题的定位基线）。
_http_ok=0
_deadline=$(( $(date +%s) + 90 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    [ "$(http_code "http://127.0.0.1/" "$DOMAIN")" = '200' ] && _http_ok=1 && break
    sleep 3
done
[ "$_http_ok" -eq 1 ]
assert "J4a-http-route-200" $? "code=$(http_code "http://127.0.0.1/" "$DOMAIN")"
if [ "$_http_ok" -eq 0 ]; then
    # 404 定位套件：响应头（谁在答 404）/ 平台视图路由与规则 / Traefik 原始
    # 日志 / 证书卷内容。
    nl "404-headers: $(wget -q -S -O /dev/null --header "Host: $DOMAIN" http://127.0.0.1/ 2>&1 | head -n 8 | tr '\n' ' ')"
    ITOK=$(cat /var/lib/fleetly/fleetly-ingress.token 2>/dev/null)
    wget -q -T 5 -O /tmp/j-configs-diag.json --header "Authorization: Bearer $ITOK" http://127.0.0.1:8422/configs 2>/dev/null
    nl "configs-diag routers: $(grep -o '"fleetly-[a-z0-9-]*"' /tmp/j-configs-diag.json 2>/dev/null | sort -u | paste -sd,)"
    nl "configs-diag rule: $(grep -o 'Host(\\`[^`]*\\`)' /tmp/j-configs-diag.json 2>/dev/null | head -n 2 | paste -sd,)"
    nl "configs-diag tls: $(grep -o '"certificates"' /tmp/j-configs-diag.json 2>/dev/null | wc -l | tr -d ' ') section(s)"
    _TR_CTR=$(docker ps --format '{{.Names}}' 2>/dev/null | grep '^fleetly-ingress\.' | head -n 1)
    nl "traefik-ctr: $_TR_CTR"
    [ -n "$_TR_CTR" ] && docker logs "$_TR_CTR" --tail 40 2>&1 | tail -n 25 | sed 's/^/traefik-log: /'
    # Traefik 运行时路由视图（内部 API 走 traefik 入口 8080，仅容器内可达）。
    [ -n "$_TR_CTR" ] && nl "traefik-api-routers: $(docker exec "$_TR_CTR" wget -q -T 5 -O - http://127.0.0.1:8080/api/http/routers 2>&1 | head -c 900)"
    [ -n "$_TR_CTR" ] && nl "traefik-api-services: $(docker exec "$_TR_CTR" wget -q -T 5 -O - http://127.0.0.1:8080/api/http/services 2>&1 | head -c 500)"
    # E1-2 后证书走 cert_dir 形态（卷+seed 已退役）——诊断直探控制面侧
    # PEM 目录（dind 内数据根可探；目录缺失 = 入口未部署，非诊断错误）。
    nl "certdir: $(ls -la /var/lib/fleetly/fleetly-certs 2>/dev/null | tail -n 8 | tr '\n' ' ')"
fi

# 4b. 证书签发就绪门（域名台账 cert_sha256 非空 = ACME 集中签发已完成）。
_cert_ok=0
CERT_SHA=''
_deadline=$(( $(date +%s) + 180 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    CERT_SHA=$(json_str "$(cli domains list --json "$APP" 2>/dev/null || printf '{}')" cert_sha256)
    printf '%s' "$CERT_SHA" | grep -q '^[0-9a-f]\{64\}$' && _cert_ok=1 && break
    sleep 4
done
[ "$_cert_ok" -eq 1 ]
assert "J4b-cert-issued" $? "cert_sha256=$CERT_SHA (ACME 集中签发台账)"
if [ "$_cert_ok" -eq 0 ]; then
    grep -i 'acme\|challenge\|certificate\|route publish' "$DLOG" 2>/dev/null | tail -n 8 | sed 's/^/issue-fail-evidence: /' || true
    nl "domains rows: $(cli domains list --json "$APP" 2>/dev/null | head -c 300)"
fi

_https_ok=0
HTTPS_PROBE=''
_deadline=$(( $(date +%s) + 300 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    # dind 内 openssl 直探 443（SNI = 域名；不校验 pebble 启动期临时根——
    # 该根不可经 HTTP 获取，信任面改由「服务链 ≡ 平台账签发链」等同性断言）。
    HTTPS_PROBE=$(printf 'GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n' "$DOMAIN" |
        openssl s_client -quiet -connect 127.0.0.1:443 -servername "$DOMAIN" 2>/tmp/j-sclient.err)
    case "$HTTPS_PROBE" in
    *'HTTP/1.1 200'*)
        _https_ok=1
        break
        ;;
    esac
    sleep 4
done
T_HTTPS=$(date +%s)
CRITICAL=$((T_HTTPS - T0))
[ "$_https_ok" -eq 1 ]
assert "J4-https-200-pebble" $? "probe=$(printf '%s' "$HTTPS_PROBE" | head -n 1) err=$(grep -i 'verify error\|alert\|error\|depth' /tmp/j-sclient.err 2>/dev/null | head -n 3 | tr '\n' ' ' | head -c 200)"
[ "$CRITICAL" -le 1200 ]
assert "J4-critical-within-20min" $? "critical=${CRITICAL}s (install→HTTPS 200; DNS/hosts 不计)"
nl "J4 CRITICAL (install→HTTPS 200): ${CRITICAL}s (https wait window $((T_HTTPS - T_HTTPS0))s)"

# 服务链 ≡ 平台账签发链（信任面的等同性断言）：
#   ① 443 服务叶证书 DER sha256 == 平台签发产物 journeyapp.crt 首证书 DER sha256；
#   ② 叶 issuer = Pebble Intermediate CA（ACME 集中签发的链形态）；
#   ③ 域名台账 cert_sha256 == 签发产物文件 sha256（certStore ↔ 台账一致）。
openssl s_client -connect 127.0.0.1:443 -servername "$DOMAIN" -showcerts </dev/null 2>/dev/null >/tmp/j-showcerts.txt
awk '/BEGIN CERT/{f=1} f{print} /END CERT/{exit}' /tmp/j-showcerts.txt >/tmp/j-served-leaf.pem 2>/dev/null
openssl x509 -in /tmp/j-served-leaf.pem -outform DER 2>/dev/null | sha256sum | awk '{print $1}' >/tmp/j-served-leaf.sha
awk '/BEGIN CERT/{f=1} f{print} /END CERT/{exit}' /var/lib/fleetly/fleetly-certs/"$APP".crt 2>/dev/null |
    openssl x509 -outform DER 2>/dev/null | sha256sum | awk '{print $1}' >/tmp/j-issued-leaf.sha
[ "$(cat /tmp/j-served-leaf.sha 2>/dev/null)" = "$(cat /tmp/j-issued-leaf.sha 2>/dev/null)" ] && [ -s /tmp/j-served-leaf.sha ]
assert "J4-served-cert-equals-issued" $? "served=$(cat /tmp/j-served-leaf.sha 2>/dev/null) issued=$(cat /tmp/j-issued-leaf.sha 2>/dev/null)"
grep -qi 'Pebble Intermediate CA' /tmp/j-showcerts.txt
assert "J4-cert-issuer-pebble" $? "chain=$(grep -E '^ [0-9] s:|i:CN' /tmp/j-showcerts.txt 2>/dev/null | head -n 4 | tr '\n' ' ' | head -c 220)"
CRT_SHA=$(sha256sum /var/lib/fleetly/fleetly-certs/"$APP".crt 2>/dev/null | awk '{print $1}')
[ "$CRT_SHA" = "$CERT_SHA" ]
assert "J4-domain-ledger-cert-sha" $? "ledger=$CERT_SHA file=$CRT_SHA"

# 证书来自 Pebble 签发：TLS 链 issuer = pebble minica 根（根证书直证）+
# 台账 cert_sha256。同时保留 curl 容器链路作旁证（非断言）。
openssl s_client -connect 127.0.0.1:443 -servername "$DOMAIN" -showcerts </dev/null 2>/dev/null >/tmp/j-showcerts.txt
grep -i 'Pebble Root CA' /tmp/j-showcerts.txt
assert "J4-cert-issuer-pebble" $? "chain=$(grep -E '^ [0-9] s:|i:CN' /tmp/j-showcerts.txt 2>/dev/null | head -n 4 | tr '\n' ' ' | head -c 220)"

# ACME/挑战链证据行（如实摘录，非断言；签发失败在此留痕）。
grep -i 'acme\|challenge\|certificate\|route publish' "$DLOG" 2>/dev/null | tail -n 8 | sed 's/^/acme-evidence: /' || true

# ------------------------------------------------------------- J5 UI 可见
ui_code=$(http_code "$HTTP/ui/")
[ "$ui_code" = '200' ]
assert "J5-ui-index-200" $? "code=$ui_code"
wget -q -T 5 -O "$JK_STAGE/ui-index.html" "$HTTP/ui/" 2>/dev/null
assert "J5-ui-index-fetched" $?
UI_ASSET=$(grep -o '/ui/assets/index-[A-Za-z0-9_-]*\.js' "$JK_STAGE/ui-index.html" 2>/dev/null | head -n 1)
[ -n "$UI_ASSET" ]
assert "J5-ui-asset-ref-found" $? "asset=$UI_ASSET"
ui_asset_code=$(http_code "$HTTP$UI_ASSET")
[ "$ui_asset_code" = '200' ]
assert "J5-ui-asset-200" $? "asset=$UI_ASSET code=$ui_asset_code"

APPS_JSON=$(wget -q -T 5 -O - --header "Authorization: Bearer $TOKEN" "$HTTP/v1/apps" 2>/dev/null)
printf '%s' "$APPS_JSON" | grep -q "\"name\": *\"$APP\""
assert "J5-rest-apps-has-journeyapp" $?
printf '%s' "$APPS_JSON" | grep -q '"derived_state": *"running"'
assert "J5-rest-apps-derived-running" $?

# 日志历史有行（sidecar 心跳；轮询等采集周期）。
_log_ok=0
_deadline=$(( $(date +%s) + 120 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    LOG_BODY=$(wget -q -T 5 -O - --header "Authorization: Bearer $TOKEN" "$HTTP/v1/apps/$APP/logs" 2>/dev/null)
    case "$LOG_BODY" in
    *journey-sidecar-heartbeat*)
        _log_ok=1
        break
        ;;
    esac
    sleep 5
done
[ "$_log_ok" -eq 1 ]
assert "J5-logs-history-has-lines" $? "logs=$(printf '%s' "$LOG_BODY" | head -c 120)"

# env set → pending 可见（配置面）。
cli env set "$APP" JOURNEY_DEMO hello >"$JK_STAGE/envset.log" 2>&1
assert "J5-env-set-rc0" $? "$(tail -n 2 "$JK_STAGE/envset.log")"
ENV_BODY=$(wget -q -T 5 -O - --header "Authorization: Bearer $TOKEN" "$HTTP/v1/apps/$APP/env" 2>/dev/null)
printf '%s' "$ENV_BODY" | grep -q '"key": *"JOURNEY_DEMO"'
assert "J5-env-row-visible" $?
printf '%s' "$ENV_BODY" | grep -q '"status": *"pending"'
assert "J5-env-pending-visible" $? "env=$(printf '%s' "$ENV_BODY" | head -c 160)"

# ------------------------------------------- J6 push v2 → 一键回滚（旧版重放）
T_PUSH2_0=$(date +%s)
sed -i 's/journey-sidecar-heartbeat-v1-/journey-sidecar-heartbeat-v2-/' "$SRC_DIR/compose.yaml"
grep -q 'heartbeat-v2-' "$SRC_DIR/compose.yaml"
assert "J6-v2-compose-differs" $?
(
    cd "$SRC_DIR" &&
        git add -A >/dev/null &&
        git commit -m 'journey v2' >/dev/null
)
assert "J6-v2-committed" $?
GIT_SSH_COMMAND='ssh -i /root/.ssh/journey_key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' \
    git -C "$SRC_DIR" push "$GIT_URL" main:main >"$JK_STAGE/push2.log" 2>&1
assert "J6-git-push2-rc0" $? "$(tail -n 3 "$JK_STAGE/push2.log")"
SHA2=$(git -C "$SRC_DIR" rev-parse HEAD)
wait_git_deploy "$APP" "$SHA2" 300
assert "J6-deploy2-succeeded" $? "sha=$SHA2"
T_DEPLOY2=$(date +%s)
nl "J6 push2-to-deployed wall: $((T_DEPLOY2 - T_PUSH2_0))s"

REV1=$(revision_id_for_seq "$APP" 1)
[ -n "$REV1" ]
assert "J6-revision1-resolved" $? "rev1=$REV1"
cli rollback --to "$REV1" --timeout 240s "$APP" >"$JK_STAGE/rollback.log" 2>&1
assert "J6-rollback-rc0" $? "$(tail -n 3 "$JK_STAGE/rollback.log")"
# 快照重放断言（revision 语义 = 部署行成功后回填「本次部署固化」的新版本行
# ——state/deployments.go RevisionID 注释口径）：回滚产生 seq=3 的新版本行，
# 其 desired_hash 必须与 seq=1（v1）一致 = 旧版内容重放；部署行 kind=rollback
# + succeeded + revision_id = 新版本行。
REVS_JSON=$(cli revisions list --json "$APP" 2>/dev/null || printf '{}')
NEW_REV_ID=$(json_str "$REVS_JSON" id)
NEW_REV_SEQ=$(json_num "$REVS_JSON" seq)
NEW_REV_HASH=$(json_str "$REVS_JSON" desired_hash)
REV1_HASH=$(revision_field_for_seq "$APP" 1 desired_hash)
[ -n "$NEW_REV_ID" ] && [ "$NEW_REV_SEQ" -ge 3 ]
assert "J6-rollback-created-new-revision" $? "newest=$NEW_REV_ID seq=$NEW_REV_SEQ (want ≥3)"
[ -n "$REV1_HASH" ] && [ "$NEW_REV_HASH" = "$REV1_HASH" ]
assert "J6-rollback-replays-v1-spec" $? "hash(newest)=$NEW_REV_HASH hash(v1)=$REV1_HASH"
cli deployments list --limit 10 --json "$APP" 2>/dev/null |
    awk -v rev="$NEW_REV_ID" 'BEGIN {RS = "}"} $0 ~ /"kind": *"rollback"/ && $0 ~ /"status": *"succeeded"/ && $0 ~ ("\"revision_id\": \"" rev "\"") {found = 1} END {exit !found}' >/dev/null
assert "J6-rollback-row-old-revision" $? "new-rev=$NEW_REV_ID"
wait_app_running "$APP" 120
assert "J6-app-running-after-rollback" $? "state=$(derived_state "$APP")"
# J6b 复检：再次发布后的 HTTP/HTTPS 路由（为 J4a 的 404 定性：一次性被拒
# 配置随下次发布自愈 vs 持续性问题）。
_i=0
while [ "$(http_code "http://127.0.0.1/" "$DOMAIN")" != '200' ] && [ "$_i" -lt 30 ]; do
    sleep 2
    _i=$((_i + 1))
done
[ "$(http_code "http://127.0.0.1/" "$DOMAIN")" = '200' ]
assert "J6b-http-route-200-after-republish" $?
HTTPS_PROBE2=''
_j6c_ok=0
_deadline=$(( $(date +%s) + 90 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    HTTPS_PROBE2=$(printf 'GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n' "$DOMAIN" |
        openssl s_client -quiet -connect 127.0.0.1:443 -servername "$DOMAIN" 2>/tmp/j-sclient2.err)
    case "$HTTPS_PROBE2" in
    *'HTTP/1.1 200'*)
        _j6c_ok=1
        break
        ;;
    esac
    sleep 3
done
[ "$_j6c_ok" -eq 1 ]
assert "J6c-https-200-after-republish" $? "probe=$(printf '%s' "$HTTPS_PROBE2" | head -n 1) err=$(grep -i 'verify error\|alert' /tmp/j-sclient2.err 2>/dev/null | head -n 2 | tr '\n' ' ')"
if [ "$_j6c_ok" -eq 0 ]; then
    # 回滚后 TLS 丢失定位：平台视图 TLS 段 / Traefik 运行时路由 / 证书卷。
    ITOK=$(cat /var/lib/fleetly/fleetly-ingress.token 2>/dev/null)
    wget -q -T 5 -O /tmp/j-configs-diag2.json --header "Authorization: Bearer $ITOK" http://127.0.0.1:8422/configs 2>/dev/null
    nl "j6c-configs-tls: $(grep -o '"certificates"' /tmp/j-configs-diag2.json 2>/dev/null | wc -l | tr -d ' ') section(s), routers: $(grep -o '"fleetly-[a-z0-9-]*"' /tmp/j-configs-diag2.json 2>/dev/null | sort -u | paste -sd,)"
    _TR_CTR=$(docker ps --format '{{.Names}}' 2>/dev/null | grep '^fleetly-ingress\.' | head -n 1)
    [ -n "$_TR_CTR" ] && nl "j6c-traefik-routers: $(docker exec "$_TR_CTR" wget -q -T 5 -O - http://127.0.0.1:8080/api/http/routers 2>&1 | head -c 600)"
    nl "j6c-cert-file: $(ls -la /var/lib/fleetly/fleetly-certs/ 2>/dev/null | tail -n 3 | tr '\n' ' ')"
fi
T_ROLLBACK=$(date +%s)
nl "J6 rollback wall: $((T_ROLLBACK - T_DEPLOY2))s"

# ------------------------------------------------- J7 信任闭环引用（T2-9b）
BK_JSON=$(cli backups list --json 2>/dev/null || printf '{}')
printf '%s' "$BK_JSON" | grep -q '"verify_status": "verified"'
assert "J7-backup-ledger-verified" $?
printf '%s' "$BK_JSON" | grep -q '"kind": "daily"'
assert "J7-backup-daily-kind" $?
printf '%s' "$BK_JSON" | grep -q '"verify_status": "failed"'
[ $? -ne 0 ]
assert "J7-backup-no-failed-rows" $?

# ------------------------------------------------------------- J8 计时汇总
END_T=$(date +%s)
TOTAL=$((END_T - T0))
[ "$TOTAL" -le 1200 ]
assert "J8-total-within-20min" $? "total=${TOTAL}s (T0→收尾；CRITICAL=${CRITICAL}s)"

INSTALL_S=$((T_INSTALL - T_INSTALL0))
PREP_S=$((T_PREP - T_PREP0))
PUSH1_S=$((T_DEPLOY1 - T_PUSH0))
HTTPS_S=$((T_HTTPS - T_HTTPS0))
PUSH2_S=$((T_DEPLOY2 - T_PUSH2_0))
ROLL_S=$((T_ROLLBACK - T_DEPLOY2))

{
    printf '## fleetly T2.26 v0.1 端到端旅程（dind 全链）\n\n'
    printf '| 阶段 | 耗时(s) | 断言 |\n|---|---|---|\n'
    printf '| J1 安装（--bin-dir --no-systemd） | %s | rc0 + 报告 4 项 |\n' "$INSTALL_S"
    printf '| J2 装配（config/pebble/keys/hosts，**不计入 ≤20min**） | %s | 14 项 |\n' "$PREP_S"
    printf '| J3 git push → 部署 succeeded | %s | push rc0 + sha 行 succeeded |\n' "$PUSH1_S"
    printf '| J4 HTTPS 200（TLS 200 + 服务链≡签发链 + issuer=Pebble Intermediate） | %s | 5 项断言 |\n' "$HTTPS_S"
    printf '| J5 UI/数据（/ui/ + REST + logs + env pending） | — | 8 项 |\n'
    printf '| J6 push v2 → rollback(seq=1) | %s + %s | 快照重放 revision 断言 |\n' "$PUSH2_S" "$ROLL_S"
    printf '| J7 备份台账 verified（T2-9b 引用） | — | 3 项 |\n'
    printf '\n| 计时 | 秒 |\n|---|---|\n'
    printf '| CRITICAL（安装开始→HTTPS 200；DNS/hosts 不计） | %s |\n' "$CRITICAL"
    printf '| TOTAL（T0→全部阶段收尾） | %s |\n' "$TOTAL"
    printf '\n| 事实 | 值 |\n|---|---|\n'
    printf '| dind 镜像 | `%s` |\n' "$DIND_TAG"
    printf '| fleetlyd 版本 | `%s` |\n' "$VERSION"
    printf '| sha1(v1) | `%s` |\n' "$SHA1"
    printf '| sha2(v2) | `%s` |\n' "$SHA2"
    printf '| 证书 cert_sha256 | `%s` |\n' "$CERT_SHA"
    printf '| 回滚目标 revision | `%s` |\n' "$REV1"
    printf '| dind IP（hosts 代演 DNS） | `%s` |\n' "$DIND_IP"
} >"$OUT/summary.md"

{
    printf '{\n'
    printf '  "journeyed_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '  "dind_image": "%s",\n' "$DIND_TAG"
    printf '  "version": "%s",\n' "$VERSION"
    printf '  "phase_seconds": {"install": %s, "prep": %s, "push_to_deploy": %s, "https_wait": %s, "push2_to_deploy": %s, "rollback": %s},\n' \
        "$INSTALL_S" "$PREP_S" "$PUSH1_S" "$HTTPS_S" "$PUSH2_S" "$ROLL_S"
    printf '  "critical_seconds": %s,\n' "$CRITICAL"
    printf '  "total_seconds": %s,\n' "$TOTAL"
    printf '  "sha1": "%s",\n' "$SHA1"
    printf '  "sha2": "%s",\n' "$SHA2"
    printf '  "cert_sha256": "%s",\n' "$CERT_SHA"
    printf '  "rollback_revision": "%s"\n' "$REV1"
    printf '}\n'
} >"$OUT/journey.json"

nl '--- summary ---'
cat "$OUT/summary.md"
nl "JK-INNER-DONE critical=${CRITICAL}s total=${TOTAL}s"
finish
