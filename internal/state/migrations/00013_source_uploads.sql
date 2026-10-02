-- +goose Up
-- 上传产物行（ADR-0019 附录 A，F1.10）：Upload 是 project 级构建材料。
-- (project_id, digest) 唯一——同项目同内容重传返回同一行；blob 全局内容
-- 寻址（DataRoot/uploads/<sha256hex>），跨项目行共享同一 blob（refcount=
-- 行数，末行删除时 blob 一并删除）。
CREATE TABLE source_uploads (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  digest TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_source_uploads_project_digest ON source_uploads(project_id, digest);
CREATE INDEX idx_source_uploads_project_list ON source_uploads(project_id, id);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
