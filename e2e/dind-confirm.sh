#!/bin/sh
# e2e dind confirm（zeabur-oss 借鉴程序第 2 批 B1+B1s 收官 W5，ADR-0089）：
# 变更预览与确认闭环 + ScaleProcess 的真实 swarm 全链——dind 起栈 →
# install 引导 → fleetly init 身份链 → CLI 全链断言。腿（与设计稿
# 2026-10-30 §6 B1/B1s 验收锚一一对应；全部腿用 staging CLI 二进制）：
#
#   1. 预览-确认两调用流：deploy --preview 出预览 + ticket 行（且预览不落
#      部署行）→ --ticket 带票部署 → succeeded；
#   2. 无票直发拒：裸 deploy（非 TTY）本地指引拒 + REST 直发 Deploy 无
#      confirmation → 428 + E_CONFIRMATION_REQUIRED；--yes 逃生门 →
#      succeeded（审计 proof=skipped）；
#   3. STALE 腿：预览后改 App 变量（base 多锚之变量环境锚漂移）→
#      --ticket 确认 → E_CONFIRMATION_STALE（拒绝不落部署行）；
#   4. MISMATCH 腿：--ticket 递伪造票 → E_CONFIRMATION_MISMATCH；
#   5. teardown 预览腿：apps delete --preview 出级联（载体 + route 行）/
#      保留（volume）清单 → TOCTOU（预览后建 route → 旧票确认 STALE，
#      「清单从未展示的删除被拒」）→ 重预览 → --ticket 删除 → 零孤儿；
#   6. LedgerOnly 审计行：confirm.redeem / confirm.skip 事后可回溯；
#   7. ScaleProcess 腿：apps scale 1→2 → succeeded 且 scale-only 零替换
#      （原任务 ID 原样在跑——ADR-0064 内容锚定红利，真 swarm 钉死）；
#   8. webhook 豁免腿（置尾）：git push 面无 confirmation 正常受理出队
#      ——材料归 git（ADR-0072），代码评审即确认，天然豁免。
#
# webhook 腿置尾的既有惯例（dind-smoke 同源）：仓库指向 TEST-NET
# （192.0.2.1），clone 在单写者 drive 步内黑洞挂起、冻结引擎循环——
# 放最后让前面全部腿零干扰，冻结随容器清理一起埋葬。
#
# 前置：本机 docker 可用且持有 nginx:1.27 与 traefik:v3.5（受管面其余
# 镜像按 dind-two-node 先例由 install 自行拉取，缺席不阻塞性能面外的腿）。
set -eu

# Git-Bash（MSYS）会把以 / 开头的容器侧路径参数转译成本机路径（dind-smoke
# 实证坑；CI Linux 无此变量，天然 no-op）。
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

# 1. 交叉编译（linux/amd64；纯 Go 零 cgo）——h2cclient 同批（REST 直发
#    腿的 HTTP 面自备：dind 无 curl、apk 依赖出站网，离线确定性不赌网络）。
log "cross-compiling fleetlyd + fleetly + h2cclient (linux/amd64)"
mkdir -p "$WORKDIR/bins"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetlyd" ./cmd/fleetlyd
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/fleetly" ./cmd/fleetly
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$WORKDIR/bins/h2cclient" ./e2e/h2cclient

# 2. 起 dind（privileged；29 线与生产对齐；--cgroupns=host 让 cadvisor 在
#    嵌套环境能看到内容器——dind-smoke 同款注释）。
log "starting dind container"
DIND_CID=$(docker run -d --privileged --name fleetly-e2e-confirm-"$$" \
  --cgroupns=host \
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
  echo "dind daemon did not become ready" >&2
  return 1
}
log "waiting for dind daemon"
wait_docker

# 镜像离线预载（宿侧 save → dind load）：nginx 部署载荷；traefik 以本地
# v3.5 别名 v3.5.4 预载（dind-h2c-route/dind-template 同款——route 行的
# 受管 reconciler 静默安顿，本脚本断言的是 route 行的面）。
log "preloading nginx + traefik into dind"
docker image save nginx:1.27 | docker exec -i "$DIND_CID" docker load >/dev/null
docker image save traefik:v3.5 | docker exec -i "$DIND_CID" docker load >/dev/null
docker exec "$DIND_CID" docker tag traefik:v3.5 traefik:v3.5.4

