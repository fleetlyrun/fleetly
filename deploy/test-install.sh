#!/bin/sh
# deploy/test-install.sh — T2.1 安装器 dind 验收（在 docker:29.8.1-dind 容器内
# 执行；宿主侧编排命令见 deploy/README.md）。
#
# 前置：宿主已把以下文件经 exec+stdin 注入 $TI_STAGE（默认 /tmp/install-test，
# docker cp 在 Engine 29.x 宿主→特权 dind 会静默丢文件，禁用）：
#   $TI_STAGE/install.sh          deploy/install.sh
#   $TI_STAGE/uninstall.sh        deploy/uninstall.sh
#   $TI_STAGE/fleetlyd.service    deploy/fleetlyd.service（unit 参考模板）
#   $TI_STAGE/bin/fleetlyd        linux 二进制（交叉编译，CGO_ENABLED=0）
#   $TI_STAGE/bin/fleetly         linux 二进制
#
# 断言清单（对应 stage 验收标准）：
#   A1  语法：install.sh / uninstall.sh / fleetlyd.service 三件套 sh -n 通过
#   A2  正路径：--bin-dir + --no-systemd 安装成功（目录/二进制/配置/参考
#       unit/符号链接齐全；unit 与 deploy/fleetlyd.service 模板逐字一致；
#       报告含 advertise-addr 判定与端口面提示）
#   A3  swarm init：dind 内无 swarm → 安装后 Swarm active，advertise-addr
#       为私网 IP（dind 环境判定）
#   A4  daemon 手动启动（--no-systemd 口径）→ /healthz/liveness 200；
#       git SSH 8424 监听；gRPC 8421 只绑回环
#   A5  CLI 可连：bootstrap token 从 daemon 日志抓取 → fleetly apps list
#   A6  重装幂等：已有 swarm → 跳过 init 不报错；已有 config 保留
#   A7  门禁负路径（版本）：假 docker（28.3.2）→ 拒绝、退出非零、输出原因
#   A8  门禁负路径（iptables）：nftables-only shim → 拒绝、退出非零
#   A9  卸载：unit/二进制/配置/符号链接消失，/var/lib/fleetly 保留且明示；
#       --purge 后数据目录消失
#   A10 探测 host 推导（整改⑤）：health_probe_host 对非回环 bind 地址打
#       真实 host、通配/空 host 回落 127.0.0.1（非回环绑定时硬编码回环
#       探测恒拒连——安装报红但平台在跑的假死形态）
#
# 断言风格与 e2e/nightly/lib.sh 一致（NAME: PASS/FAIL + 退出码），但独立
# 存放（e2e/ 只读，本脚本不引用它）。

set -u
# shellcheck disable=SC2034
NL_FAIL=0

TI_STAGE="${TI_STAGE:-/tmp/install-test}"
STAGE_BIN="$TI_STAGE/bin"
INSTALL_SH="$TI_STAGE/install.sh"
UNINSTALL_SH="$TI_STAGE/uninstall.sh"
SERVICE_TPL="$TI_STAGE/fleetlyd.service"
INSTALL_LOG="/tmp/ti-install.log"
REINSTALL_LOG="/tmp/ti-reinstall.log"
NEG_VER_LOG="/tmp/ti-neg-version.log"
NEG_IPT_LOG="/tmp/ti-neg-iptables.log"
UNINSTALL_LOG="/tmp/ti-uninstall.log"
DLOG="/tmp/ti-fleetlyd.log"

