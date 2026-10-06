-- +goose Up
-- Browse 会话回收台账（F3.6，ADR-0051 决策 1）：browse 载体是外部活体
-- （容器），daemon 重启后会话注册表丢失而载体仍在跑——行是重启后重注册
-- 与到点回收的锚（Task run 行持久化的同一理由）。行不承载凭证（方言渲染
-- 在 Ensure 期从 Secret 单真源重新解封——databaseLoop 同口径）；回收即删
-- 行（非 tombstone：会话非资源行，无软删语义）。
CREATE TABLE browse_sessions (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL,
  database_id TEXT NOT NULL,
  engine      TEXT NOT NULL,
  read_only   INTEGER NOT NULL,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);
CREATE INDEX idx_browse_sessions_database ON browse_sessions (database_id);
CREATE INDEX idx_browse_sessions_project ON browse_sessions (project_id);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