# 3. 一行安装（bin-dir 模式，dind-smoke 同款）：容器侧路径用 /root。
log "running install.sh inside dind (FLEETLY_BIN_DIR mode)"
docker cp "$WORKDIR/bins" "$DIND_CID":/root/bins
docker cp install.sh "$DIND_CID":/root/install.sh
docker exec -e FLEETLY_BIN_DIR=/root/bins "$DIND_CID" sh /root/install.sh

# 3b. 身份链（dind-smoke 同款）：bootstrap → fleetly init 铸 CLI token，
#     此后全链以 init 铸的 token 行进。
log "identity chain via fleetly init"
BOOTSTRAP_TOKEN=$(docker exec "$DIND_CID" sh -c 'tr -d "\r\n" < /var/lib/fleetly/bootstrap-token')
if [ -z "$BOOTSTRAP_TOKEN" ]; then
  echo "bootstrap token file missing or empty" >&2
  exit 1
fi
CURRENT_TOKEN=$(docker exec -e FLEETLY_ADDR=127.0.0.1:9080 "$DIND_CID" \
  fleetly init --json --token "$BOOTSTRAP_TOKEN" alice \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$CURRENT_TOKEN" ]; then
  echo "fleetly init did not mint a CLI token" >&2
  exit 1
fi

cli() {
  docker exec -e FLEETLY_ADDR=127.0.0.1:9080 -e FLEETLY_TOKEN="$CURRENT_TOKEN" "$DIND_CID" fleetly "$@"
}

cli whoami >/dev/null
log "identity chain green"

# 4. 世界：单 project + 四 App（每腿独立 App：免遭彼此在途部署的 admission
#    合并干扰——confirm golden 同款纪律）。
log "creating project + apps"
cli projects create confirm >/dev/null
PROJECT_ID=$(cli --json projects list | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1)
for APP_NAME in confirm-a confirm-yes confirm-tear confirm-hook; do
  cli apps create --project "$PROJECT_ID" "$APP_NAME" >/dev/null
done
# protojson 字段序：id 在 name 前（App 字段号序）——先记 id，见 name 命中
# 即回带（dind-smoke 场景 2b 的 scaler 解析同款）。
app_id_of() {
  cli --json apps list --project "$PROJECT_ID" | awk -F'"' -v n="$1" '/"id":/ {id=$4} /"name":/ && $4 == n {print id; exit}'
}
CA=$(app_id_of confirm-a)
CY=$(app_id_of confirm-yes)
CT=$(app_id_of confirm-tear)
CH=$(app_id_of confirm-hook)
for ID in "$CA" "$CY" "$CT" "$CH"; do
  [ -n "$ID" ] || { echo "app id resolution failed" >&2; exit 1; }
done
log "project=$PROJECT_ID confirm-a=$CA confirm-yes=$CY confirm-tear=$CT confirm-hook=$CH"

deployment_count() {
  cli --json deployments list --app "$1" | /usr/bin/grep -c '"state":' || true
}

# latest_deployment_id 取某 App 最新部署行 id（腿 6 审计锚——confirm.redeem/
# confirm.skip 行必须锚到本腿自己的部署行，历史行不再可满足 grep）。
latest_deployment_id() {
  cli --json deployments list --app "$1" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' | head -1
}

latest_state() {
  cli --json deployments list --app "$1" | sed -n 's/.*"state": *"\([^"]*\)".*/\1/p' | head -1
}

wait_state() {
  app="$1"; want="$2"; i=0; state=""
  while [ "$i" -lt 240 ]; do
    state=$(latest_state "$app")
    if [ "$state" = "$want" ]; then
      return 0
    fi
    case "$state" in
      failed|superseded|cancelled)
        cli --json deployments list --app "$app" >&2 || true
        echo "deployment reached $state before $want" >&2
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 1
  done
  echo "timed out waiting for $want (last=$state)" >&2
  return 1
}

