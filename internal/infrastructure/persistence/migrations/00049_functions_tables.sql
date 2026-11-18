-- +goose Up
-- Functions 两表（ADR-0102 任务②；行面真源 = 设计 v3 §4.1）：functions
-- 聚合根 + function_revisions 冻结体。建表即带 ADR-0101 保留律的执法前提
--（Prune/DeleteAllByFunction 走 repo 同批落地；current_revision_id 降软
-- 引用——聚合终局全清时行仍短暂持锚，FK 会拒绝合法清理序，与
-- deployments.to_revision 同判据的 ADR-0100 例外）。
--
-- 生命周期编码沿仓内既定形态：deleted_at TEXT（'' = 活跃；tombstone 即
-- state=deleted 语义，不设第二列）；(project_id, name) 唯一性限活跃行
--（部分索引——同名重建随 tombstone 立即可用，ADR-0070 名基语义）。
-- function_revisions 的 seq 是 per-Function 修订序（UNIQUE(function_id, seq)），
-- 与 revisions 表同款豁免（entityid 守卫 seqExemptTables）。

CREATE TABLE functions (
  id                 TEXT PRIMARY KEY,
  seq                INTEGER NOT NULL,
  project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name               TEXT NOT NULL,
  -- status 是观测缓存（pending|running|degraded|stopped；wire-gen 匹配才
  -- 采信——零状态类事件，设计 §4.1/§10）。
  status             TEXT NOT NULL DEFAULT 'pending',
  -- current_revision_id 软引用（ADR-0101/0100 例外清单）：保留窗清理与
  -- 聚合终局全清后悬空是设计内终态。
  current_revision_id TEXT NOT NULL DEFAULT '',
  -- generation 是行持久单调号（重启安全；Ensure 指纹对照锚）。
  generation          INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint    TEXT NOT NULL DEFAULT '',
  replicas            INTEGER NOT NULL DEFAULT 1,
  min_replicas        INTEGER NOT NULL DEFAULT 1,
  max_replicas        INTEGER NOT NULL DEFAULT 1,
  -- exposure 是可扩展枚举（private|public；缺省 private，设计 v3 §6 三律③）。
  exposure            TEXT NOT NULL DEFAULT 'private',
  public_host         TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  deleted_at          TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_functions_seq ON functions(seq);
CREATE UNIQUE INDEX idx_functions_project_name ON functions(project_id, name) WHERE deleted_at = '';
CREATE INDEX idx_functions_project_list ON functions(project_id, seq);

CREATE TABLE function_revisions (
  id          TEXT PRIMARY KEY,
  function_id TEXT NOT NULL REFERENCES functions(id) ON DELETE RESTRICT,
  seq         INTEGER NOT NULL,
  digest      TEXT NOT NULL,
  spec        BLOB NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_function_revisions_function_seq ON function_revisions(function_id, seq);
CREATE UNIQUE INDEX idx_function_revisions_function_digest ON function_revisions(function_id, digest);
