-- +goose Up
-- Route 聚合（Edge 上下文实体；F0.15）。软删 tombstone；同 Project 内
-- host+path 活跃唯一。
CREATE TABLE routes (
  id         TEXT PRIMARY KEY,
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
  deleted_at TEXT
);
CREATE UNIQUE INDEX idx_routes_project_host_path ON routes(project_id, host, path) WHERE deleted_at = '';
CREATE INDEX idx_routes_app ON routes(app_id);

-- +goose Down
-- goose 只前滚（架构 §8）。