# ---- 腿 1：预览-确认两调用流（deploy --preview → --ticket → succeeded）----
log "leg 1: preview -> ticket deploy -> succeeded"
cli deploy --app "$CA" --image nginx:1.27 --preview > "$WORKDIR/preview-a.out"
/usr/bin/grep -q "^deploy preview for app $CA\$" "$WORKDIR/preview-a.out" || {
  echo "preview output header missing:" >&2; cat "$WORKDIR/preview-a.out" >&2; exit 1; }
/usr/bin/grep -q "^workload changes" "$WORKDIR/preview-a.out" || {
  echo "preview output has no workload changes section:" >&2; cat "$WORKDIR/preview-a.out" >&2; exit 1; }
# 空基线形态：全量 added（replicas 0->1），不照抄 ErrNotFound。
/usr/bin/grep -q 'replicas 0->1' "$WORKDIR/preview-a.out" || {
  echo "first-deploy preview must be the all-added shape (replicas 0->1):" >&2
  cat "$WORKDIR/preview-a.out" >&2; exit 1; }
TICKET_A=$(sed -n 's/^ticket: \([^ ][^ ]*\) .*/\1/p' "$WORKDIR/preview-a.out")
[ -n "$TICKET_A" ] || { echo "preview did not print a ticket line" >&2; exit 1; }
/usr/bin/grep -q "expires in 600s" "$WORKDIR/preview-a.out" || {
  echo "preview ticket line does not carry the 600s TTL" >&2; exit 1; }
# 预览是只读孪生：退出即零部署行（受理半边没跑）。
if [ "$(deployment_count "$CA")" != "0" ]; then
  echo "preview must not admit a deployment row" >&2
  exit 1
fi
log "preview printed diff + ticket $TICKET_A (no deployment row admitted)"

cli deploy --app "$CA" --image nginx:1.27 --ticket "$TICKET_A" >/dev/null
wait_state "$CA" succeeded
DEP_ID_REDEEM=$(latest_deployment_id "$CA")
[ -n "$DEP_ID_REDEEM" ] || { echo "leg 1 deployment id resolution failed" >&2; exit 1; }
log "ticketed deploy reached succeeded (deployment $DEP_ID_REDEEM)"
# 票是单用途：同票二发必须 MISMATCH（tombstone 可辨因——「已确认过」不与
# 伪造不可分辨，设计稿 §1.4 #4）。
if cli deploy --app "$CA" --image nginx:1.27 --ticket "$TICKET_A" >/dev/null 2>"$WORKDIR/redeem.err"; then
  echo "a redeemed ticket must be refused on resend" >&2
  exit 1
fi
/usr/bin/grep -q '^E_CONFIRMATION_MISMATCH:' "$WORKDIR/redeem.err" || {
  echo "redeemed-ticket resend must present E_CONFIRMATION_MISMATCH, got:" >&2
  cat "$WORKDIR/redeem.err" >&2; exit 1; }
log "redeemed ticket refused on resend (single-use, distinguishable)"

# ---- 腿 2：无票直发拒（裸 deploy + REST 直发 428）+ --yes 逃生门 ----
log "leg 2: no-ticket refusal (428) + --yes escape hatch"
# 裸 deploy（非 TTY）：CLI 本地指引拒（不发请求——白付一次拨号无意义）。
if cli deploy --app "$CY" --image nginx:1.27 >/dev/null 2>"$WORKDIR/nontty.err"; then
  echo "bare deploy in a non-TTY must be refused locally" >&2
  exit 1
fi
/usr/bin/grep -q "requires confirmation and stdin is not a terminal" "$WORKDIR/nontty.err" || {
  echo "bare deploy refusal must carry the two-step guidance, got:" >&2
  cat "$WORKDIR/nontty.err" >&2; exit 1; }
log "bare deploy refused locally with the preview/ticket guidance"

# 服务端半边（REQUIRED 的真源）：REST 直发 Deploy 不带 confirmation →
# 428 Precondition Required + 错误码可辨（errcode 显式映射，RFC 9110 428
# 语义：先预览再确认的前置动作未完成）。
printf '{"app_id":"%s","image":"nginx:1.27","idempotency_key":"e2e-confirm-required-1"}' "$CY" \
  | docker exec -i "$DIND_CID" sh -c 'cat > /tmp/confirm-body'
