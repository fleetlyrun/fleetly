-- +goose Up
-- B4b（ADR-0096 决策 1）：PubSub 聚合表 pubsubs——与 databases 同形
-- （design §4.1 聚合与表：version/settings/resources 同列〔00039 形〕、
-- 备份三列、恢复两列〔00019 形〕、软删同律〔00014 形〕）。前向迁移
-- （非 flag-day：新表新行，存量零触，design §7.6——goose 只前滚，
-- staging 走 ADR-0015 升级序即可）。
CREATE TABLE pubsubs (
  id                    TEXT PRIMARY KEY,
  seq                   INTEGER NOT NULL,
  project_id            TEXT NOT NULL,
  name                  TEXT NOT NULL,
  engine                TEXT NOT NULL,
  -- version 是用户面 major 短名（ADR-0094：创建时选定、存续期不可变）。
  version               TEXT NOT NULL,
  -- settings 是旋钮键值 JSON（pubtemplate 白名单值域）；缺省 '{}'。
  settings              TEXT NOT NULL DEFAULT '{}',
  cpu_millis            INTEGER,
  memory_mb             INTEGER,
  -- credentials_ref 是凭证 Secret 名（pubsub:<name>，ADR-0096 决策 4）。
  credentials_ref       TEXT NOT NULL,
  generation            INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint      TEXT NOT NULL DEFAULT '',
  backup_interval_secs   INTEGER NOT NULL DEFAULT 86400,
  backup_retention_secs INTEGER NOT NULL DEFAULT 604800,
  -- status: pending | running | degraded | stopped（收敛环从观测缓存推进）。
  status                TEXT NOT NULL DEFAULT 'pending',
  last_backup_at        TEXT NOT NULL DEFAULT '',
  restore_from_backup   TEXT NOT NULL DEFAULT '',
  restore_error         TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  deleted_at            TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_pubsubs_seq ON pubsubs(seq);
CREATE UNIQUE INDEX idx_pubsubs_project_name ON pubsubs(project_id, name) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
