-- +goose Up
-- 审计 Team 轴（ADR-0035 决策 6）：ListAudit 的行级过滤锚。team_id 从
-- 写入 ctx 铸入（authn 拦截器=调用方 Team；webhook 面=App 归属 Team；
-- system/无身份动作与迁移前存量落 ''）。'' 行按平台级可见处理（内容是
-- 动作名+资源 ID+指纹，无值载荷）；不回填存量——旧行无归属事实，回填
-- 即编造。
ALTER TABLE audit ADD COLUMN team_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_audit_team ON audit(team_id);

-- +goose Down
-- goose 只前滚（架构 §8）。
