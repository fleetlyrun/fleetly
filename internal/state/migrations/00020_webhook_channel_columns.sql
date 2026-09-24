-- W4 通道扩展（observability 设计 §8，D-W4-4，v0.3 W4-S3；迁移只加法纪
-- 律）：webhook_endpoints 增 type / target 两列——通知通道从单一 webhook
-- 扩为 webhook|slack|email 三通道。
--
--   type    通道词表 webhook|slack|email；NOT NULL DEFAULT 'webhook'——存
--           量行由 DEFAULT 自动回填为 webhook（行为逐字不变的升级路径）。
--           SQLite 无 CHECK 修改通道，词表由 state 层写入通道双保险（
--           webhook_deliveries.status 同款纪律）。
--   target  email 通道的收件地址（to）；webhook/slack 为空串——url 列被
--           webhook/slack 复用（Slack Incoming Webhook URL 即端点 URL），
--           email 端点 url 留空、收件地址独立成列。
--
-- 台账/游标零变化：通道分叉只发生在单次投递尝试的执行体（设计 §8.4），
-- webhook_deliveries / webhook_state 不动。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
ALTER TABLE webhook_endpoints ADD COLUMN type TEXT NOT NULL DEFAULT 'webhook';
ALTER TABLE webhook_endpoints ADD COLUMN target TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE webhook_endpoints DROP COLUMN type;
ALTER TABLE webhook_endpoints DROP COLUMN target;
