-- +goose NO TRANSACTION
-- +goose Up
-- 核心资源图外键（2026-11-11）：SQLite 不能原地加外键，按 00046 先例
-- 逐表重建（临时表 + 复制 + 改名 + 在役索引按当前形态重建）。外键一律
-- ON DELETE RESTRICT——软删语义（deleted_at tombstone）仍归应用层；外键
-- 只阻止硬删仍被引用的父行，不级联。state/status 枚举值域仍归 domain，
-- 不在 SQL 复制第二真源（本迁移不含任何枚举 CHECK）。
--
-- 跳过面（schema 事实决定，非省事）：
--   * deployments.from_revision / builds.revision_id：'' = 无基线/未关联，
--     空串不是 revisions 行；加外键会拒绝合法行。
--   * backups.subject_id：主体中性（database/pubsub/bucketstore 共用），
--     多态列无单一父表。
--   * github_apps.project_id：'' = 平台级行，空串不是 projects 行。
--   * change_freezes.team_id：'' = 全局冻结行（00012 口径），空串不是
--     teams 行；普通外键会拒绝全局冻结，故不重建本表。
--   * 身份表（memberships/tokens/invitations/app_hooks/hook_deliveries）
--     已有外键，不重复。
--
-- 孤儿行处置：复制期 foreign_keys 关闭（DDL 前提），因此末尾用
-- pragma_foreign_key_check 全库复核；任一孤儿行即 CHECK 失败回滚——
-- 迁移 fail-closed，不做静默丢弃/补父行。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

