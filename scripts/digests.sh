#!/bin/sh
# digests.sh —— 数据服务引擎版本矩阵的 digest 取值/对账工具（ADR-0094
# 决策 6 SOP：追 patch = 本脚本拉 Docker Hub index digest 回填注册表常量，
# 与 TestTemplateFaces/TestImageDigestsPinned 门禁钉板同 commit）。
#
# 用法：
#   scripts/digests.sh [engine [version]]         # 取值 + 对账（无参 = 全矩阵）
#   scripts/digests.sh -check [engine [version]]  # 漂移即退出码 1（CI 对账模式）
#
# 示例：
#   scripts/digests.sh postgres 18     # 单条：engine version repo:tag <digest> <MATCH|DRIFT>
#   scripts/digests.sh -check          # 全矩阵漂移断言
#
# 实现在 scripts/digests/main.go（go 承载，mise 工具链内零额外依赖；矩阵
# 真源 = dbtemplate 注册表运行时读取，本脚本不复制 tag 表）。
set -e
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
exec go run ./scripts/digests "$@"
