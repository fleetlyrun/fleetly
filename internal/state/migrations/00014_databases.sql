-- +goose Up
-- Database 聚合行（F1.12，ADR-0029）：模板渲染的托管有状态服务。无
-- Revision/Deployment 行——模板参数创建即不可变（版本矩阵随 F2）。
-- generation 是已下发投影的单调编号；spec_fingerprint 是该编号对应的
-- 投影指纹（重启安全：指纹未变则重放同 gen，不触发载体滚动）。
-- 挂靠卷与凭证 Secret 是 Project 级材料（卷名 = 数据库名的确定性公式，
-- 行上不另存引用）。
CREATE TABLE databases (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT NOT NULL,
  name                  TEXT NOT NULL,
  engine                TEXT NOT NULL,
  -- credentials_ref 是凭证 Secret 名（database:<name>，ADR-0029 决策 6）。
  credentials_ref       TEXT NOT NULL,
  generation            INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint      TEXT NOT NULL DEFAULT '',
  backup_interval_secs   INTEGER NOT NULL DEFAULT 86400,
  backup_retention_secs INTEGER NOT NULL DEFAULT 604800,
  -- status: pending | running | degraded | stopped（收敛环从观测缓存推进）。
  status     TEXT NOT NULL DEFAULT 'pending',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE UNIQUE INDEX idx_databases_project_name ON databases(project_id, name) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
