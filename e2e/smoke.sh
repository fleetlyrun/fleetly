#!/bin/sh
# fleetlyd 冒烟 E2E（T0.4 / 交付流水线 M0 骨架）。
#
# 用途：在 POSIX sh 环境（CI 的 docker:29.8.1-dind 容器内，或任何 Linux
# 宿主）拉起 fleetlyd 二进制，验证最小生命周期：
#   1) 后台启动（--addr 参数化）
#   2) /healthz/liveness 在 deadline 内返回 200
#   3) /v1/system/ping 应答 JSON 的 service == "fleetlyd"
#   4) SIGTERM 优雅停止，进程退出码为 0
# 任一步失败即输出 FAIL 并以非零码退出；全部通过输出 PASS 汇总、退出 0。
#
# 用法（CI 与本地手动同一入口，见 e2e/README.md）：
#   FLEETLYD_BIN=/tmp/fleetlyd sh smoke.sh
#
# 参数（环境变量，均可覆盖）：
#   FLEETLYD_BIN  fleetlyd 二进制路径（必填，无默认可执行值）
#   HTTP_ADDR       HTTP 面监听地址，默认 127.0.0.1:8420（与平台默认一致）
#   SMOKE_TIMEOUT_S liveness 就绪 deadline（秒），默认 15
#   SMOKE_LOG       二进制 stdout/stderr 落盘路径，默认 ${TMPDIR:-/tmp}/fleetlyd-smoke.log
#
# 工具依赖：POSIX sh + busybox/ GNU 工具。HTTP 客户端取 curl 或 wget 之一
# （docker:29.8.1-dind 基于 Alpine，自带 busybox wget，无需 apk add）；
# JSON 断言用 sed，不依赖 jq。
#
# 设计依据：docs/design/2026-09-17-delivery-pipeline.md §2.2 PR 轨道第 5 项、
# P2（E2E 宿主用 dind）、§4 M0；docs/plan/2026-09-17-task-breakdown.md T0.4。

set -u

FLEETLYD_BIN="${FLEETLYD_BIN:-}"
HTTP_ADDR="${HTTP_ADDR:-127.0.0.1:8420}"
SMOKE_TIMEOUT_S="${SMOKE_TIMEOUT_S:-15}"
SMOKE_LOG="${SMOKE_LOG:-${TMPDIR:-/tmp}/fleetlyd-smoke.log}"

SMOKE_BODY="${TMPDIR:-/tmp}/fleetlyd-smoke-ping.json"
DEADLINE=$(( $(date +%s) + SMOKE_TIMEOUT_S ))
PASS_COUNT=0
FAIL_COUNT=0
APP_PID=

# 代理变量会影响对 127.0.0.1 的请求（CI 镜像内偶见注入），探活前一律摘除。
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy 2>/dev/null || true
NO_PROXY='*'
no_proxy='*'
export NO_PROXY no_proxy

cleanup() {
    # 兜底：脚本中途失败时不留孤儿 fleetlyd 进程。
    if [ -n "$APP_PID" ] && kill -0 "$APP_PID" 2>/dev/null; then
        kill -TERM "$APP_PID" 2>/dev/null || true
        wait "$APP_PID" 2>/dev/null || true
    fi
    rm -f "$SMOKE_BODY"
}
trap cleanup EXIT INT TERM

ok()   { PASS_COUNT=$(( PASS_COUNT + 1 )); echo "[smoke] PASS: $1"; }
fail() { FAIL_COUNT=$(( FAIL_COUNT + 1 )); echo "[smoke] FAIL: $1"; }

dump_log_tail() {
    echo "[smoke] ---- fleetlyd log tail ($SMOKE_LOG) ----"
    tail -n 20 "$SMOKE_LOG" 2>/dev/null || echo "[smoke] (no log file)"
    echo "[smoke] ---- end log tail ----"
}