REST_CODE=$(docker exec -e FLEETLY_BEARER="$CURRENT_TOKEN" "$DIND_CID" sh -c \
  '/root/bins/h2cclient -X POST -o /tmp/confirm-resp \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $FLEETLY_BEARER" \
     -data @/tmp/confirm-body http://127.0.0.1:9081/v1/deployments' \
  | sed -n 's/^STATUS \([0-9]*\).*/\1/p')
REST_BODY=$(docker exec "$DIND_CID" cat /tmp/confirm-resp)
if [ "$REST_CODE" != "428" ]; then
  echo "REST Deploy without confirmation must answer 428, got status ${REST_CODE:-none}: $REST_BODY" >&2
  exit 1
fi
case "$REST_BODY" in
  *E_CONFIRMATION_REQUIRED*) ;;
  *) echo "428 body must carry E_CONFIRMATION_REQUIRED, got: $REST_BODY" >&2; exit 1 ;;
esac
if [ "$(deployment_count "$CY")" != "0" ]; then
  echo "the refused Deploy must not admit a deployment row" >&2
  exit 1
fi
log "REST Deploy without confirmation refused: 428 + E_CONFIRMATION_REQUIRED (no row)"

# --yes 逃生门（脚本/CI 通道；skip 不验票不烧票，审计 proof=skipped）。
cli deploy --app "$CY" --image nginx:1.27 --yes > "$WORKDIR/yes.out"
/usr/bin/grep -q "skipped preview confirmation (--yes)" "$WORKDIR/yes.out" || {
  echo "--yes output must carry the skip note, got:" >&2; cat "$WORKDIR/yes.out" >&2; exit 1; }
wait_state "$CY" succeeded
DEP_ID_SKIP=$(latest_deployment_id "$CY")
[ -n "$DEP_ID_SKIP" ] || { echo "leg 2 deployment id resolution failed" >&2; exit 1; }
log "--yes escape hatch deployed to succeeded (deployment $DEP_ID_SKIP)"

# ---- 腿 3：STALE（预览后改 App 变量 → 变量环境锚漂移）----
log "leg 3: variable drift -> E_CONFIRMATION_STALE"
cli deploy --app "$CY" --image nginx:1.27 --preview > "$WORKDIR/preview-stale.out"
TICKET_STALE=$(sed -n 's/^ticket: \([^ ][^ ]*\) .*/\1/p' "$WORKDIR/preview-stale.out")
[ -n "$TICKET_STALE" ] || { echo "stale-leg preview did not mint a ticket" >&2; exit 1; }
ROWS_BEFORE=$(deployment_count "$CY")
# 旗标须在位置参数前（Go flag 在首个位置参数处停止解析——库 ParseFlags
# 同款）。
cli apps vars set --app "$CY" --value on drift_probe >/dev/null
if cli deploy --app "$CY" --image nginx:1.27 --ticket "$TICKET_STALE" >/dev/null 2>"$WORKDIR/stale.err"; then
  echo "a ticket minted before a variable change must be refused" >&2
  exit 1
fi
/usr/bin/grep -q '^E_CONFIRMATION_STALE:' "$WORKDIR/stale.err" || {
  echo "variable drift must present E_CONFIRMATION_STALE, got:" >&2
  cat "$WORKDIR/stale.err" >&2; exit 1; }
if [ "$(deployment_count "$CY")" != "$ROWS_BEFORE" ]; then
  echo "the stale refusal must not admit a deployment row" >&2
  exit 1
fi
log "variable drift after preview refused: E_CONFIRMATION_STALE (re-preview advised)"

# ---- 腿 4：MISMATCH（伪造票）----
log "leg 4: forged ticket -> E_CONFIRMATION_MISMATCH"
if cli deploy --app "$CY" --image nginx:1.27 --ticket "forged-confirm-ticket" >/dev/null 2>"$WORKDIR/mismatch.err"; then
  echo "a forged ticket must be refused" >&2
  exit 1
fi
/usr/bin/grep -q '^E_CONFIRMATION_MISMATCH:' "$WORKDIR/mismatch.err" || {
  echo "forged ticket must present E_CONFIRMATION_MISMATCH, got:" >&2
  cat "$WORKDIR/mismatch.err" >&2; exit 1; }
log "forged ticket refused: E_CONFIRMATION_MISMATCH"

