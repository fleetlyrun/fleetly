#!/bin/sh
# deploy/test-upgrade.sh — T2.23 平台自升级 dind 验收（docker:29.8.1-dind 容器
# 内执行；宿主侧编排命令见 run-upgrade-test.sh 与 deploy/README.md）。
#
# 前置：宿主已把以下文件经 exec+stdin 注入 $UG_STAGE（禁用 docker cp）：
#   $UG_STAGE/install.sh        deploy/install.sh
#   $UG_STAGE/upgrade.sh        deploy/upgrade.sh
#   $UG_STAGE/bin-vA/fleetlyd   linux 二进制（版本串 = $VERSION_A；交叉编译）
#   $UG_STAGE/bin-vA/fleetly
#   $UG_STAGE/bin-vB/fleetlyd   linux 二进制（版本串 = $VERSION_B；vA ≠ vB）
#   $UG_STAGE/bin-vB/fleetly
#   $UG_STAGE/bin-vC/fleetlyd   linux 二进制（版本串 = $VERSION_C；注入迁移
#                               00099 的 schema-skew 变体，S4/F5/S20——CLI
#                               由本套件在 dind 内伪造，无需真实件）
#   $UG_STAGE/probe.tar.gz      探针应用镜像（宿主 docker save | gzip；scratch
#                               + 静态二进制，dind 内 docker load，零 registry
#                               依赖；镜像名 probeapp:1）
#
# 场景与断言清单（对应 T2.23 验收标准）：
#   U1  安装 vA（--bin-dir --no-systemd）+ 自写配置（ACME 关、快引擎参数）
#   U2  daemon 启动 + liveness + bootstrap token + CLI 可用 + ping=vA
#   U3  探针镜像构建 + fleetly-ingress（Traefik global）1/1 + drillapp 部署
#       成功 + apps derived_state=running + 入口路由 200 + 常驻探针起表
#   U4  基线台账：daemon 首启 daily 备份 verified（备份链前置）
#   S1  正常升级 vA→vB：upgrade.sh rc0 + 版本=vB + derived_state 不变 +
#       全程 probe 零失败（应用不停 E2E）+ pre_upgrade 备份 verified +
#       manifest 密钥指纹/密钥分离断言
#   S2  失败自动回退：坏 vB'（截断 ELF，启动即退）→ upgrade rc!=0 + 输出
#       ROLLED BACK + 版本回到 S1 之后的 vB（单步回滚语义 = 回到最后一个
#       运行过的可用版本，而非最初版本）+ daemon healthy + probe 仍零失败 +
#       诊断明确（坏件不留存、previous 归位）
#   S3  备份链：台账 kind/verify_status 全断言（无 failed 行、
#       pre_upgrade verified、daily≥2 拍）+ 台账行数 = 备份目录数
#   S4  F5/S20 回退 schema 感知：vC（注入迁移 00099 的「新版本」）启动即把
#       DB 迁到 vB 之上，伪造 CLI 版本串令 ⑦ 验证失败 → 回退路径：
#       S4a 无 --auto-restore → die + 三步人肉指引（daemon 诚实停机、DB
#       仍为高版本）+ 指引人工照做可恢复；S4b --auto-restore → 快照校验 +
#       自动恢复 + 旧件拉起 + ROLLED BACK（带 auto-restore 行）
#   收尾 探针全程零失败收表（覆盖 S1+S2+S4 全部 daemon 停启窗口——应用面
#       与 daemon 解耦的实证）。
#
# 断言风格与 test-install.sh 一致（NAME: PASS/FAIL + 退出码）。

set -u
# shellcheck disable=SC2034
UG_FAIL=0

