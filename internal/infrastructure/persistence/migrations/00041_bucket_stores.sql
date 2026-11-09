-- +goose Up
-- B5（ADR-0096 决策 1）：Bucket Store 聚合表 bucket_stores——与 pubsubs
-- （00040）同形（design §4.1 聚合与表：version/settings/resources 同列、
-- 备份三列、恢复两列、软删同律）。备份/恢复列同形在表而面不在——silo 转储
-- 挂账（design §4.5 L4 表；DataCapabilities.Backup=false 门控 backups 卫星
-- 不铸行、受理面无 TriggerBackup/restore_from_backup），同形列是 kind
-- descriptor 归一的行面前提（三表同形、机制层经 descriptor 归一）。前向
-- 迁移（非 flag-day：新表新行，存量零触，design §7.6——goose 只前滚，
-- staging 走 ADR-0015 升级序即可）。
CREATE TABLE bucket_stores (
  id                    TEXT PRIMARY KEY,
  seq                   INTEGER NOT NULL,
  project_id            TEXT NOT NULL,
  name                  TEXT NOT NULL,
  engine                TEXT NOT NULL,
  -- version 是用户面 major 短名（ADR-0094：创建时选定、存续期不可变；
  -- silo 单版 RELEASE 年月短名）。
  version               TEXT NOT NULL,
  -- settings 是旋钮键值 JSON（buckettemplate 白名单值域；silo v1 零旋钮
  -- 恒 '{}'）。
  settings              TEXT NOT NULL DEFAULT '{}',
  cpu_millis            INTEGER,
  memory_mb             INTEGER,
  -- credentials_ref 是凭证 Secret 名（bucketstore:<name>，ADR-0096 决策 4
  -- ——值 = endpoint/access_key/secret_key k=v 方言串）。
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
CREATE UNIQUE INDEX idx_bucket_stores_seq ON bucket_stores(seq);
CREATE UNIQUE INDEX idx_bucket_stores_project_name ON bucket_stores(project_id, name) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
