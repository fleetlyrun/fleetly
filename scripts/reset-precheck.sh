#!/bin/sh
# 全量重置序「运行侧同名卷清理步」的受检工具（机制批 B3，docs/proposals/
# 2026-11-04-volume-carrier-keying-and-job-order.md 第 4 条）。
#
# 背景（staging runbook 记·六十一附节坑②/坑③，记·四十八同形先例）：重置
# 只拆载体不清运行侧卷时，同名重建实体会挂上重置前数据面——库 = 新凭证
# 对旧 pgdata 隐蔽认证失败且不可自愈；app 声明卷 = 跨项目同名共键双写损坏
# 面。机制位已由 ADR-0097 承接（卷载体键换行 ULID + 库卷异源 fail-closed
# 门），本工具是重置窗口的防御性检查+清理：把该步从人肉 shell 升格为
# 受检清单（只经 docker 面，不动 Go 服务面）。
#
# 清点与分类（对照重置窗口期望集）：
#   managed   受管域常量卷（traefik/zot/victorialogs/victoriametrics 四枚，
#             来源锚 providers 各 provider.go 的 VolumeID 常量；新增受管卷
#             随同批补 managed_volume 表——双向保鲜）——重置后合法存续，
#             永不动；
#   hazard    用户域旧名键卷（fleetly-vol-<卷名>，后缀非 26 位行 id 形）——
#             同名重建碰撞面（坑②/坑③形态），重播种前必清；
#   stale     用户域行 id 键卷（fleetly-vol-<26 位 Crockford base32 小写>，
#             ADR-0097 后新铸形态）——旧世代盘面残留（行 id never-reuse，
#             无碰撞面；重播种已开始时可能是新库合法卷，先复核再清）；
#   carrier   fleetly-db-* 残留库载体——拆除步漏做（架构 §8 孤儿律：对不上
#             账的载体只登记、永不自动删；报告附手工序，不由本工具删）。
#   in use    卷仍被存活载体挂载（daemon 侧 dangling=false）——先完成对应
#             载体拆除步，卷转 dangling 后再清。
#
# 使用窗（顺序敏感）：重置序「起新版（全新库 goose 前滚）」之后、「重播种
# 清单」之前——此时新库零用户域行，全部用户域 fleetly-vol-* 都是旧世代残
# 留。窗口外 ULID 形卷无法从 docker 面归因新旧行（CLI 无 volumes list 动
# 词）——勿在窗口外执行 --prune。
#
# 用法（在控制面 manager 上执行）：
#   sh scripts/reset-precheck.sh             # 只读清点+分类清单（清单先行）
#   sh scripts/reset-precheck.sh --prune     # 清理 dangling 的 hazard/stale
#                                            # 用户域卷；managed 与 in-use
#                                            # 永不动；carrier 只报告
# 退出码：0 = 无需动作（或 --prune 后已清干净）；3 = 有发现待处置；1 = 用
# 法/环境错误。DOCKER 环境变量可覆写 docker 二进制路径（自测桩通道）。
#
# 边界（诚实声明，ADR-0097 决策 5 同款）：docker 卷 node-local，本脚本只见
# 控制面 manager 本机 daemon——worker 节点的卷残留不在本清点内（多节点
# swarm 库卷钉离控制面是 v1 外形态）；k3s 面走各 runtime runbook 的通道。
set -eu
LC_ALL=C
export LC_ALL

DOCKER="${DOCKER:-docker}"
PRUNE=0

usage() {
  cat <<'EOF'
usage: sh scripts/reset-precheck.sh [--prune]

Checked listing for the full-reset sequence step "runtime-side same-name
volume cleanup" (batch B3; the mechanism itself is carried by ADR-0097).
Run on the control-plane manager, after the fresh database is up and
before re-seeding starts.

  (no flags)  read-only classified listing — volumes (fleetly-vol-*) and
              leftover database carriers (fleetly-db-*)
  --prune     additionally remove dangling user-domain volumes; managed
              constants and in-use volumes are never touched; leftover
              database carriers are registered only (never auto-removed)

exit codes: 0 nothing to do; 3 findings need action; 1 usage/environment error
environment: DOCKER overrides the docker binary (self-test stub channel)
EOF
}

for arg in "$@"; do
  case "$arg" in
    --prune) PRUNE=1 ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $arg (supported: --prune)" >&2
      exit 1
      ;;
  esac
done

if ! "$DOCKER" version >/dev/null 2>&1; then
  echo "docker face unreachable via: $DOCKER (run on the control-plane manager)" >&2
  exit 1
fi

# managed_volume：受管域期望集（来源锚 internal/infrastructure/providers/
# 各 provider.go 的 VolumeID 常量：traefik acmeVolumeID、zot/victorialogs/
# victoriametrics volumeID）。受管卷 VolumeID 是跨代常量（记·六十一：换名
# 不换卷），重置后合法存续。
managed_volume() {
  case "$1" in
    fleetly-vol-fleetly-proxy-acme | \
    fleetly-vol-fleetly-registry-zot | \
    fleetly-vol-fleetly-logging-victorialogs | \
    fleetly-vol-fleetly-metrics-victoriametrics) return 0 ;;
  esac
  return 1
}

