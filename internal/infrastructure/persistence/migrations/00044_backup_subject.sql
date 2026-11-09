-- +goose Up
-- 备份台账主体中性化（2026-11-10）：backups 表从 database 单类型承载改为
-- 主体中性（database/pubsub/bucketstore 共用）。列改名 database_id →
-- subject_id，新增 kind 类型词面列（dataservicetemplate.Kind*）。
--
-- 回填：pubsub 引擎（nats/kafka/redpanda）的存量行标 kind='pubsub'，其余
-- 标 'database'。预发布期允许直接重置（无兼容层承诺）；本迁移仍按存量行
-- 语义回填，避免重置前的开发库升级后行值失真。
--
-- 索引：旧 idx_backups_database(database_id, seq) 改名重建为
-- idx_backups_subject(subject_id, seq)。实例 id 全局唯一，故过滤不需要
-- kind 轴；kind 是执行面路由与事件聚合轴的判据。
ALTER TABLE backups RENAME COLUMN database_id TO subject_id;
ALTER TABLE backups ADD COLUMN kind TEXT NOT NULL DEFAULT 'database';
UPDATE backups SET kind = 'pubsub' WHERE engine IN ('nats', 'kafka', 'redpanda');
DROP INDEX idx_backups_database;
CREATE INDEX idx_backups_subject ON backups(subject_id, seq);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