nl() { printf '[ti %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
pass() { nl "$1: PASS"; }
fail() {
    nl "$1: FAIL ${2:-}"
    # shellcheck disable=SC2034
    NL_FAIL=$((NL_FAIL + 1))
}
assert() { # <name> <0|1> [detail]
    if [ "$2" -eq 0 ]; then
        pass "$1"
    else
        fail "$1" "${3:-}"
    fi
}
finish() {
    if [ "$NL_FAIL" -eq 0 ]; then
        nl "SUITE-DONE all-asserts-passed"
        exit 0
    fi
    nl "SUITE-DONE failed-asserts=$NL_FAIL"
    nl "---- fleetlyd log tail ($DLOG) ----"
    tail -n 40 "$DLOG" 2>/dev/null || true
    nl "---- end fleetlyd log tail ----"
    exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

http_get() { # <url> — 2xx 时退出 0
    if have curl; then
        curl -fsS --max-time 3 -o /dev/null "$1"
    else
        wget -q -T 3 -O /dev/null "$1" 2>/dev/null
    fi
}

nl "=== T2.1 installer dind suite (stage=$TI_STAGE) ==="

# ---------------------------------------------------- preflight + A1 语法
for _f in "$INSTALL_SH" "$UNINSTALL_SH" "$SERVICE_TPL" "$STAGE_BIN/fleetlyd" "$STAGE_BIN/fleetly"; do
    [ -s "$_f" ] || {
        nl "FATAL: staged file missing: $_f (host-side exec+stdin staging incomplete)"
        exit 1
    }
done
# 本地检出可能是 CRLF：busybox ash 无法执行带 CR 的脚本——统一就地剥离。
sed -i 's/\r$//' "$INSTALL_SH" "$UNINSTALL_SH" "$SERVICE_TPL" 2>/dev/null || true

sh -n "$INSTALL_SH"
assert "A1-shn-install" $?
sh -n "$UNINSTALL_SH"
assert "A1-shn-uninstall" $?

# ---------------------------------------------------------- A2 正路径安装
# 门禁要求 dind 引擎 >= 29.8.1（镜像 docker:29.8.1-dind 保证）。
ENG=$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)
nl "inner engine: $ENG"

sh "$INSTALL_SH" --bin-dir "$STAGE_BIN" --no-systemd >"$INSTALL_LOG" 2>&1
RC=$?
assert "A2-install-rc0" "$RC" "rc=$RC"
if [ "$RC" -ne 0 ]; then
    nl "---- install output ----"
    cat "$INSTALL_LOG" || true
    finish
fi

[ -x /opt/fleetly/bin/fleetlyd ]
assert "A2-fleetlyd-installed" $?
[ -x /opt/fleetly/bin/fleetly ]
assert "A2-fleetly-installed" $?
[ -f /opt/fleetly/etc/config.yaml ]
assert "A2-config-written" $?
[ -x /usr/local/bin/fleetly ]
assert "A2-cli-symlink" $?
[ "$(ls -l /usr/local/bin/fleetly 2>/dev/null | sed -n 's/.*-> //p')" = "/opt/fleetly/bin/fleetly" ]
assert "A2-cli-symlink-target" $?

grep -q 'addr: "0.0.0.0:8420"' /opt/fleetly/etc/config.yaml
assert "A2-config-http-addr" $?
grep -q '127.0.0.1:8421' /opt/fleetly/etc/config.yaml
assert "A2-config-grpc-loopback" $?
grep -q '/var/lib/fleetly/fleetly.db' /opt/fleetly/etc/config.yaml
assert "A2-config-db-under-data-root" $?
grep -q '0.0.0.0:8424' /opt/fleetly/etc/config.yaml
assert "A2-config-git-public-bound" $?

# unit 参考副本与 deploy/fleetlyd.service 模板逐字一致（防两份漂移）。
diff /opt/fleetly/etc/fleetlyd.service "$SERVICE_TPL" >/dev/null 2>&1
assert "A2-unit-matches-template" $?
grep -q 'After=.*docker\.service' /opt/fleetly/etc/fleetlyd.service
assert "A2-unit-after-docker" $?
grep -q 'Restart=on-failure' /opt/fleetly/etc/fleetlyd.service
assert "A2-unit-restart" $?
grep -q 'WorkingDirectory=/var/lib/fleetly' /opt/fleetly/etc/fleetlyd.service
assert "A2-unit-workingdir" $?

# 报告：版本/引擎/后端/advertise-addr 判定/端口面/token 指引。
grep -q 'engine gate' "$INSTALL_LOG"
assert "A2-report-engine-gate-line" $?
grep -q 'iptables backend: pass' "$INSTALL_LOG"
assert "A2-report-iptables-pass" $?
grep -q 'advertise-addr' "$INSTALL_LOG"
assert "A2-report-advertise-addr" $?
grep -q 'port exposure' "$INSTALL_LOG"
assert "A2-report-port-exposure-section" $?
grep -q '8424' "$INSTALL_LOG"
assert "A2-report-git-port" $?
grep -q '2377' "$INSTALL_LOG"
assert "A2-report-swarm-ports" $?
grep -q 'bootstrap token' "$INSTALL_LOG"
assert "A2-report-token-hint" $?

# -------------------------------------------------------------- A3 swarm
[ "$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null)" = 'active' ]
assert "A3-swarm-active" $?
ADV=$(docker info --format '{{.Swarm.NodeAddr}}' 2>/dev/null | sed 's/:2377$//')
[ -n "$ADV" ]
assert "A3-advertise-addr-set" $?
case "$ADV" in
10.* | 192.168.* | 172.1[6-9].* | 172.2* | 172.3[01].*) assert "A3-advertise-private" 0 ;;
*) assert "A3-advertise-private" 1 "adv=$ADV" ;;
esac

