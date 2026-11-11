-- +goose NO TRANSACTION
-- +goose Up
-- 软删列归一（2026-11-11）：早期七表（00001 projects/apps、00003 routes、
-- 00004 secrets/volumes/networks、00014 databases）的 deleted_at 可空，
-- 而 00023 起的新表一律 TEXT NOT NULL DEFAULT ''。读径以 deleted_at = ''
-- 判活跃，partial unique index 也只收该谓词为真的行——NULL 行既不可见
-- （软删后永远查不到）也不入唯一索引（等同已删），活跃行可能重名且不可
-- 见。本迁移把七表 deleted_at 统一为 NOT NULL DEFAULT ''：存量 NULL 是
-- 旧代码未软删的活跃行，COALESCE 归一为空串后恢复可见；非空 tombstone
-- 原值保留（UTC RFC3339 文本）。
--
-- 风险边界：若存量 NULL 行存在同 (team/project, name) 或 (host, path)
-- 重名，新 partial unique index 创建会失败——这是 fail-closed 的诚实信号
-- （旧索引从未覆盖这些行），按内部测试前提重建库，不做静默去重。
--
-- SQLite 不能改列可空性，只能重建表。apps 被 app_hooks.app_id 外键引用，
-- DROP 父表需在外键关闭时进行；PRAGMA foreign_keys 不能在事务内切换，
-- 故本迁移 NO TRANSACTION + 显式事务（写柄 MaxOpenConns(1)，pragma 与
-- 后续语句同连接）。结束恢复 foreign_keys=ON；重建后的 apps 行 id 不变，
-- app_hooks 外键在 PRAGMA foreign_key_check 下保持完整。
--
-- 索引按“当前形态”逐表重建：00010 已把 idx_projects_name 换成
-- idx_projects_team_name，00016 已把 idx_routes_project_host_path 换成
-- idx_routes_host_path——只重建在役索引，不复活历史索引。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE projects_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  name       TEXT NOT NULL,
  team_id    TEXT NOT NULL DEFAULT 'default',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
-- staging 表也先挂 UNIQUE(seq)：复制期双铸即失败（与正式表同不变量），
-- 也满足迁移契约“每张 id+seq 表必须有 UNIQUE(seq)”的静态扫描；改名后
-- 换成正式索引名。
CREATE UNIQUE INDEX idx_projects_new_seq ON projects_new(seq);
INSERT INTO projects_new (id, seq, name, team_id, created_at, updated_at, deleted_at)
  SELECT id, seq, name, team_id, created_at, updated_at, COALESCE(deleted_at, '')
  FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
DROP INDEX idx_projects_new_seq;
CREATE UNIQUE INDEX idx_projects_seq ON projects(seq);
CREATE UNIQUE INDEX idx_projects_team_name ON projects(team_id, name) WHERE deleted_at = '';

CREATE TABLE apps_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  -- project_id 无外键（00001 历史形态，级联由应用层收口）；重建保持原样。
  project_id TEXT NOT NULL,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_apps_new_seq ON apps_new(seq);