# ---- 腿 5：teardown 预览 → TOCTOU STALE → 重预览删除 → 零孤儿 ----
log "leg 5: teardown preview -> TOCTOU stale -> confirmed delete -> zero orphans"
# 基线带卷（保留清单的引用面）+ route 行（级联与 TOCTOU 锚）。
cli volumes create --project "$PROJECT_ID" tear-scratch >/dev/null
docker exec -i "$DIND_CID" sh -c "cat > /root/tear-spec.json" <<JSON
{"source":{"image":{"ref":"nginx:1.27"}},
 "processes":[{"name":"web","image":"nginx:1.27","volumes":[{"volume_id":"tear-scratch","target":"/data"}]}]}
JSON
cli deploy --app "$CT" --spec-file /root/tear-spec.json --yes >/dev/null
wait_state "$CT" succeeded
cli routes create --project "$PROJECT_ID" --host tear-a.confirm.test --app "$CT" --process web --port 80 >/dev/null

cli apps delete --app "$CT" --preview > "$WORKDIR/tear-preview.out"
/usr/bin/grep -q "^teardown preview for app $CT\$" "$WORKDIR/tear-preview.out" || {
  echo "teardown preview header missing:" >&2; cat "$WORKDIR/tear-preview.out" >&2; exit 1; }
/usr/bin/grep -q '^  workload .*\-web$' "$WORKDIR/tear-preview.out" || {
  echo "teardown cascade must list the web carrier:" >&2; cat "$WORKDIR/tear-preview.out" >&2; exit 1; }
/usr/bin/grep -q '^  route_row tear-a.confirm.test$' "$WORKDIR/tear-preview.out" || {
  echo "teardown cascade must list the route row:" >&2; cat "$WORKDIR/tear-preview.out" >&2; exit 1; }
/usr/bin/grep -q '^  volume: volumes are project-scoped and survive app deletion (mounted: tear-scratch)$' "$WORKDIR/tear-preview.out" || {
  echo "teardown kept list must carry the volume with its survival reason:" >&2
  cat "$WORKDIR/tear-preview.out" >&2; exit 1; }
TICKET_TEAR=$(sed -n 's/^ticket: \([^ ][^ ]*\) .*/\1/p' "$WORKDIR/tear-preview.out")
[ -n "$TICKET_TEAR" ] || { echo "teardown preview did not mint a ticket" >&2; exit 1; }
log "teardown preview listed cascade (carrier + route) and kept (volume) with ticket"

# TOCTOU：预览后新增 route——票面 route 集锚漂移，清单从未展示的删除被拒
# （设计稿 §1.4 #8）。
cli routes create --project "$PROJECT_ID" --host tear-b.confirm.test --app "$CT" --process web --port 80 >/dev/null
if cli apps delete --app "$CT" --ticket "$TICKET_TEAR" >/dev/null 2>"$WORKDIR/tear-stale.err"; then
  echo "a teardown ticket minted before a new route row must be refused" >&2
  exit 1
fi
/usr/bin/grep -q '^E_CONFIRMATION_STALE:' "$WORKDIR/tear-stale.err" || {
  echo "route-set drift must present E_CONFIRMATION_STALE, got:" >&2
  cat "$WORKDIR/tear-stale.err" >&2; exit 1; }
cli --json apps list --project "$PROJECT_ID" | /usr/bin/grep -q "\"$CT\"" || {
  echo "the refused teardown must leave the app row in place" >&2
  exit 1; }
log "TOCTOU: route added after preview refused the old ticket (app intact)"

# 重预览（增量世界）→ 带票删除成功。
cli apps delete --app "$CT" --preview > "$WORKDIR/tear-preview2.out"
/usr/bin/grep -q '^  route_row tear-b.confirm.test$' "$WORKDIR/tear-preview2.out" || {
  echo "the fresh preview must now list the late route in the cascade:" >&2
  cat "$WORKDIR/tear-preview2.out" >&2; exit 1; }
