-- +goose Up
-- 采集游标（F2.4，ADR-0040 决策 2）：每活跃隔离域一行，last_ts 只在
-- Ingest 成功后推进——VL 不可达 → 游标冻结 → 恢复后 Since=游标 从 docker
-- json-file 缓冲重放补窗（补窗深度以 docker 日志文件在场为界，诚实边界）。
-- namespace 键 = NamespaceRef.String() 形态（含 task:/db: 域前缀）。
CREATE TABLE log_cursors (
  namespace  TEXT PRIMARY KEY,
  last_ts    TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
