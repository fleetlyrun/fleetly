# 平台升级与版本偏差

"升级永不弄坏你的东西"是差异化押注（竞品报告 §6.3-④），必须有设计支撑。决定：fleetlyd 升级序 = Platform Backup 前置 → 二进制/镜像替换 → SQLite 迁移（goose 前滚；失败回滚 = 恢复备份重放）→ Managed Provider 逐个 reconcile（镜像钉版、逐个升级）→ 解除只读。验收标准：升级不得导致任何用户 Workload 重启或路由中断（进 N2 e2e）。

CLI 与 server 版本偏差策略 = **N-1 兼容**：server 接受上一版 CLI；请求带版本协商头，超出偏差返回明确的升级提示；proto buf breaking 门禁保持。