INSERT INTO apps_new (id, seq, project_id, name, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, created_at, updated_at, COALESCE(deleted_at, '')
  FROM apps;
DROP TABLE apps;
ALTER TABLE apps_new RENAME TO apps;
DROP INDEX idx_apps_new_seq;
CREATE UNIQUE INDEX idx_apps_seq ON apps(seq);
CREATE UNIQUE INDEX idx_apps_project_name ON apps(project_id, name) WHERE deleted_at = '';

CREATE TABLE routes_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL,
  host       TEXT NOT NULL,
  path       TEXT NOT NULL DEFAULT '',
  app_id     TEXT NOT NULL,
  process    TEXT NOT NULL,
  port       INTEGER NOT NULL,
  protocol   TEXT NOT NULL DEFAULT 'http',
  tls_mode   TEXT NOT NULL DEFAULT 'auto',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_routes_new_seq ON routes_new(seq);
INSERT INTO routes_new
    (id, seq, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at,
         COALESCE(deleted_at, '')
  FROM routes;
DROP TABLE routes;
ALTER TABLE routes_new RENAME TO routes;
DROP INDEX idx_routes_new_seq;
CREATE UNIQUE INDEX idx_routes_seq ON routes(seq);
CREATE UNIQUE INDEX idx_routes_host_path ON routes(host, path) WHERE deleted_at = '';
CREATE INDEX idx_routes_app ON routes(app_id);

CREATE TABLE databases_new (
  id                     TEXT PRIMARY KEY,
  seq                    INTEGER NOT NULL,
  project_id             TEXT NOT NULL,
  name                   TEXT NOT NULL,
  engine                 TEXT NOT NULL,
  credentials_ref        TEXT NOT NULL,
  generation             INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint       TEXT NOT NULL DEFAULT '',
  backup_interval_secs   INTEGER NOT NULL DEFAULT 86400,
  backup_retention_secs  INTEGER NOT NULL DEFAULT 604800,
  status                 TEXT NOT NULL DEFAULT 'pending',
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL,
  deleted_at             TEXT NOT NULL DEFAULT '',
  last_backup_at         TEXT NOT NULL DEFAULT '',
  restore_from_backup    TEXT NOT NULL DEFAULT '',
  restore_error          TEXT NOT NULL DEFAULT '',
  version                TEXT NOT NULL,
  settings               TEXT NOT NULL DEFAULT '{}',
  cpu_millis             INTEGER,
  memory_mb              INTEGER,
  restore_state          TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_databases_new_seq ON databases_new(seq);
INSERT INTO databases_new
    (id, seq, project_id, name, engine, credentials_ref, generation, spec_fingerprint,
     backup_interval_secs, backup_retention_secs, status, created_at, updated_at, deleted_at,
     last_backup_at, restore_from_backup, restore_error, version, settings, cpu_millis, memory_mb,
     restore_state)
  SELECT id, seq, project_id, name, engine, credentials_ref, generation, spec_fingerprint,
         backup_interval_secs, backup_retention_secs, status, created_at, updated_at,
         COALESCE(deleted_at, ''),
         last_backup_at, restore_from_backup, restore_error, version, settings, cpu_millis, memory_mb,
         restore_state
  FROM databases;
DROP TABLE databases;
ALTER TABLE databases_new RENAME TO databases;
DROP INDEX idx_databases_new_seq;
CREATE UNIQUE INDEX idx_databases_seq ON databases(seq);
CREATE UNIQUE INDEX idx_databases_project_name ON databases(project_id, name) WHERE deleted_at = '';

CREATE TABLE secrets_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  ciphertext  BLOB NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_secrets_new_seq ON secrets_new(seq);
INSERT INTO secrets_new (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, COALESCE(deleted_at, '')
  FROM secrets;
DROP TABLE secrets;
ALTER TABLE secrets_new RENAME TO secrets;
DROP INDEX idx_secrets_new_seq;
CREATE UNIQUE INDEX idx_secrets_seq ON secrets(seq);
CREATE UNIQUE INDEX idx_secrets_project_name ON secrets(project_id, name) WHERE deleted_at = '';

CREATE TABLE volumes_new (
  id             TEXT PRIMARY KEY,
  seq            INTEGER NOT NULL,
  project_id     TEXT NOT NULL,
  name           TEXT NOT NULL,
  pinned_node_id TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  deleted_at     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_volumes_new_seq ON volumes_new(seq);
INSERT INTO volumes_new (id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, pinned_node_id, created_at, updated_at, COALESCE(deleted_at, '')
  FROM volumes;
DROP TABLE volumes;
ALTER TABLE volumes_new RENAME TO volumes;
DROP INDEX idx_volumes_new_seq;
CREATE UNIQUE INDEX idx_volumes_seq ON volumes(seq);
CREATE UNIQUE INDEX idx_volumes_project_name ON volumes(project_id, name) WHERE deleted_at = '';

CREATE TABLE networks_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  egress_none INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_networks_new_seq ON networks_new(seq);
INSERT INTO networks_new (id, seq, project_id, name, egress_none, created_at, deleted_at)
  SELECT id, seq, project_id, name, egress_none, created_at, COALESCE(deleted_at, '')
  FROM networks;
DROP TABLE networks;
ALTER TABLE networks_new RENAME TO networks;
DROP INDEX idx_networks_new_seq;
CREATE UNIQUE INDEX idx_networks_seq ON networks(seq);
CREATE UNIQUE INDEX idx_networks_project_name ON networks(project_id, name) WHERE deleted_at = '';

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