TICKET_TEAR2=$(sed -n 's/^ticket: \([^ ][^ ]*\) .*/\1/p' "$WORKDIR/tear-preview2.out")
[ -n "$TICKET_TEAR2" ] || { echo "re-preview did not mint a ticket" >&2; exit 1; }
cli apps delete --app "$CT" --ticket "$TICKET_TEAR2" | /usr/bin/grep -q "deleted app $CT" || {
  echo "ticketed teardown did not report the deletion" >&2
  exit 1
}
# 零孤儿：载体清空（受管循环移除有窗，有界轮询）、route 行双清、app 行消失。
CT_LC=$(printf '%s' "$CT" | tr 'A-Z' 'a-z')
i=0
while [ "$i" -lt 30 ]; do
  N_SVC=$(docker exec "$DIND_CID" docker service ls --filter "label=fleetly.ns.app=$CT_LC" -q | /usr/bin/grep -c . || true)
  [ "$N_SVC" = "0" ] && break
  i=$((i + 1))
  sleep 1
done
if [ "$N_SVC" != "0" ]; then
  echo "orphan carriers survive the confirmed teardown:" >&2
  docker exec "$DIND_CID" docker service ls --filter "label=fleetly.ns.app=$CT_LC" >&2 || true
  exit 1
fi
if cli --json routes list --project "$PROJECT_ID" | /usr/bin/grep -q 'tear-[ab].confirm.test'; then
  echo "route rows must be purged with the confirmed teardown" >&2
  cli --json routes list --project "$PROJECT_ID" >&2 || true
  exit 1
fi
if cli --json apps list --project "$PROJECT_ID" | /usr/bin/grep -q "\"$CT\""; then
  echo "app row must be gone after the confirmed teardown" >&2
  exit 1
fi
log "confirmed teardown left zero orphans (carriers + routes + app row)"

# ---- 腿 6：LedgerOnly 审计行（事后可证；DEP_ID 锚定本腿部署行——修复批
# F4 强化：历史行不再可满足 grep，redeem/skip 行必须锚到腿 1/腿 2 的部署）----
log "leg 6: confirm.redeem / confirm.skip audit rows (anchored to this run's deployments)"
cli audit --action confirm. --limit 50 | /usr/bin/grep -q "confirm.redeem.*deployment/$DEP_ID_REDEEM" || {
  echo "confirm.redeem audit row for deployment $DEP_ID_REDEEM missing" >&2
  cli audit --action confirm. --limit 50 >&2 || true
  exit 1
}
cli audit --action confirm. --limit 50 | /usr/bin/grep -q "confirm.skip.*deployment/$DEP_ID_SKIP" || {
  echo "confirm.skip audit row for deployment $DEP_ID_SKIP missing" >&2
  exit 1
}
log "LedgerOnly audit rows present (confirm.redeem + confirm.skip, anchored to $DEP_ID_REDEEM / $DEP_ID_SKIP)"

# ---- 腿 7：ScaleProcess（apps scale 1→2，scale-only 零替换）----
log "leg 7: apps scale 1->2 with zero task replacement"
CA_LC=$(printf '%s' "$CA" | tr 'A-Z' 'a-z')
svc_of() {
  docker exec "$DIND_CID" docker service ls --format '{{.Name}}' \
    --filter "label=fleetly.ns.app=$CA_LC" --filter "label=fleetly.process=$1" | head -1
}
WEB_SVC=$(svc_of web)
[ -n "$WEB_SVC" ] || { echo "leg 7: web service not found for app $CA" >&2; exit 1; }
# 基线任务 ID（scale 前唯一在跑任务）——零替换断言的对照锚。
ORIG_TASK=$(docker exec "$DIND_CID" docker service ps "$WEB_SVC" --format '{{.ID}} {{.CurrentState}}' \
  | awk '$2 == "Running" {print $1; exit}')