UG_STAGE="${UG_STAGE:-/tmp/upgrade-test}"
STAGE_BIN_A="$UG_STAGE/bin-vA"
STAGE_BIN_B="$UG_STAGE/bin-vB"
INSTALL_SH="$UG_STAGE/install.sh"
UPGRADE_SH="$UG_STAGE/upgrade.sh"
PROBE_BIN="$UG_STAGE/probe"
PROBE_TAR="$UG_STAGE/probe.tar.gz"
DLOG="/tmp/ug-fleetlyd.log"
PID_FILE="/var/run/fleetlyd.pid"
PROBE_JSON="/tmp/probe.json"
DRILL_YAML="$UG_STAGE/drillapp.yaml"
PROBE_OUT="/tmp/ug-probe.log"

VERSION_A=v0.1.0-uga
VERSION_B=v0.1.0-ugb
DOMAIN=drill.test.local
HTTP=http://127.0.0.1:8420

nl() { printf '[ug %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    UG_FAIL=$((UG_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then
        pass "$1"
    else
        fail "$1" "${3:-}"
    fi
}
finish() {
    if [ "$UG_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$UG_FAIL"
    nl "---- fleetlyd log tail ($DLOG) ----"
    tail -n 60 "$DLOG" 2>/dev/null || true
    nl "---- probe output tail ($PROBE_OUT) ----"
    tail -n 20 "$PROBE_OUT" 2>/dev/null || true
    exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

http_code() { # <url> [host]
    # 注意：dind（docker:29.8.1-dind）无 curl，只有 busybox wget；wget -S
    # 的状态行走 stderr——必须 2>&1 收编，且行首有空格（gsub 先剥空格）。
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

http_get() { # <url> — 输出 body（失败输出 {}）
    if have curl; then
        curl -fsS --max-time 5 "$1" 2>/dev/null || printf '{}'
    else
        wget -q -T 5 -O - "$1" 2>/dev/null || printf '{}'
    fi
}

# json_str / json_num — 定点字段提取（indent JSON；非通用解析器）。
json_str() {
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

cli() { # <args...> — 带 conn env 的 fleetly CLI（经 SDK 打 gRPC 面）
    FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN="$TOKEN" /opt/fleetly/bin/fleetly "$@"
}

# 轮询谓词（全部在本 shell 执行——不用 sh -c 子壳，套件函数不可导出）。
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
wait_traefik() {
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        traefik_ready && return 0
        sleep 3
    done
    return 1
}
route_ok() {
    [ "$(http_code "http://127.0.0.1/" "$DOMAIN")" = '200' ]
}
wait_route() {
    _deadline=$(( $(date +%s) + $1 ))
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        route_ok && return 0
        sleep 2
    done
    return 1
}
derived_states() {
    cli apps list --json 2>/dev/null |
        grep -o '"derived_state": *"[a-z]*"' |
        sed 's/.*: *"//; s/"$//' |
        sort
}
first_derived_state() {
    derived_states | head -n 1
}
probe_counters() {
    cat "$PROBE_JSON" 2>/dev/null || printf '{}'
}
ping_version() {
    json_str "$(http_get "$HTTP/v1/system/ping")" version
}

nl "=== T2.23 upgrade dind suite (stage=$UG_STAGE) ==="

# ------------------------------------------------------------ U0 preflight
for _f in "$INSTALL_SH" "$UPGRADE_SH" "$STAGE_BIN_A/fleetlyd" "$STAGE_BIN_A/fleetly" \
    "$STAGE_BIN_B/fleetlyd" "$STAGE_BIN_B/fleetly" "$UG_STAGE/bin-vC/fleetlyd" "$PROBE_TAR"; do
    [ -s "$_f" ] || {
        nl "FATAL: staged file missing: $_f"
        exit 1
    }
done
# CRLF 就地剥离（busybox ash 无法执行带 CR 的脚本；二进制绝不 sed）。
sed -i 's/\r$//' "$INSTALL_SH" "$UPGRADE_SH" 2>/dev/null || true
sh -n "$UPGRADE_SH"
assert "U0-shn-upgrade" $?
VA_VER=$("$STAGE_BIN_A/fleetly" version 2>/dev/null | awk '{print $2}')
VB_VER=$("$STAGE_BIN_B/fleetly" version 2>/dev/null | awk '{print $2}')
nl "staged vA: $VA_VER / vB: $VB_VER"
[ "$VA_VER" = "$VERSION_A" ]
assert "U0-vA-version" $? "got $VA_VER"
[ "$VB_VER" = "$VERSION_B" ]
assert "U0-vB-version" $? "got $VB_VER"

# ------------------------------------------------------------ U1 安装 vA
sh "$INSTALL_SH" --bin-dir "$STAGE_BIN_A" --no-systemd >"/tmp/ug-install.log" 2>&1
RC=$?
assert "U1-install-vA-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    cat "/tmp/ug-install.log" || true
    fail "U1-install-aborted-suite" "cannot continue without a working install"
    finish
fi

# 自写配置：ACME 关闭（离线 dind）、快引擎参数（观察窗 5s）、备份缺省 keep=7。
mkdir -p /opt/fleetly/etc /var/lib/fleetly
# 预拉 ingress 依赖镜像（cert seed 容器与 Traefik 服务不自动拉镜像——
# T2.15 已知边界，sweep 周期长；测试环境在 daemon 启动前显式预备）。
docker pull alpine:3.20 >/dev/null 2>&1 || true
docker pull traefik:v3.5 >/dev/null 2>&1 || true
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
assert "U1-config-written" $?

# ------------------------------------------------------------ U2 daemon 启动
(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90
assert "U2-liveness-200" $?
if [ "$(http_code "$HTTP/healthz/liveness")" != '200' ]; then
    nl "---- U2 diagnostics: daemon log ($(ls -la "$DLOG" 2>/dev/null || echo missing)) ----"
    ps 2>/dev/null | head -n 12 || true
    tail -n 40 "$DLOG" 2>/dev/null || true
    fail "U2-aborted-suite" "cannot continue without a live daemon"
    finish
fi
# B5：token 本体不再进日志——首启写入 <数据根>/bootstrap-token（0600）。
TOKEN=$(cat /var/lib/fleetly/bootstrap-token 2>/dev/null)
[ -n "$TOKEN" ]
assert "U2-bootstrap-token" $?
[ -n "$TOKEN" ] || {
    fail "U2-aborted-suite" "no token"
    finish
}
cli apps list >"/tmp/ug-cli.out" 2>&1
assert "U2-cli-apps-list" $? "$(tail -n 2 /tmp/ug-cli.out)"
[ "$(ping_version)" = "$VERSION_A" ]
assert "U2-ping-version-vA" $? "got $(ping_version)"

# ------------------------------------------------------------ U3 探针 + 部署
docker load <"$UG_STAGE/probe.tar.gz" >"/tmp/ug-load.log" 2>&1
assert "U3-probe-image-loaded" $?
if ! docker image inspect probeapp:1 >/dev/null 2>&1; then
    tail -n 20 "/tmp/ug-load.log" || true
    fail "U3-probe-image-tag" "probeapp:1 missing after load"
    finish
fi

cat > "$DRILL_YAML" <<EOF
name: drillapp
services:
  web:
    image: probeapp:1
    # compose command = 容器 Command（完整 argv 覆盖，含镜像 entrypoint 位）。
    command: ["/probe", "serve"]
    expose: ["8080"]
    healthcheck:
      test: ["CMD", "/probe", "hc"]
    labels:
      fleetly.domains: "$DOMAIN"
EOF

# Traefik（fleetly-ingress global service）就绪——「应用不停」双轨的前提件。
wait_traefik 240
assert "U3-traefik-1-1" $?
docker service ls 2>/dev/null | head -n 5 || true

cli deploy --timeout 240s "$DRILL_YAML" >"/tmp/ug-deploy.log" 2>&1
RC=$?
assert "U3-deploy-rc0" "$RC" "$(tail -n 3 /tmp/ug-deploy.log)"
if [ "$RC" -ne 0 ]; then
    nl "---- deployments dump ----"
    cli deployments list --json drillapp 2>/dev/null || true
    docker service ps -a 2>/dev/null | head -n 10 || true
fi

_i=0
while [ "$(first_derived_state)" != "running" ] && [ "$_i" -lt 30 ]; do
    _i=$((_i + 2))
    sleep 2
done
[ "$(first_derived_state)" = "running" ]
assert "U3-app-running" $? "state=$(first_derived_state)"

wait_route 60
assert "U3-route-200" $? "route=$(http_code "http://127.0.0.1/" "$DOMAIN")"

# 常驻探针（dind 宿主侧进程——打宿主 80 经 Traefik Host 路由到应用）。
"$PROBE_BIN" watch -url "http://127.0.0.1/" -host "$DOMAIN" -out "$PROBE_JSON" -every 1s >"$PROBE_OUT" 2>&1 &
PROBE_PID=$!
sleep 4
PT=$(json_num "$(probe_counters)" total)
[ -n "$PT" ] && [ "$PT" -ge 1 ]
assert "U3-probe-sampling" $? "probe total=$PT"

# ------------------------------------------------------------ U4 基线台账
LEDGER=$(cli backups list --json 2>/dev/null || printf '{}')
printf '%s' "$LEDGER" | grep -q '"verify_status": "verified"'
assert "U4-daily-backup-verified" $?
printf '%s' "$LEDGER" | grep -q '"kind": "daily"'
assert "U4-daily-kind-present" $?
printf '%s' "$LEDGER" | grep -q '"kind": "pre_upgrade"'
[ $? -ne 0 ]
assert "U4-no-pre-upgrade-yet" $?

# ------------------------------------------------------------ S1 正常升级
BASELINE=$(derived_states)
nl "S1: baseline derived_state = $(printf '%s' "$BASELINE" | paste -sd,)"
[ "$BASELINE" = "running" ]
assert "S1-baseline-running" $?

sh "$UPGRADE_SH" --bin-dir "$STAGE_BIN_B" --no-systemd --pid-file "$PID_FILE" \
    --token "$TOKEN" --probe-url "http://127.0.0.1/" --probe-host "$DOMAIN" \
    >"/tmp/ug-s1.log" 2>&1
RC=$?
assert "S1-upgrade-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    nl "---- S1 upgrade output ----"
    cat "/tmp/ug-s1.log" || true
fi
grep -q 'result          : UPGRADED' "/tmp/ug-s1.log"
assert "S1-report-upgraded" $?

[ "$(ping_version)" = "$VERSION_B" ]
assert "S1-version-vB" $? "ping=$(ping_version)"
_i=0
while [ "$(first_derived_state)" != "running" ] && [ "$_i" -lt 30 ]; do
    _i=$((_i + 2))
    sleep 2
done
[ "$(first_derived_state)" = "running" ]
assert "S1-derived-state-unchanged" $? "state=$(first_derived_state)"

# probe 全程零失败（应用不停 E2E 断言——daemon stop/start 窗口内探针持续 200）。
sleep 2
PN=$(probe_counters)
PF=$(json_num "$PN" fail)
PT=$(json_num "$PN" total)
[ "$PF" = '0' ]
assert "S1-probe-zero-failures" $? "probe total=$PT fail=$PF last_err=$(json_str "$PN" last_err)"

# pre_upgrade 备份在台账且 verified；台账无 failed 行（诚实绿色 = 全行 verified）。
LEDGER=$(cli backups list --json 2>/dev/null || printf '{}')
printf '%s' "$LEDGER" | grep -q '"kind": "pre_upgrade"'
assert "S1-pre-upgrade-kind-present" $?
printf '%s' "$LEDGER" | grep -q '"verify_status": "failed"'
[ $? -ne 0 ]
assert "S1-no-failed-rows" $?
BK_ID=$(printf '%s' "$LEDGER" |
    grep -B 1 '"kind": "pre_upgrade"' |
    grep -o '"id": *"[^"]*"' |
    head -n 1 |
    sed 's/.*: *"//; s/"$//')
[ -n "$BK_ID" ]
assert "S1-pre-upgrade-id-extracted" $? "id=$BK_ID"

# manifest 事实：密钥指纹 = 密钥文件 sha256sum；密钥不在备份目录（密钥分离）。
MANIFEST="/var/lib/fleetly/backups/$BK_ID/manifest.json"
[ -f "$MANIFEST" ]
assert "S1-manifest-exists" $? "path=$MANIFEST"
KEY_FP=$(sha256sum /var/lib/fleetly/fleetly.key 2>/dev/null | awk '{print $1}')
MAN_FP=$(json_str "$(cat "$MANIFEST" 2>/dev/null || printf '{}')" key_fingerprint)
[ -n "$MAN_FP" ] && [ "$MAN_FP" = "$KEY_FP" ]
assert "S1-manifest-key-fingerprint" $? "manifest=$MAN_FP key=$KEY_FP"
[ -f "/var/lib/fleetly/backups/$BK_ID/fleetly.db" ]
assert "S1-snapshot-file-exists" $?
ls /var/lib/fleetly/backups/*/fleetly.key >/dev/null 2>&1
[ $? -ne 0 ]
assert "S1-key-not-in-backup-dir" $?

# ------------------------------------------------------------ S2 失败自动回退
BAD_DIR="$UG_STAGE/bin-bad"
mkdir -p "$BAD_DIR"
head -c 400000 "$STAGE_BIN_B/fleetlyd" >"$BAD_DIR/fleetlyd"
cp "$STAGE_BIN_B/fleetly" "$BAD_DIR/fleetly"
chmod 0755 "$BAD_DIR/fleetlyd" "$BAD_DIR/fleetly"
assert "S2-bad-binary-staged" $?

sh "$UPGRADE_SH" --bin-dir "$BAD_DIR" --no-systemd --pid-file "$PID_FILE" \
    --token "$TOKEN" >"/tmp/ug-s2.log" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "S2-upgrade-failed-rc" $? "rc=$RC (want non-zero)"
grep -q 'ROLLED BACK' "/tmp/ug-s2.log"
assert "S2-rolled-back-reported" $?
grep -q 'platform healthy' "/tmp/ug-s2.log"
assert "S2-honest-red-report" $?

wait_liveness 90
assert "S2-daemon-healthy-after-rollback" $?
# 单步回滚语义：回到 S2 尝试前正在运行的版本（= S1 升级后的 vB），不是 vA。
[ "$(ping_version)" = "$VERSION_B" ]
assert "S2-rolled-back-to-previous-vB" $? "ping=$(ping_version)"
# 在跑的 daemon 可执行体确为归位后的平台二进制（previous 已 mv 归位）。
RUNEXE=$(readlink "/proc/$(cat "$PID_FILE" 2>/dev/null)/exe" 2>/dev/null)
case "$RUNEXE" in
/opt/fleetly/bin/fleetlyd) assert "S2-running-binary-restored" 0 ;;
*) assert "S2-running-binary-restored" 1 "exe=$RUNEXE pidfile=$(cat "$PID_FILE" 2>/dev/null)" ;;
esac

# S2 期间应用仍由 Swarm/Traefik 服务——probe 依旧零失败。
sleep 2
PN=$(probe_counters)
PF=$(json_num "$PN" fail)
[ "$PF" = '0' ]
assert "S2-probe-zero-failures" $? "probe fail=$PF last_err=$(json_str "$PN" last_err)"
# 回退后应用仍在服务（路由面复核——Swarm/Traefik 不依赖 daemon 存活）。
wait_route 60
assert "S2-route-200-after-rollback" $? "route=$(http_code "http://127.0.0.1/" "$DOMAIN")"

# ------------------------------------------------------------ S3 备份链
LEDGER=$(cli backups list --json 2>/dev/null || printf '{}')
printf '%s' "$LEDGER" | grep -q '"kind": "pre_upgrade"'
assert "S3-pre-upgrade-in-ledger" $?
printf '%s' "$LEDGER" | grep -q '"verify_status": "failed"'
[ $? -ne 0 ]
assert "S3-no-failed-rows" $?
printf '%s' "$LEDGER" | grep -A 8 '"kind": "pre_upgrade"' | grep -q '"verify_status": "verified"'
assert "S3-pre-upgrade-verified" $?
# daily 至少两拍（vA 首启 + 升级后 vB 首启 + 回退后 vA 首启）。
DAILY_COUNT=$(printf '%s' "$LEDGER" | grep -c '"kind": "daily"')
[ "$DAILY_COUNT" -ge 2 ]
assert "S3-daily-at-least-2" $? "daily rows=$DAILY_COUNT"

# 台账行数 = 备份目录数（保留策略下无幽灵行——目录与账一致）。
LEDGER_COUNT=$(printf '%s' "$LEDGER" | grep -c '"id":')
DIR_COUNT=$(ls -1d /var/lib/fleetly/backups/*/ 2>/dev/null | wc -l | tr -d ' ')
[ "$LEDGER_COUNT" = "$DIR_COUNT" ]
assert "S3-ledger-matches-dirs" $? "ledger=$LEDGER_COUNT dirs=$DIR_COUNT"

# ------------------------------------------------------------ S4 F5 schema 感知
# vC = 带 vB 不认识的更高版本迁移（00099，run-upgrade-test.sh 注入构建）的
# 「新版本」；伪造 CLI 自报版本串与 daemon 实报错位 → ⑦ 验证失败（daemon
# 本身健康、迁移已应用）→ 回退 vB 时触发 F5 schema 感知路径。
VC_REAL="$UG_STAGE/bin-vC"
VC_STAGE="$UG_STAGE/bin-vC-mismatch"
mkdir -p "$VC_STAGE"
[ -s "$VC_REAL/fleetlyd" ]
assert "S4-vC-binary-staged" $?
cp "$VC_REAL/fleetlyd" "$VC_STAGE/fleetlyd"
cat >"$VC_STAGE/fleetly" <<'FAKECLI'
#!/bin/sh
# S4（F5/S20）测试专用伪造 CLI：upgrade.sh --bin-dir 形态经 CLI 自报
# TARGET_VERSION——本脚本自报一个与 daemon 实报不一致的版本串，制造
# 「daemon 健康（迁移已应用）但 ⑦ 版本验证失败」的精确回退触发现场。
echo "fleetly v0.1.0-f5-mismatch-target"
FAKECLI
chmod 0755 "$VC_STAGE/fleetlyd" "$VC_STAGE/fleetly"
assert "S4-mismatch-bin-dir-staged" $?

# S4a：无 --auto-restore——schema 错配在拉起旧件之前被显式拦截（die +
# 三步指引），daemon 诚实停机、DB 保持高版本（不替用户决定数据回滚）。
sh "$UPGRADE_SH" --bin-dir "$VC_STAGE" --no-systemd --pid-file "$PID_FILE" \
    --token "$TOKEN" >"/tmp/ug-s4a.log" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "S4a-upgrade-failed-rc" $? "rc=$RC (want non-zero)"
grep -q 'schema above rollback target' "/tmp/ug-s4a.log"
assert "S4a-schema-guard-message" $?
grep -q 'auto-restore' "/tmp/ug-s4a.log"
assert "S4a-manual-guidance-given" $?
[ "$(http_code "$HTTP/healthz/liveness")" != '200' ]
assert "S4a-daemon-left-stopped" $? "liveness=$(http_code "$HTTP/healthz/liveness")"
SV_OUT=$(/opt/fleetly/bin/fleetlyd schema-version -c /opt/fleetly/etc/config.yaml 2>&1)
SV_DB=$(printf '%s\n' "$SV_OUT" | sed -n 's/^db=//p')
SV_MAX=$(printf '%s\n' "$SV_OUT" | sed -n 's/^max=//p')
[ -n "$SV_DB" ] && [ "$SV_DB" -gt "$SV_MAX" ]
assert "S4a-db-still-above-rollback-binary" $? "db=$SV_DB max=$SV_MAX"

# 指引可行动性（S4a 的三步人肉指引照做即恢复）：取本次运行的 pre_upgrade
# 快照（upgrade.sh ② 步日志行携带 ID——此刻 daemon 已停，不能走 CLI 台账）
# → sha256 对照 manifest → 拷贝入库（清残留 WAL/SHM）→ 旧件拉起。
BK4=$(sed -n 's/.*pre-upgrade backup verified: //p' "/tmp/ug-s4a.log" | head -n 1)
[ -n "$BK4" ]
assert "S4a-restore-snapshot-id-extracted" $? "id=$BK4"
SNAP="/var/lib/fleetly/backups/$BK4/fleetly.db"
MAN_SHA=$(json_str "$(cat "/var/lib/fleetly/backups/$BK4/manifest.json" 2>/dev/null || printf '{}')" sha256)
SNAP_SHA=$(sha256sum "$SNAP" 2>/dev/null | awk '{print $1}')
[ -n "$MAN_SHA" ] && [ "$MAN_SHA" = "$SNAP_SHA" ]
assert "S4a-restore-snapshot-sha-ok" $? "manifest=$MAN_SHA actual=$SNAP_SHA"
mv /var/lib/fleetly/fleetly.db /var/lib/fleetly/fleetly.db.s4a-aside
rm -f /var/lib/fleetly/fleetly.db-wal /var/lib/fleetly/fleetly.db-shm
cp "$SNAP" /var/lib/fleetly/fleetly.db
(
    cd /var/lib/fleetly
    nohup /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml >>"$DLOG" 2>&1 &
    echo $! >"$PID_FILE"
)
wait_liveness 90
assert "S4a-manual-restore-daemon-up" $?
[ "$(ping_version)" = "$VERSION_B" ]
assert "S4a-manual-restore-vB-serving" $? "ping=$(ping_version)"

# S4b：同一现场 + --auto-restore——自动闭环：快照校验 + 恢复 + 旧件拉起 +
# ROLLED BACK 报告（报告带 auto-restore 行，诚实披露状态库已被回滚）。
sh "$UPGRADE_SH" --bin-dir "$VC_STAGE" --no-systemd --pid-file "$PID_FILE" \
    --token "$TOKEN" --auto-restore >"/tmp/ug-s4b.log" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "S4b-upgrade-failed-rc" $? "rc=$RC (want non-zero)"
grep -q 'ROLLED BACK' "/tmp/ug-s4b.log"
assert "S4b-rolled-back-reported" $?
grep -q 'auto-restore    : yes' "/tmp/ug-s4b.log"
assert "S4b-auto-restore-reported" $?
wait_liveness 90
assert "S4b-daemon-healthy-after-auto-restore" $?
[ "$(ping_version)" = "$VERSION_B" ]
assert "S4b-vB-serving" $? "ping=$(ping_version)"
SV_OUT=$(/opt/fleetly/bin/fleetlyd schema-version -c /opt/fleetly/etc/config.yaml 2>&1)
SV_DB=$(printf '%s\n' "$SV_OUT" | sed -n 's/^db=//p')
SV_MAX=$(printf '%s\n' "$SV_OUT" | sed -n 's/^max=//p')
[ -n "$SV_DB" ] && [ "$SV_DB" -le "$SV_MAX" ]
assert "S4b-db-schema-restored-within-limit" $? "db=$SV_DB max=$SV_MAX"

# 收尾：探针停表 + 全程零失败复核（覆盖 S1+S2+S4 全部 daemon 停启窗口
# ——应用面（Swarm/Traefik）与 daemon 解耦的实证）。
kill "$PROBE_PID" 2>/dev/null || true
PN=$(probe_counters)
PT=$(json_num "$PN" total)
PF=$(json_num "$PN" fail)
nl "probe summary: total=$PT fail=$PF last_err=$(json_str "$PN" last_err)"
[ "$PF" = '0' ] && [ "$PT" -ge 20 ]
assert "S3-probe-final-zero-fail" $? "probe total=$PT fail=$PF"

finish