# rowid_keyed：卷键是否行 id 形（fleetly-vol-<26 位 Crockford base32 小写
# ULID>；正则与 e2e/dind-upgrade.sh 的 ADR-0097 断言同款）。
rowid_keyed() {
  printf '%s\n' "$1" | grep -qE '^fleetly-vol-[0-9a-hjkmnp-tv-z]{26}$'
}

# dangling：卷在 daemon 侧无容器引用（列表级过滤与 swarm 孤儿卷清扫同款）。
dangling() {
  printf '%s\n' "$DANGLING_VOLS" | grep -qxF "$1"
}

# gather：清点三面（捕获再处理——docker 失败即中止，勿以管道吞退出码）。
gather() {
  ALL_VOLS=$("$DOCKER" volume ls --filter name=fleetly-vol- --format '{{.Name}}' 2>/dev/null) || {
    echo "docker volume ls failed — is this the manager daemon?" >&2
    exit 1
  }
  DANGLING_VOLS=$("$DOCKER" volume ls --filter dangling=true --filter name=fleetly-vol- --format '{{.Name}}' 2>/dev/null) || {
    echo "docker volume ls (dangling) failed — is this the manager daemon?" >&2
    exit 1
  }
  SVC_NAMES=$("$DOCKER" service ls --format '{{.Name}}' 2>/dev/null) || {
    echo "docker service ls failed — this face needs the swarm manager daemon" >&2
    exit 1
  }
}

# report：分类报告一轮；计数落 hazard/stale/in_use/carrier 四变量。
# 卷名/载体名不含空白（docker 命名字符集），for 分词安全；sort 定输出序。
report() {
  hazard=0
  stale=0
  in_use=0
  carrier=0
  managed=0
  vols_seen=0
  echo "volumes (docker volume ls, prefix fleetly-vol-):"
  for v in $(printf '%s\n' "$ALL_VOLS" | sort); do
    [ -n "$v" ] || continue
    vols_seen=$((vols_seen + 1))
    if managed_volume "$v"; then
      managed=$((managed + 1))
      printf '  %-7s  %s\n' managed "$v"
      continue
    fi
    use_note=""
    if ! dangling "$v"; then
      use_note="  [in use — finish the carrier teardown step first]"
      in_use=$((in_use + 1))
    fi
    if rowid_keyed "$v"; then
      stale=$((stale + 1))
      printf '  %-7s  %s%s\n' stale "$v" "$use_note"
    else
      hazard=$((hazard + 1))
      printf '  %-7s  %s%s\n' hazard "$v" "$use_note"
      printf '%s\n' '           name-keyed legacy volume: a same-name rebuild mounts this stale data (runbook record 61 pits 2/3)'
    fi
  done
  [ "$vols_seen" -gt 0 ] || echo '  (no fleetly-vol-* volumes present)'
  echo "database carriers (docker service ls, prefix fleetly-db-):"
  db_seen=0
  for s in $(printf '%s\n' "$SVC_NAMES" | sort); do
    [ -n "$s" ] || continue
    case "$s" in
      fleetly-db-*)
        carrier=$((carrier + 1))
        db_seen=1
        printf '  %-7s  %s\n' carrier "$s"
        ;;
    esac
  done
  [ "$db_seen" -gt 0 ] || echo '  (none)'
}

echo "fleetly reset precheck (manager-local docker face; window: fresh db up, re-seed not started)"
echo

gather
report

if [ "$PRUNE" -eq 0 ]; then
  echo
  echo "summary: $managed managed (expected), $hazard name-keyed hazard, $stale row-id stale, $in_use in use, $carrier leftover database carriers"
  if [ "$((hazard + stale + carrier))" -gt 0 ]; then
    echo "next: review the listing, then re-run with --prune to remove dangling user-domain volumes (managed constants are never touched)"
    exit 3
  fi
  echo "clean: nothing to do before re-seeding"
  exit 0
fi

# --prune：只清 dangling 的用户域卷（hazard/stale 两类）。managed 永不动；
# in-use 卷跳过（docker 也会拒，先报告不硬试）；carrier 只登记不删（孤儿
# 律）。单体失败不中断（记尾错，末态复点定退出码）。
echo
echo "prune: removing dangling user-domain volumes"
prune_failed=0
for v in $(printf '%s\n' "$ALL_VOLS" | sort); do
  [ -n "$v" ] || continue
  managed_volume "$v" && continue
  if ! dangling "$v"; then
    echo "  skip (in use): $v"
    continue
  fi
  if "$DOCKER" volume rm "$v" >/dev/null 2>&1; then
    echo "  removed: $v"
  else
    echo "  failed:  $v (docker volume rm error; volume kept)" >&2
    prune_failed=$((prune_failed + 1))
  fi
done

# 末态复点：清后仍有用户域卷或残留载体 = 退出码 3（处置未尽）。
gather
echo
echo "post-prune state:"
report
echo
echo "summary: $managed managed (expected), $hazard name-keyed hazard, $stale row-id stale, $in_use in use, $carrier leftover database carriers; prune failures: $prune_failed"
if [ "$((hazard + stale + carrier))" -gt 0 ]; then
  echo "action needed: in-use volumes require finishing the carrier teardown step; leftover database carriers are registered only (orphan policy) — remove manually per the runbook reset sequence, or finish the teardown step on the old-generation CLI face"
  exit 3
fi
[ "$prune_failed" -eq 0 ] || exit 3
echo "clean: nothing left before re-seeding"
exit 0