# http_fetch PATH OUTFILE：GET http://HTTP_ADDR/PATH，2xx 时返回 0 并写 OUTFILE。
# curl 用 -f（>=400 即失败）；busybox wget 对 4xx/5xx 本就返回非零。
# 语义等价于「HTTP 200 可用」。
http_fetch() {
    _path="$1"
    _out="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsS --max-time 3 "http://${HTTP_ADDR}${_path}" > "$_out" 2>/dev/null
    elif command -v wget >/dev/null 2>&1; then
        wget -q -T 3 -O "$_out" "http://${HTTP_ADDR}${_path}"
    else
        echo "[smoke] FAIL: no http client (curl/wget) available" >&2
        return 127
    fi
}

echo "[smoke] fleetlyd smoke E2E: bin=$FLEETLYD_BIN addr=$HTTP_ADDR deadline=${SMOKE_TIMEOUT_S}s"

# ---- 前置：二进制存在且可执行 ------------------------------------------------
if [ -n "$FLEETLYD_BIN" ] && [ -x "$FLEETLYD_BIN" ]; then
    ok "preflight: binary exists and is executable ($FLEETLYD_BIN)"
else
    fail "preflight: FLEETLYD_BIN not set or not executable (got '$FLEETLYD_BIN')"
    echo "[smoke] summary: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# ---- 1) 后台启动 ------------------------------------------------------------
"$FLEETLYD_BIN" --addr "$HTTP_ADDR" >"$SMOKE_LOG" 2>&1 &
APP_PID=$!
ok "start: fleetlyd launched in background (pid $APP_PID, log $SMOKE_LOG)"

# ---- 2) liveness 就绪（带 deadline）------------------------------------------
live_status="not-ready"
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
    if ! kill -0 "$APP_PID" 2>/dev/null; then
        live_status="process-exited"
        break
    fi
    if http_fetch /healthz/liveness /dev/null; then
        live_status="ready"
        break
    fi
    sleep 1
done

if [ "$live_status" = "ready" ]; then
    ok "liveness: GET /healthz/liveness returned 200 within ${SMOKE_TIMEOUT_S}s"
else
    fail "liveness: /healthz/liveness not ready within ${SMOKE_TIMEOUT_S}s (state: $live_status)"
    wait "$APP_PID" 2>/dev/null
    _rc=$?
    echo "[smoke] (process exit code was $_rc, state: $live_status)"
    dump_log_tail
    echo "[smoke] summary: $PASS_COUNT passed, $FAIL_COUNT failed"
    exit 1
fi

# ---- 3) ping 断言 service == fleetlyd --------------------------------------
# 注意：gateway 用 protojson 输出，字段间空白不保证（逗号/冒号后可能有空格），
# sed 需容忍可选空白；不依赖 jq。
if http_fetch /v1/system/ping "$SMOKE_BODY" && [ -s "$SMOKE_BODY" ]; then
    service_name=$(sed -n 's/.*"service"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$SMOKE_BODY")
    if [ "$service_name" = "fleetlyd" ]; then
        ok "ping: GET /v1/system/ping service == \"fleetlyd\" (body: $(cat "$SMOKE_BODY"))"
    else
        fail "ping: service field mismatch, want \"fleetlyd\", got \"${service_name}\" (body: $(cat "$SMOKE_BODY"))"
    fi
else
    fail "ping: GET /v1/system/ping failed or empty body"
    dump_log_tail
fi

# ---- 4) SIGTERM 优雅停止，退出码 0 -------------------------------------------
# fleetlyd 的信号监听与退出码由 lynx Runner 托管：SIGTERM → 优雅关停 → 0。
stop_rc="unknown"
kill -TERM "$APP_PID" 2>/dev/null || true
wait "$APP_PID" 2>/dev/null
stop_rc=$?
APP_PID=
if [ "$stop_rc" -eq 0 ]; then
    ok "stop: SIGTERM graceful shutdown, exit code 0"
else
    fail "stop: expected exit code 0 after SIGTERM, got $stop_rc"
    dump_log_tail
fi

# ---- 汇总 --------------------------------------------------------------------
echo "[smoke] summary: $PASS_COUNT passed, $FAIL_COUNT failed"
if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
fi
exit 0