[ -n "$ORIG_TASK" ] || {
  echo "leg 7: no running task before scaling:" >&2
  docker exec "$DIND_CID" docker service ps "$WEB_SVC" --format '{{.ID}} {{.CurrentState}}' >&2
  exit 1
}
cli apps scale "$CA" web 2 | /usr/bin/grep -q "scaled to 2 replicas" || {
  echo "apps scale did not report the scaling admission" >&2
  exit 1
}
wait_state "$CA" succeeded
# 零替换三断言：原任务 ID 原样 Running、恰 2 Running、0 Shutdown（旧口径
# 全员换代下必红——ADR-0064 内容锚定等价，真 swarm 钉死）。
sleep 3 # swarm 副本收敛的一拍余量
TASK_ROWS=$(docker exec "$DIND_CID" docker service ps "$WEB_SVC" --format '{{.ID}} {{.CurrentState}}')
N_RUNNING=$(printf '%s\n' "$TASK_ROWS" | awk '$2 == "Running"' | /usr/bin/grep -c . || true)
N_SHUTDOWN=$(printf '%s\n' "$TASK_ROWS" | awk '$2 == "Shutdown"' | /usr/bin/grep -c . || true)
if [ "$N_RUNNING" != "2" ] || [ "$N_SHUTDOWN" != "0" ]; then
  echo "leg 7: expected 2 Running + 0 Shutdown task rows, got ($N_RUNNING/$N_SHUTDOWN):" >&2
  printf '%s\n' "$TASK_ROWS" >&2
  exit 1
fi
printf '%s\n' "$TASK_ROWS" | awk -v t="$ORIG_TASK" '$1 == t && $2 == "Running"' | /usr/bin/grep -q . || {
  echo "leg 7: original task $ORIG_TASK must keep running (scale-only must not replace it):" >&2
  printf '%s\n' "$TASK_ROWS" >&2
  exit 1
}
log "scale-only: original task $ORIG_TASK still running, 2 Running + 0 Shutdown (zero replacement)"

# ---- 腿 8（置尾）：webhook 豁免——push 无 confirmation 正常受理出队 ----
log "leg 8: webhook face exempt (push admitted without confirmation)"
HOOK_SECRET=$(cli --json hooks set --app "$CH" --repo https://192.0.2.1/acme/confirm.git --branch main --watch web/ \
  | sed -n 's/.*"secret": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$HOOK_SECRET" ]; then
  echo "hooks set did not mint a secret" >&2
  exit 1
fi

SIGN() {
  printf '%s' "$1" | openssl dgst -sha256 -hmac "$HOOK_SECRET" | sed -n 's/.*= *\([0-9a-f]*\).*/\1/p'
}
payload() { printf '%s' "$1" | docker exec -i "$DIND_CID" sh -c 'cat > /tmp/hook-payload'; }
post_hook() {
  code=$(docker exec "$DIND_CID" /root/bins/h2cclient -X POST -o /tmp/hook-body \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: $1" -H "X-GitHub-Delivery: $2" -H "X-Hub-Signature-256: $3" \
    -data "@/tmp/hook-payload" http://127.0.0.1:9081/v1/hooks/$HOOK_SECRET \
    | sed -n 's/^STATUS \([0-9]*\).*/\1/p')
  body=$(docker exec "$DIND_CID" cat /tmp/hook-body)
  printf '%s %s' "$code" "$body"
}

COMMIT="1234567890123456789012345678901234567890"
PUSH="{\"ref\":\"refs/heads/main\",\"after\":\"$COMMIT\",\"head_commit\":{\"id\":\"$COMMIT\",\"message\":\"confirm e2e push\"},\"commits\":[{\"modified\":[\"web/index.ts\"]}]}"
payload "$PUSH"
SIG=$(SIGN "$PUSH")
resp=$(post_hook push d-confirm-1 "sha256=$SIG")
case "$resp" in
  200\ *accepted*) ;;
  *) echo "webhook push must be admitted without confirmation, got: $resp" >&2; exit 1 ;;
esac
WEBHOOK_DEP=$(printf '%s' "$resp" | sed -n 's/.*"deployment_id": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$WEBHOOK_DEP" ] || { echo "webhook push response carries no deployment_id" >&2; exit 1; }
# 引擎已接手（TEST-NET clone 挂起/快败环境下 building/failed 都是「受理成功
# 且无票」的证据；卡 queued 才是失败——豁免腿断言的是受理出队）。
i=0
while [ "$i" -lt 60 ]; do
  state=$(latest_state "$CH")
  case "$state" in
    building|releasing|observing|failed) break ;;
  esac
  i=$((i + 1))
  sleep 1
done
case "$state" in
  building|releasing|observing|failed) log "webhook deployment $WEBHOOK_DEP admitted and picked up (state=$state) — no confirmation required" ;;
  *) echo "webhook deployment stuck in $state (exemption must admit, not stall)" >&2; exit 1 ;;
esac

log "CONFIRM PASSED"