# ------------------------------------- A4 daemon 手动启动 + 端口面 + liveness
(
    cd /var/lib/fleetly && exec /opt/fleetly/bin/fleetlyd -c /opt/fleetly/etc/config.yaml
) >"$DLOG" 2>&1 &
DPID=$!

_DEADLINE=$(( $(date +%s) + 90 ))
_LIVE=0
while [ "$(date +%s)" -lt "$_DEADLINE" ]; do
    if ! kill -0 "$DPID" 2>/dev/null; then
        break
    fi
    if http_get "http://127.0.0.1:8420/healthz/liveness"; then
        _LIVE=1
        break
    fi
    sleep 2
done
# assert 的参数约定是 rc 语义（0 = 通过）；_LIVE 是布尔语义（1 = 就绪），
# 换算后再断言——直接传会把 PASS 判成 FAIL。
if [ "$_LIVE" -eq 1 ]; then
    assert "A4-liveness-200" 0
else
    assert "A4-liveness-200" 1 "state=$_LIVE (deadline exceeded or process exited)"
fi
if [ "$_LIVE" -eq 0 ]; then
    nl "---- fleetlyd log tail ----"
    tail -n 40 "$DLOG" || true
fi

netstat -tln 2>/dev/null | grep -q ':8424 '
assert "A4-git-ssh-listening" $?
netstat -tln 2>/dev/null | grep -q '127.0.0.1:8421 '
assert "A4-grpc-loopback-listening" $?
# HTTP 面按配置绑 0.0.0.0:8420，Go 通配监听可能落 [::]:8420（双栈）——
# busybox netstat 把 IPv6 通配渲染成 :::8420，三种形态都认。
netstat -tln 2>/dev/null | grep -E '(:::|0\.0\.0\.0:|\[::\]:)8420 ' >/dev/null
assert "A4-http-public-listening" $?

# --------------------------------------------------------------- A5 CLI
# token 行是 JSON：msg 末尾 "...: <plaintext>"}——先取最后一个 ": " 之后，
# 再剥掉 JSON 收尾的引号与花括号。
TOK=$(grep 'bootstrap admin token' "$DLOG" 2>/dev/null | sed -e 's/.*: //' -e 's/".*//' | head -n 1)
[ -n "$TOK" ]
assert "A5-bootstrap-token-in-log" $?
if [ -n "$TOK" ]; then
    FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN="$TOK" /opt/fleetly/bin/fleetly apps list >"/tmp/ti-cli.out" 2>&1
    assert "A5-cli-apps-list" $? "$(cat /tmp/ti-cli.out 2>/dev/null | tail -3)"
else
    fail "A5-cli-apps-list" "no token in log"
fi

# 优雅停服（e2e/smoke 契约：SIGTERM → 退出码 0）。
kill -TERM "$DPID" 2>/dev/null || true
wait "$DPID" 2>/dev/null
_STOP_RC=$?
assert "A4-daemon-sigterm-rc0" "$_STOP_RC" "rc=$_STOP_RC"

# ------------------------------------------------------- A6 重装（幂等）
CFG_SHA_BEFORE=$(sha256sum /opt/fleetly/etc/config.yaml | awk '{print $1}')
sh "$INSTALL_SH" --bin-dir "$STAGE_BIN" --no-systemd >"$REINSTALL_LOG" 2>&1
RC=$?
assert "A6-reinstall-rc0" "$RC" "rc=$RC"
grep -q 'already active' "$REINSTALL_LOG"
assert "A6-swarm-init-skipped" $?
CFG_SHA_AFTER=$(sha256sum /opt/fleetly/etc/config.yaml | awk '{print $1}')
[ "$CFG_SHA_BEFORE" = "$CFG_SHA_AFTER" ]
assert "A6-config-preserved" $?