-- projects：team_id → teams(id)。
CREATE TABLE projects_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  name       TEXT NOT NULL,
  team_id    TEXT NOT NULL DEFAULT 'default' REFERENCES teams(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_projects_new_seq ON projects_new(seq);
INSERT INTO projects_new (id, seq, name, team_id, created_at, updated_at, deleted_at)
  SELECT id, seq, name, team_id, created_at, updated_at, deleted_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
DROP INDEX idx_projects_new_seq;
CREATE UNIQUE INDEX idx_projects_seq ON projects(seq);
CREATE UNIQUE INDEX idx_projects_team_name ON projects(team_id, name) WHERE deleted_at = '';

-- apps：project_id → projects(id)。
CREATE TABLE apps_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_apps_new_seq ON apps_new(seq);
INSERT INTO apps_new (id, seq, project_id, name, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, created_at, updated_at, deleted_at FROM apps;
DROP TABLE apps;
ALTER TABLE apps_new RENAME TO apps;
DROP INDEX idx_apps_new_seq;
CREATE UNIQUE INDEX idx_apps_seq ON apps(seq);
CREATE UNIQUE INDEX idx_apps_project_name ON apps(project_id, name) WHERE deleted_at = '';

-- revisions：app_id → apps(id)。
CREATE TABLE revisions_new (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  seq         INTEGER NOT NULL,
  digest      TEXT NOT NULL,
  spec        BLOB NOT NULL,
  created_at  TEXT NOT NULL,
  source_text TEXT NOT NULL DEFAULT '',
  UNIQUE(app_id, seq)
);
CREATE UNIQUE INDEX idx_revisions_new_app_digest ON revisions_new(app_id, digest);
INSERT INTO revisions_new (id, app_id, seq, digest, spec, created_at, source_text)
  SELECT id, app_id, seq, digest, spec, created_at, source_text FROM revisions;
DROP TABLE revisions;
ALTER TABLE revisions_new RENAME TO revisions;
DROP INDEX idx_revisions_new_app_digest;
CREATE UNIQUE INDEX idx_revisions_app_digest ON revisions(app_id, digest);

-- deployments：app_id → apps(id)；to_revision → revisions(id)。
CREATE TABLE deployments_new (
  id                 TEXT PRIMARY KEY,
  seq                INTEGER NOT NULL,
  app_id             TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  from_revision      TEXT NOT NULL DEFAULT '',
  to_revision        TEXT NOT NULL REFERENCES revisions(id) ON DELETE RESTRICT,
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

-- builds：app_id → apps(id)；revision_id 可空串（无关联），不设外键。
CREATE TABLE builds_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  app_id      TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  revision_id TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL,
  digest      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  repo        TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_builds_new_seq ON builds_new(seq);
INSERT INTO builds_new
    (id, seq, app_id, revision_id, state, digest, error, created_at, updated_at, finished_at, repo)
  SELECT id, seq, app_id, revision_id, state, digest, error, created_at, updated_at, finished_at, repo
  FROM builds;
DROP TABLE builds;
ALTER TABLE builds_new RENAME TO builds;
DROP INDEX idx_builds_new_seq;
CREATE UNIQUE INDEX idx_builds_seq ON builds(seq);
CREATE INDEX idx_builds_app ON builds(app_id);
CREATE INDEX idx_builds_state ON builds(state);

-- routes：project_id → projects(id)；app_id → apps(id)。
CREATE TABLE routes_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  host       TEXT NOT NULL,
  path       TEXT NOT NULL DEFAULT '',
  app_id     TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
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
    (id, seq, project_id, host, path, app_id, process, port, protocol, tls_mode,
     created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, host, path, app_id, process, port, protocol, tls_mode,
         created_at, updated_at, deleted_at
  FROM routes;
DROP TABLE routes;
ALTER TABLE routes_new RENAME TO routes;
DROP INDEX idx_routes_new_seq;
CREATE UNIQUE INDEX idx_routes_seq ON routes(seq);
CREATE UNIQUE INDEX idx_routes_host_path ON routes(host, path) WHERE deleted_at = '';
CREATE INDEX idx_routes_app ON routes(app_id);

-- secrets：project_id → projects(id)。
CREATE TABLE secrets_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name        TEXT NOT NULL,
  ciphertext  BLOB NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_secrets_new_seq ON secrets_new(seq);
INSERT INTO secrets_new
    (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at
  FROM secrets;
DROP TABLE secrets;
ALTER TABLE secrets_new RENAME TO secrets;
DROP INDEX idx_secrets_new_seq;
CREATE UNIQUE INDEX idx_secrets_seq ON secrets(seq);
CREATE UNIQUE INDEX idx_secrets_project_name ON secrets(project_id, name) WHERE deleted_at = '';

-- configs：project_id → projects(id)。
CREATE TABLE configs_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  version    INTEGER NOT NULL,
  content    BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(project_id, name, version)
);
CREATE UNIQUE INDEX idx_configs_new_seq ON configs_new(seq);
INSERT INTO configs_new (id, seq, project_id, name, version, content, created_at)
  SELECT id, seq, project_id, name, version, content, created_at FROM configs;
DROP TABLE configs;
ALTER TABLE configs_new RENAME TO configs;
DROP INDEX idx_configs_new_seq;
CREATE UNIQUE INDEX idx_configs_seq ON configs(seq);

-- volumes：project_id → projects(id)。
CREATE TABLE volumes_new (
  id             TEXT PRIMARY KEY,
  seq            INTEGER NOT NULL,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name           TEXT NOT NULL,
  pinned_node_id TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  deleted_at     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_volumes_new_seq ON volumes_new(seq);
INSERT INTO volumes_new
    (id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
  FROM volumes;
DROP TABLE volumes;
ALTER TABLE volumes_new RENAME TO volumes;
DROP INDEX idx_volumes_new_seq;
CREATE UNIQUE INDEX idx_volumes_seq ON volumes(seq);
CREATE UNIQUE INDEX idx_volumes_project_name ON volumes(project_id, name) WHERE deleted_at = '';

-- networks：project_id → projects(id)。
CREATE TABLE networks_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name        TEXT NOT NULL,
  egress_none INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_networks_new_seq ON networks_new(seq);
INSERT INTO networks_new (id, seq, project_id, name, egress_none, created_at, deleted_at)
  SELECT id, seq, project_id, name, egress_none, created_at, deleted_at FROM networks;
DROP TABLE networks;
ALTER TABLE networks_new RENAME TO networks;
DROP INDEX idx_networks_new_seq;
CREATE UNIQUE INDEX idx_networks_seq ON networks(seq);
CREATE UNIQUE INDEX idx_networks_project_name ON networks(project_id, name) WHERE deleted_at = '';

-- network_peers：network_id → networks(id)；peer_project_id → projects(id)。
-- （00011 无 peer_network_id 列：声明面是 (network, peer project)，挂靠方
-- 项目轴即 peer_project_id。）
CREATE TABLE network_peers_new (
  id              TEXT PRIMARY KEY,
  seq             INTEGER NOT NULL,
  network_id      TEXT NOT NULL REFERENCES networks(id) ON DELETE RESTRICT,
  peer_project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  state           TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  approved_at     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_network_peers_new_seq ON network_peers_new(seq);
INSERT INTO network_peers_new
    (id, seq, network_id, peer_project_id, state, created_at, updated_at, approved_at)
  SELECT id, seq, network_id, peer_project_id, state, created_at, updated_at, approved_at
  FROM network_peers;
DROP TABLE network_peers;
ALTER TABLE network_peers_new RENAME TO network_peers;
DROP INDEX idx_network_peers_new_seq;
CREATE UNIQUE INDEX idx_network_peers_seq ON network_peers(seq);
CREATE UNIQUE INDEX idx_network_peers_active ON network_peers(network_id, peer_project_id) WHERE state != 'revoked';
CREATE INDEX idx_network_peers_peer ON network_peers(peer_project_id);

-- tasks：project_id → projects(id)。
CREATE TABLE tasks_new (
  id                  TEXT PRIMARY KEY,
  seq                 INTEGER NOT NULL,
  project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name                TEXT NOT NULL DEFAULT '',
  form                TEXT NOT NULL,
  state               TEXT NOT NULL,
  spec                BLOB NOT NULL,
  owner_token_id      TEXT NOT NULL DEFAULT '',
  desired_concurrency INTEGER NOT NULL DEFAULT 1,
  network_group       TEXT NOT NULL DEFAULT '',
  dns_name            TEXT NOT NULL DEFAULT '',
  lease_deadline      TEXT NOT NULL DEFAULT '',
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  finished_at         TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_tasks_new_seq ON tasks_new(seq);
INSERT INTO tasks_new
    (id, seq, project_id, name, form, state, spec, owner_token_id, desired_concurrency,
     network_group, dns_name, lease_deadline, created_at, updated_at, finished_at)
  SELECT id, seq, project_id, name, form, state, spec, owner_token_id, desired_concurrency,
         network_group, dns_name, lease_deadline, created_at, updated_at, finished_at
  FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
DROP INDEX idx_tasks_new_seq;
CREATE UNIQUE INDEX idx_tasks_seq ON tasks(seq);
CREATE INDEX idx_tasks_project ON tasks (project_id, seq);
CREATE INDEX idx_tasks_driving ON tasks (state);
CREATE UNIQUE INDEX idx_tasks_name ON tasks (project_id, name) WHERE name != '' AND state != 'deleted';

-- runs：task_id → tasks(id)；project_id → projects(id)。
CREATE TABLE runs_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  state       TEXT NOT NULL,
  stop_reason TEXT NOT NULL DEFAULT '',
  exit_code   INTEGER,
  workload_id TEXT NOT NULL DEFAULT '',
  dns_name    TEXT NOT NULL DEFAULT '',
  deadline    TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  started_at  TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_runs_new_seq ON runs_new(seq);
INSERT INTO runs_new
    (id, seq, task_id, project_id, state, stop_reason, exit_code, workload_id, dns_name,
     deadline, created_at, updated_at, started_at, finished_at)
  SELECT id, seq, task_id, project_id, state, stop_reason, exit_code, workload_id, dns_name,
         deadline, created_at, updated_at, started_at, finished_at
  FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
DROP INDEX idx_runs_new_seq;
CREATE UNIQUE INDEX idx_runs_seq ON runs(seq);
CREATE INDEX idx_runs_task ON runs (task_id, seq);
CREATE INDEX idx_runs_driving ON runs (state);
CREATE INDEX idx_runs_deadline ON runs (deadline) WHERE deadline != '';

-- schedules：project_id → projects(id)。
CREATE TABLE schedules_new (
  id           TEXT PRIMARY KEY,
  seq          INTEGER NOT NULL,
  project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name         TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL,
  cron_expr    TEXT NOT NULL,
  timezone     TEXT NOT NULL,
  spec         BLOB NOT NULL,
  next_fire_at TEXT NOT NULL DEFAULT '',
  last_task_id TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_schedules_new_seq ON schedules_new(seq);
INSERT INTO schedules_new
    (id, seq, project_id, name, state, cron_expr, timezone, spec, next_fire_at,
     last_task_id, created_at, updated_at)
  SELECT id, seq, project_id, name, state, cron_expr, timezone, spec, next_fire_at,
         last_task_id, created_at, updated_at
  FROM schedules;
DROP TABLE schedules;
ALTER TABLE schedules_new RENAME TO schedules;
DROP INDEX idx_schedules_new_seq;
CREATE UNIQUE INDEX idx_schedules_seq ON schedules(seq);
CREATE INDEX idx_schedules_project ON schedules (project_id, seq);
CREATE UNIQUE INDEX idx_schedules_name ON schedules (project_id, name) WHERE name != '' AND state != 'deleted';

-- databases：project_id → projects(id)。
CREATE TABLE databases_new (
  id                     TEXT PRIMARY KEY,
  seq                    INTEGER NOT NULL,
  project_id             TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
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
     last_backup_at, restore_from_backup, restore_error, version, settings, cpu_millis,
     memory_mb, restore_state)
  SELECT id, seq, project_id, name, engine, credentials_ref, generation, spec_fingerprint,
         backup_interval_secs, backup_retention_secs, status, created_at, updated_at, deleted_at,
         last_backup_at, restore_from_backup, restore_error, version, settings, cpu_millis,
         memory_mb, restore_state
  FROM databases;
DROP TABLE databases;
ALTER TABLE databases_new RENAME TO databases;
DROP INDEX idx_databases_new_seq;
CREATE UNIQUE INDEX idx_databases_seq ON databases(seq);
CREATE UNIQUE INDEX idx_databases_project_name ON databases(project_id, name) WHERE deleted_at = '';

-- backups：project_id → projects(id)；subject_id 多态（database/pubsub/
-- bucketstore），不设外键。
CREATE TABLE backups_new (
  id             TEXT PRIMARY KEY,
  seq            INTEGER NOT NULL,
  project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  subject_id     TEXT NOT NULL,
  engine         TEXT NOT NULL,
  object_key     TEXT NOT NULL DEFAULT '',
  digest         TEXT NOT NULL DEFAULT '',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  retention_secs INTEGER NOT NULL DEFAULT 604800,
  status         TEXT NOT NULL DEFAULT 'pending',
  error          TEXT NOT NULL DEFAULT '',
  started_at     TEXT NOT NULL DEFAULT '',
  finished_at    TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  engine_version TEXT NOT NULL DEFAULT '',
  kind           TEXT NOT NULL DEFAULT 'database'
);
CREATE UNIQUE INDEX idx_backups_new_seq ON backups_new(seq);
INSERT INTO backups_new
    (id, seq, project_id, subject_id, engine, object_key, digest, size_bytes,
     retention_secs, status, error, started_at, finished_at, created_at,
     engine_version, kind)
  SELECT id, seq, project_id, subject_id, engine, object_key, digest, size_bytes,
         retention_secs, status, error, started_at, finished_at, created_at,
         engine_version, kind
  FROM backups;
DROP TABLE backups;
ALTER TABLE backups_new RENAME TO backups;
DROP INDEX idx_backups_new_seq;
CREATE UNIQUE INDEX idx_backups_seq ON backups(seq);
CREATE INDEX idx_backups_subject ON backups(subject_id, seq);

-- alert_rules：app_id → apps(id)。
CREATE TABLE alert_rules_new (
  id               TEXT PRIMARY KEY,
  seq              INTEGER NOT NULL,
  app_id           TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  metric           TEXT NOT NULL,
  threshold        REAL NOT NULL,
  for_secs         INTEGER NOT NULL DEFAULT 0,
  enabled          INTEGER NOT NULL DEFAULT 1,
  state            TEXT NOT NULL DEFAULT 'ok',
  state_since      TEXT NOT NULL DEFAULT '',
  last_value       REAL NOT NULL DEFAULT 0,
  last_observed_at TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  pending_since    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_alert_rules_new_seq ON alert_rules_new(seq);
INSERT INTO alert_rules_new
    (id, seq, app_id, metric, threshold, for_secs, enabled, state, state_since,
     last_value, last_observed_at, created_at, updated_at, pending_since)
  SELECT id, seq, app_id, metric, threshold, for_secs, enabled, state, state_since,
         last_value, last_observed_at, created_at, updated_at, pending_since
  FROM alert_rules;
DROP TABLE alert_rules;
ALTER TABLE alert_rules_new RENAME TO alert_rules;
DROP INDEX idx_alert_rules_new_seq;
CREATE UNIQUE INDEX idx_alert_rules_seq ON alert_rules(seq);
CREATE INDEX idx_alert_rules_app ON alert_rules(app_id);

-- shared_variables：project_id → projects(id)。
CREATE TABLE shared_variables_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  value      TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_shared_variables_new_seq ON shared_variables_new(seq);
INSERT INTO shared_variables_new
    (id, seq, project_id, name, value, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, value, created_at, updated_at, deleted_at
  FROM shared_variables;
DROP TABLE shared_variables;
ALTER TABLE shared_variables_new RENAME TO shared_variables;
DROP INDEX idx_shared_variables_new_seq;
CREATE UNIQUE INDEX idx_shared_variables_seq ON shared_variables(seq);
CREATE UNIQUE INDEX idx_shared_variables_project_name ON shared_variables(project_id, name) WHERE deleted_at = '';

-- app_variables：app_id → apps(id)。
CREATE TABLE app_variables_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  app_id     TEXT NOT NULL REFERENCES apps(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  value      TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_app_variables_new_seq ON app_variables_new(seq);
INSERT INTO app_variables_new
    (id, seq, app_id, name, value, created_at, updated_at, deleted_at)
  SELECT id, seq, app_id, name, value, created_at, updated_at, deleted_at
  FROM app_variables;
DROP TABLE app_variables;
ALTER TABLE app_variables_new RENAME TO app_variables;
DROP INDEX idx_app_variables_new_seq;
CREATE UNIQUE INDEX idx_app_variables_seq ON app_variables(seq);
CREATE UNIQUE INDEX idx_app_variables_app_name ON app_variables(app_id, name) WHERE deleted_at = '';

-- browse_sessions：project_id → projects(id)；database_id → databases(id)。
CREATE TABLE browse_sessions_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  database_id TEXT NOT NULL REFERENCES databases(id) ON DELETE RESTRICT,
  engine      TEXT NOT NULL,
  read_only   INTEGER NOT NULL,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_browse_sessions_new_seq ON browse_sessions_new(seq);
INSERT INTO browse_sessions_new
    (id, seq, project_id, database_id, engine, read_only, created_at, expires_at)
  SELECT id, seq, project_id, database_id, engine, read_only, created_at, expires_at
  FROM browse_sessions;
DROP TABLE browse_sessions;
ALTER TABLE browse_sessions_new RENAME TO browse_sessions;
DROP INDEX idx_browse_sessions_new_seq;
CREATE UNIQUE INDEX idx_browse_sessions_seq ON browse_sessions(seq);
CREATE INDEX idx_browse_sessions_database ON browse_sessions (database_id);
CREATE INDEX idx_browse_sessions_project ON browse_sessions (project_id);

-- pubsubs：project_id → projects(id)。
CREATE TABLE pubsubs_new (
  id                    TEXT PRIMARY KEY,
  seq                   INTEGER NOT NULL,
  project_id            TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name                  TEXT NOT NULL,
  engine                TEXT NOT NULL,
  version               TEXT NOT NULL,
  settings              TEXT NOT NULL DEFAULT '{}',
  cpu_millis            INTEGER,
  memory_mb             INTEGER,
  credentials_ref       TEXT NOT NULL,
  generation            INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint      TEXT NOT NULL DEFAULT '',
  backup_interval_secs   INTEGER NOT NULL DEFAULT 86400,
  backup_retention_secs INTEGER NOT NULL DEFAULT 604800,
  status                TEXT NOT NULL DEFAULT 'pending',
  last_backup_at        TEXT NOT NULL DEFAULT '',
  restore_from_backup   TEXT NOT NULL DEFAULT '',
  restore_error         TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  deleted_at            TEXT NOT NULL DEFAULT '',
  restore_state         TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_pubsubs_new_seq ON pubsubs_new(seq);
INSERT INTO pubsubs_new
    (id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb,
     credentials_ref, generation, spec_fingerprint, backup_interval_secs,
     backup_retention_secs, status, last_backup_at, restore_from_backup,
     restore_error, created_at, updated_at, deleted_at, restore_state)
  SELECT id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb,
         credentials_ref, generation, spec_fingerprint, backup_interval_secs,
         backup_retention_secs, status, last_backup_at, restore_from_backup,
         restore_error, created_at, updated_at, deleted_at, restore_state
  FROM pubsubs;
DROP TABLE pubsubs;
ALTER TABLE pubsubs_new RENAME TO pubsubs;
DROP INDEX idx_pubsubs_new_seq;
CREATE UNIQUE INDEX idx_pubsubs_seq ON pubsubs(seq);
CREATE UNIQUE INDEX idx_pubsubs_project_name ON pubsubs(project_id, name) WHERE deleted_at = '';

-- bucket_stores：project_id → projects(id)。
CREATE TABLE bucket_stores_new (
  id                    TEXT PRIMARY KEY,
  seq                   INTEGER NOT NULL,
  project_id            TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  name                  TEXT NOT NULL,
  engine                TEXT NOT NULL,
  version               TEXT NOT NULL,
  settings              TEXT NOT NULL DEFAULT '{}',
  cpu_millis            INTEGER,
  memory_mb             INTEGER,
  credentials_ref       TEXT NOT NULL,
  generation            INTEGER NOT NULL DEFAULT 0,
  spec_fingerprint      TEXT NOT NULL DEFAULT '',
  backup_interval_secs   INTEGER NOT NULL DEFAULT 86400,
  backup_retention_secs INTEGER NOT NULL DEFAULT 604800,
  status                TEXT NOT NULL DEFAULT 'pending',
  last_backup_at        TEXT NOT NULL DEFAULT '',
  restore_from_backup   TEXT NOT NULL DEFAULT '',
  restore_error         TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  deleted_at            TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_bucket_stores_new_seq ON bucket_stores_new(seq);
INSERT INTO bucket_stores_new
    (id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb,
     credentials_ref, generation, spec_fingerprint, backup_interval_secs,
     backup_retention_secs, status, last_backup_at, restore_from_backup,
     restore_error, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, name, engine, version, settings, cpu_millis, memory_mb,
         credentials_ref, generation, spec_fingerprint, backup_interval_secs,
         backup_retention_secs, status, last_backup_at, restore_from_backup,
         restore_error, created_at, updated_at, deleted_at
  FROM bucket_stores;
DROP TABLE bucket_stores;
ALTER TABLE bucket_stores_new RENAME TO bucket_stores;
DROP INDEX idx_bucket_stores_new_seq;
CREATE UNIQUE INDEX idx_bucket_stores_seq ON bucket_stores(seq);
CREATE UNIQUE INDEX idx_bucket_stores_project_name ON bucket_stores(project_id, name) WHERE deleted_at = '';

-- topics：pubsub_id → pubsubs(id)。
CREATE TABLE topics_new (
  id           TEXT PRIMARY KEY,
  seq          INTEGER NOT NULL,
  project_id   TEXT NOT NULL,
  pubsub_id    TEXT NOT NULL REFERENCES pubsubs(id) ON DELETE RESTRICT,
  name         TEXT NOT NULL,
  partitions   INTEGER NOT NULL,
  retention_ms INTEGER,
  status       TEXT NOT NULL DEFAULT 'pending',
  status_error TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  deleted_at   TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_topics_new_seq ON topics_new(seq);
INSERT INTO topics_new
    (id, seq, project_id, pubsub_id, name, partitions, retention_ms, status,
     status_error, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, pubsub_id, name, partitions, retention_ms, status,
         status_error, created_at, updated_at, deleted_at
  FROM topics;
DROP TABLE topics;
ALTER TABLE topics_new RENAME TO topics;
DROP INDEX idx_topics_new_seq;
CREATE UNIQUE INDEX idx_topics_seq ON topics(seq);
CREATE UNIQUE INDEX idx_topics_pubsub_name ON topics(pubsub_id, name) WHERE deleted_at = '';
CREATE INDEX idx_topics_pubsub ON topics(pubsub_id);

-- streams：pubsub_id → pubsubs(id)。
CREATE TABLE streams_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL,
  pubsub_id   TEXT NOT NULL REFERENCES pubsubs(id) ON DELETE RESTRICT,
  name        TEXT NOT NULL,
  subjects    TEXT NOT NULL DEFAULT '[]',
  retention   TEXT NOT NULL DEFAULT 'limits',
  storage     TEXT NOT NULL DEFAULT 'file',
  max_bytes   INTEGER,
  status      TEXT NOT NULL DEFAULT 'pending',
  status_err  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_streams_new_seq ON streams_new(seq);
INSERT INTO streams_new
    (id, seq, project_id, pubsub_id, name, subjects, retention, storage, max_bytes,
     status, status_err, created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, pubsub_id, name, subjects, retention, storage, max_bytes,
         status, status_err, created_at, updated_at, deleted_at
  FROM streams;
DROP TABLE streams;
ALTER TABLE streams_new RENAME TO streams;
DROP INDEX idx_streams_new_seq;
CREATE UNIQUE INDEX idx_streams_seq ON streams(seq);
CREATE UNIQUE INDEX idx_streams_pubsub_name ON streams(pubsub_id, name) WHERE deleted_at = '';
CREATE INDEX idx_streams_pubsub ON streams(pubsub_id);

-- bucket_resources：bucket_store_id → bucket_stores(id)。
CREATE TABLE bucket_resources_new (
  id              TEXT PRIMARY KEY,
  seq             INTEGER NOT NULL,
  project_id      TEXT NOT NULL,
  bucket_store_id TEXT NOT NULL REFERENCES bucket_stores(id) ON DELETE RESTRICT,
  name            TEXT NOT NULL,
  quota_bytes     INTEGER,
  status          TEXT NOT NULL DEFAULT 'pending',
  status_err      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  deleted_at      TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_bucket_resources_new_seq ON bucket_resources_new(seq);
INSERT INTO bucket_resources_new
    (id, seq, project_id, bucket_store_id, name, quota_bytes, status, status_err,
     created_at, updated_at, deleted_at)
  SELECT id, seq, project_id, bucket_store_id, name, quota_bytes, status, status_err,
         created_at, updated_at, deleted_at
  FROM bucket_resources;
DROP TABLE bucket_resources;
ALTER TABLE bucket_resources_new RENAME TO bucket_resources;
DROP INDEX idx_bucket_resources_new_seq;
CREATE UNIQUE INDEX idx_bucket_resources_seq ON bucket_resources(seq);
CREATE UNIQUE INDEX idx_bucket_resources_store_name ON bucket_resources(bucket_store_id, name) WHERE deleted_at = '';
CREATE INDEX idx_bucket_resources_store ON bucket_resources(bucket_store_id);

-- source_uploads：project_id → projects(id)。
CREATE TABLE source_uploads_new (
  id         TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  digest     TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_source_uploads_new_seq ON source_uploads_new(seq);
INSERT INTO source_uploads_new (id, seq, project_id, digest, size_bytes, created_at)
  SELECT id, seq, project_id, digest, size_bytes, created_at FROM source_uploads;
DROP TABLE source_uploads;
ALTER TABLE source_uploads_new RENAME TO source_uploads;
DROP INDEX idx_source_uploads_new_seq;
CREATE UNIQUE INDEX idx_source_uploads_seq ON source_uploads(seq);
CREATE UNIQUE INDEX idx_source_uploads_project_digest ON source_uploads(project_id, digest);
CREATE INDEX idx_source_uploads_project_list ON source_uploads(project_id, seq);

-- 全库外键复核（复制期 foreign_keys=OFF，插入不受约束）：任一孤儿行即
-- CHECK 失败，事务回滚，迁移 fail-closed。guard 表是迁移局部临时面。
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
