# 平台升级与版本偏差

> **2026-10-19（S19）修订**：v1 **无平台运行期只读闸门、无 `platform restore` 动词**。正文升级序末步「解除只读」不成立——实际末步是启动后健康核对（`status`/`doctor` + 零扰动断言，见 runbook「平台升级操作序」）。失败回滚 = 停服 + Platform Backup 重放 + 旧版二进制回装（goose 只前滚；恢复窗内不接受写由停服保证，不由平台执法）。实现平台态闸门 + restore 动词列为后续独立项（触发：F2.3 恢复链落地）。历史正文不改写。

"升级永不弄坏你的东西"是差异化押注（竞品报告 §6.3-④），必须有设计支撑。决定：fleetlyd 升级序 = Platform Backup 前置 → 二进制/镜像替换 → SQLite 迁移（goose 前滚；失败回滚 = 恢复备份重放）→ Managed Provider 逐个 reconcile（镜像钉版、逐个升级）→ 解除只读。验收标准：升级不得导致任何用户 Workload 重启或路由中断（进 N2 e2e）。

CLI 与 server 版本偏差策略 = **N-1 兼容**：server 接受上一版 CLI；请求带版本协商头，超出偏差返回明确的升级提示；proto buf breaking 门禁保持。