# --------------------------------------------- A7 门禁负路径（引擎版本）
FAKE1="/tmp/ti-fakebin-ver"
mkdir -p "$FAKE1"
cat >"$FAKE1/docker" <<EOF
#!/bin/sh
case " \$* " in
*"{{.Server.Version}}"*) echo "28.3.2"; exit 0 ;;
esac
exit 0
EOF
chmod +x "$FAKE1/docker"
PATH="$FAKE1:$PATH" sh "$INSTALL_SH" --bin-dir "$STAGE_BIN" --no-systemd >"$NEG_VER_LOG" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "A7-low-engine-rejected" $? "rc=$RC (want non-zero)"
grep -q "28.3.2" "$NEG_VER_LOG"
assert "A7-low-engine-reason-echoed" $?
grep -q "29.8.1" "$NEG_VER_LOG"
assert "A7-low-engine-floor-echoed" $?

# -------------------------------------------- A8 门禁负路径（iptables）
FAKE2="/tmp/ti-fakebin-ipt"
mkdir -p "$FAKE2"
REAL_DOCKER=$(command -v docker)
cat >"$FAKE2/docker" <<EOF
#!/bin/sh
exec $REAL_DOCKER "\$@"
EOF
cat >"$FAKE2/iptables" <<'EOF'
#!/bin/sh
echo "iptables v1.8.13 (nf_tables)"
exit 0
EOF
cat >"$FAKE2/iptables-legacy" <<'EOF'
#!/bin/sh
echo "iptables v1.8.13 (nf_tables)"
exit 0
EOF
chmod +x "$FAKE2/docker" "$FAKE2/iptables" "$FAKE2/iptables-legacy"
PATH="$FAKE2:$PATH" sh "$INSTALL_SH" --bin-dir "$STAGE_BIN" --no-systemd >"$NEG_IPT_LOG" 2>&1
RC=$?
[ "$RC" -ne 0 ]
assert "A8-nftables-rejected" $? "rc=$RC (want non-zero)"
grep -qi 'iptables' "$NEG_IPT_LOG"
assert "A8-nftables-reason-echoed" $?

# ------------------------------------- A10 探测 host 推导（整改⑤）
# install.sh 的健康探测必须打在 daemon 真实绑定的地址上：--http-addr 绑
# 非回环地址（如 10.0.0.5:8420）时 daemon 只绑该地址，127.0.0.1 探测恒
# 拒连——60s 假死后 die，而 systemctl enable --now 已成功（安装报红但
# 平台在跑）。最小可靠形态：把 health_probe_host 定义从 install.sh 原样
# 抽出直接喂样本断言（dind 内不必起第二个 daemon 生命周期即可复核推导）。
PH_SH=/tmp/ti-probe-host.sh
sed -n '/^health_probe_host()/,/^}/p' "$INSTALL_SH" >"$PH_SH"
[ -s "$PH_SH" ]
assert "A10-probe-host-fn-extracted" $?
# shellcheck disable=SC1090
. "$PH_SH"
[ "$(health_probe_host '10.0.0.5:8420')" = '10.0.0.5' ]
assert "A10-specific-host-probed-directly" $?
[ "$(health_probe_host '0.0.0.0:8420')" = '127.0.0.1' ]
assert "A10-wildcard-host-falls-back" $?
[ "$(health_probe_host ':8420')" = '127.0.0.1' ]
assert "A10-empty-host-falls-back" $?
[ "$(health_probe_host '[::1]:8420')" = '[::1]' ]
assert "A10-ipv6-literal-kept" $?
[ "$(health_probe_host '[::]:8420')" = '127.0.0.1' ]
assert "A10-ipv6-wildcard-falls-back" $?

# --------------------------------------------------------------- A9 卸载
sh "$UNINSTALL_SH" >"$UNINSTALL_LOG" 2>&1
RC=$?
assert "A9-uninstall-rc0" "$RC" "rc=$RC"
[ ! -e /opt/fleetly/bin/fleetlyd ]
assert "A9-binary-gone" $?
[ ! -e /opt/fleetly/etc/config.yaml ]
assert "A9-config-gone" $?
[ ! -e /usr/local/bin/fleetly ]
assert "A9-symlink-gone" $?
[ ! -e /etc/systemd/system/fleetlyd.service ]
assert "A9-unit-gone" $?
[ -d /var/lib/fleetly ]
assert "A9-data-kept" $?
grep -q 'KEPT' "$UNINSTALL_LOG"
assert "A9-data-kept-echoed" $?
grep -q 'swarm leave --force' "$UNINSTALL_LOG"
assert "A9-swarm-leave-hint" $?

sh "$UNINSTALL_SH" --purge >>"$UNINSTALL_LOG" 2>&1
RC=$?
assert "A9-purge-rc0" "$RC" "rc=$RC"
[ ! -d /var/lib/fleetly ]
assert "A9-data-purged" $?

finish
