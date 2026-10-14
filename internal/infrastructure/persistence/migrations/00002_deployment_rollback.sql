-- +goose Up
-- 领域模型 §4：failed → (自动) rolling-back → succeeded|failed。第二个
-- failed（回滚也失败）是终态，不再自动重试——rollback_attempted 区分
-- "待自动回滚的 failed"与"回滚已尽力终态 failed"（显式 rollback 命令
-- 创建新 Deployment，不复用终态行）。
ALTER TABLE deployments ADD COLUMN rollback_attempted INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- goose 只前滚（架构 §8）。
