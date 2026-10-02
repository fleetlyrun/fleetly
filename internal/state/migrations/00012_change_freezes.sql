-- +goose Up
-- 变更冻结窗（ADR-0017 附录 A.3，F1.9）：team_id='' 是全局冻结行；每 scope
-- 至多一条活跃行（部分唯一索引，Set 命中活跃冻结即 E_CONFLICT）；lift =
-- 落 lifted_at（历史行保留，List 可回读）。
CREATE TABLE change_freezes (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  lifted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_change_freezes_active_team ON change_freezes(team_id) WHERE lifted_at = '';
CREATE INDEX idx_change_freezes_list ON change_freezes(id);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
