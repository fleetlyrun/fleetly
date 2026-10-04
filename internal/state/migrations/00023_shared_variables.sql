-- +goose Up
-- SharedVariable 实体表（F2.9，ADR-0043）：Project 级共享变量，值明文
-- （非敏感契约——敏感值走 secrets 表的 age 信封）。归一化期合成进
-- AppSpec（Project 层在下、App 层 env 覆盖，ADR-0027）；软删与 secrets
-- 同款 tombstone。项目删除不级联清理——与既有库材料同挂账口径
-- （F2.8 runbook 记录·五）。
CREATE TABLE shared_variables (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name       TEXT NOT NULL,
  value      TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_shared_variables_project_name ON shared_variables(project_id, name) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
