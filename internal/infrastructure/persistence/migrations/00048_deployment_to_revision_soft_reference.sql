-- +goose NO TRANSACTION
-- +goose Up
-- ADR-0101：deployments.to_revision 降软引用（Revision 保留窗清理后，历史
-- deployment 行引用的 revision 悬空是设计内终态——该外键「防悬空引用」的
-- 保护价值不复存在；FromRevision 本就无外键）。SQLite 不能 DROP CONSTRAINT，
-- 按 00047 先例重建表；除该边外列/索引/其余外键逐字节保持。
-- 孤儿行处置与 00047 同款：复制期 foreign_keys 关闭，末尾全库复核 fail-closed。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

-- deployments：仅去 to_revision → revisions(id) 一条边。
CREATE TABLE deployments_new (
  id                 TEXT PRIMARY KEY,
  seq                INTEGER NOT NULL,
  app_id             TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  from_revision      TEXT NOT NULL DEFAULT '',
  to_revision        TEXT NOT NULL,
  state              TEXT NOT NULL,
  generation         INTEGER NOT NULL DEFAULT 0,
  idempotency_key    TEXT NOT NULL DEFAULT '',
  commit_sha         TEXT NOT NULL DEFAULT '',
  superseded_by      TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  observe_deadline   TEXT NOT NULL DEFAULT '',
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  finished_at        TEXT NOT NULL DEFAULT '',
  rollback_attempted INTEGER NOT NULL DEFAULT 0,
  pre_deploy         TEXT NOT NULL DEFAULT '',
  from_generation    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_deployments_new_seq ON deployments_new(seq);
INSERT INTO deployments_new
    (id, seq, app_id, from_revision, to_revision, state, generation, idempotency_key,
     commit_sha, superseded_by, error, observe_deadline, created_at, updated_at,
     finished_at, rollback_attempted, pre_deploy, from_generation)
  SELECT id, seq, app_id, from_revision, to_revision, state, generation, idempotency_key,
         commit_sha, superseded_by, error, observe_deadline, created_at, updated_at,
         finished_at, rollback_attempted, pre_deploy, from_generation
  FROM deployments;
DROP TABLE deployments;
ALTER TABLE deployments_new RENAME TO deployments;
DROP INDEX idx_deployments_new_seq;
CREATE UNIQUE INDEX idx_deployments_seq ON deployments(seq);
CREATE INDEX idx_deployments_app ON deployments(app_id);
CREATE INDEX idx_deployments_state ON deployments(state);
CREATE UNIQUE INDEX idx_deployments_app_idem_active ON deployments(app_id, idempotency_key)
  WHERE idempotency_key != ''
    AND state IN ('queued','preparing','building','releasing','observing','rolling-back');

-- 全库外键复核（与 00047 同款）：任一孤儿行即 CHECK 失败，事务回滚。
CREATE TEMP TABLE fk_violation_guard (
  violations INTEGER NOT NULL CHECK (violations = 0)
);
INSERT INTO fk_violation_guard (violations)
  SELECT COUNT(*) FROM pragma_foreign_key_check;
DROP TABLE fk_violation_guard;

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
