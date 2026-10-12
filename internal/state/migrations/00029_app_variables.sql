-- +goose Up
-- AppVariable 实体表（ADR-0072 变量三级合成顶层）：App 级平台变量，
-- 值明文（非敏感契约同 shared_variables——敏感值走 secrets 表的 age 信
-- 封）。冻结咽喉合成进 AppSpec（SharedVariable 底 < 源声明 env 中 <
-- AppVariable 顶）；软删 tombstone 与 shared_variables 同款。App 删除
-- 不级联清理——与 shared_variables 同挂账口径（F2.8 runbook 记录·五）。
CREATE TABLE app_variables (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  app_id     TEXT NOT NULL,
  name       TEXT NOT NULL,
  value      TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_app_variables_seq ON app_variables(seq);
CREATE UNIQUE INDEX idx_app_variables_app_name ON app_variables(app_id, name) WHERE deleted_at = '';

-- +goose Down
DROP TABLE app_variables;
