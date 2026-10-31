-- +goose Up
-- 事件聚合索引（B2a W1b facts 地基，zeabur 借鉴设计 §2.4 件 3）：outbox
-- 此前只有 (team_id, seq) 前向扫描（ListAfter，00032）——事件读面是 team
-- 维的；诊断面需要「近窗事件」的按聚合前向扫描（某 App/某 workload 的
-- 近窗事实，ListByAggregate 消费本索引），(team_id, aggregate,
-- aggregate_id, seq) 四列承载：team 等值 + 聚合等值 + seq 范围。
-- 纯索引无行为变化：存量行零回填，查询语义由 repo 面承载。
CREATE INDEX idx_outbox_aggregate ON outbox(team_id, aggregate, aggregate_id, seq);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
