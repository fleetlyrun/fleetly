-- +goose Up
-- 材料与网络/卷（F0.14/16/17/18，ADR-0014）：
--   - secrets：age 信封密文（KEK 在数据根 keys/，轮换 runbook 注释见
--     internal/material）；值永不回显，fingerprint 是 sha256 前 16 hex。
--   - configs：版本化明文挂载文件（可回读、有配额——配额执法随 API 面）。
--   - volumes：卷行；pinned_node_id 是钉住锚（平台节点 ID，首次挂载分配）。
--   - networks：Project 级网络；egress_none 声明（swarm v1 弱隔离明示）。
CREATE TABLE secrets (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  ciphertext  BLOB NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT
);
CREATE UNIQUE INDEX idx_secrets_project_name ON secrets(project_id, name) WHERE deleted_at = '';

CREATE TABLE configs (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name       TEXT NOT NULL,
  version    INTEGER NOT NULL,
  content    BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(project_id, name, version)
);

CREATE TABLE volumes (
  id             TEXT PRIMARY KEY,
  project_id     TEXT NOT NULL,
  name           TEXT NOT NULL,
  pinned_node_id TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  deleted_at     TEXT
);
CREATE UNIQUE INDEX idx_volumes_project_name ON volumes(project_id, name) WHERE deleted_at = '';

CREATE TABLE networks (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  egress_none INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL,
  deleted_at  TEXT
);
CREATE UNIQUE INDEX idx_networks_project_name ON networks(project_id, name) WHERE deleted_at = '';

-- +goose Down
-- goose 只前滚（架构 §8）。
