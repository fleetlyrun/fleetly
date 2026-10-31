-- +goose Up
-- Alert Rule pending 持续窗直接改列（B2a W1b，zeabur 借鉴设计第二轮定稿：
-- 已裁持久化 + 直接改列、无双写过渡——开发阶段无兼容包袱）：越限首见
-- 起窗落列（pending_since）、窗内采样推进不重置、满窗迁移 firing、回落
-- 归位清列；engine 重启/规则装载时从行恢复起算点（重启不重置——此前
-- pending 窗在 engine 内存 map，重启即丢是采样近似语义的一部分，ADR-0041
-- 该边界自本列起收窄为「采样连续性仍按采样序」）。
--
-- 语义：'' = 无 pending 窗（ok 态常态）；非空 = UTC RFC3339 秒精度文本
-- （state.FormatTime 同口径）。存量行回填 ''（升级时无在途窗可编造）；
-- 迁移沿（firing/ok）由既有 state/state_since 列承载，不建新索引
-- （评估面走 ListEnabled 全量小表）。
ALTER TABLE alert_rules ADD COLUMN pending_since TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
