-- +goose Up
-- Team 轴接实（ADR-0028）：idx_projects_name 全局唯一改 (team_id, name)——
-- Team 1─* Project 的领域关系在 schema 层闭合；同名项目跨 Team 并存、
-- 同 Team 撞名拒。存量唯一索引迁移顺序：先建后删，同一事务内原子完成
--（goose 单迁移单事务；存量行全为 'default' 团队，新索引构建无冲突）。
CREATE UNIQUE INDEX idx_projects_team_name ON projects(team_id, name) WHERE deleted_at = '';
DROP INDEX idx_projects_name;

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
