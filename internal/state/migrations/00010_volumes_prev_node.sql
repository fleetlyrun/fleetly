-- multi-node §2.8（E1-7，迁移 00010，唯一 DB 迁移，只加纪律）：volumes 增列
-- prev_platform_node_id——显式换点（rebind）时登记数据原在节点，作为源节点
-- 残留卷的清理指引锚（ListVolumes 的 residual 派生标记：
-- prev_platform_node_id != '' 的 active 行指向源节点待 docker volume rm）。
--
-- 语义：空值 = 现状语义（卷从未跨节点迁移，无残留指引）——既有行全部
-- 缺省空串，行为逐字节不变；新列只增不改既有列、不动约束（架构 §2.8
-- 契约版本化纪律：迁移只加法，回滚 = 恢复快照）。

-- +goose Up
ALTER TABLE volumes ADD COLUMN prev_platform_node_id TEXT NOT NULL DEFAULT '';
