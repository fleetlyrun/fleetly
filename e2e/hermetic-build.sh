#!/bin/sh
# e2e hermetic build verify（P5-2）：换装构建通道（build:hermetic）的
# hermeticity 探针，两腿——
#   a. 版本戳对账：构建后 fleetly --json version 的 commit 字段必须等于
#      构建时铸入的 HEAD 短戳（ldflags 三件的对账面）；
#   b. 双构建 sha256 一致：向 cmd/fleetlyd/ 落一枚未跟踪 junk 源文件
#      （合法 Go、永不调用）再走同款通道——HEAD 归档快照构建下 junk
#      物理不可入包，sha256 必须逐字节一致；若有人把通道退化回工作树
#      构建，junk 改变产物哈希即红（2026-10-11 换装事故的回归锚）。
# 任何失败 exit 1；trap 保证 junk 文件必清（快照临时目录由 build:hermetic
# 自身的 trap 收口）。
set -eu

JUNK="cmd/fleetlyd/zz_hermetic_probe_junk.go"

cleanup() {
  rm -f "$JUNK"
}
trap cleanup EXIT INT TERM

log() { printf '==> %s\n' "$1"; }

# ---- a. 第一轮 hermetic 构建 + 版本戳对账 ----
log "hermetic build pass 1"
mise run build:hermetic
SHA1=$(sha256sum bin/fleetlyd | awk '{print $1}')
log "pass 1 fleetlyd sha256: $SHA1"

COMMIT=$(git rev-parse --short HEAD)
# --json version 输出本地合成 JSON（两空格缩进）：version/commit/date 三键。
if ! ./bin/fleetly --json version | grep -q "\"commit\": \"$COMMIT\""; then
  echo "version stamp mismatch: commit $COMMIT not found in 'fleetly --json version' output" >&2
  ./bin/fleetly --json version >&2 || true
  exit 1
fi
log "version stamp verified: commit=$COMMIT"

# ---- b. junk 探针 + 第二轮构建（sha256 逐字节一致断言）----
log "dropping untracked junk source under cmd/fleetlyd, rebuilding (pass 2)"
cat > "$JUNK" <<'EOF'
// Package main 的 hermeticity 探针夹具（e2e/hermetic-build.sh 注入后随
// trap 清除，永不提交）：函数无调用方，合法编译单元。构建通道若退化回
// 工作树构建，本文件入包即改变产物哈希，verify:hermetic 转红。
package main

func hermeticProbeUnused() {}
EOF
mise run build:hermetic
SHA2=$(sha256sum bin/fleetlyd | awk '{print $1}')
log "pass 2 fleetlyd sha256: $SHA2"

if [ "$SHA1" != "$SHA2" ]; then
  echo "hermeticity probe FAILED: rebuild sha256 differs ($SHA1 vs $SHA2) — the build channel leaked working-tree state" >&2
  exit 1
fi

# ---- c. PASS 出口（失败已全部 exit 1）----
echo "PASS hermetic build verify: sha256 identical across builds ($SHA1), commit stamp $COMMIT"
