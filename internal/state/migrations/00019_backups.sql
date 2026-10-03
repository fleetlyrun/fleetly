-- +goose Up
-- Backup 台账 + Database 行的调度锚与恢复挂起列（F2.2，ADR-0039 决策 6）。
-- 台账行先落（pending）再执行（执行器把产物流写 ObjectStore，成功回填
-- object_key/digest/size）；object_key 形态
-- backups/<projectID>/<databaseID>/<utc-compact-ts>-<backupULID>（无冒号，
-- Windows 控制面纪律）。retention_secs 是创建时刻的行级保留窗快照——
-- 后续改窗不追溯已落备份（删旧备份不可逆，冻结即诚实）。
-- databases.last_backup_at 是定时调度锚（成功才推进；行存续独立于台账
-- 清理）；restore_from_backup 在场 = 恢复挂起（备份环执行；成功清位 +
-- database.restored 事件，失败清挂起 + restore_error——半恢复态重试不可
-- 幂等，诚实留给用户重建）。
CREATE TABLE backups (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL,
  database_id TEXT NOT NULL,
  engine      TEXT NOT NULL,
  object_key  TEXT NOT NULL DEFAULT '',
  digest      TEXT NOT NULL DEFAULT '',
  size_bytes  INTEGER NOT NULL DEFAULT 0,
  retention_secs INTEGER NOT NULL DEFAULT 604800,
  -- status: pending | running | succeeded | failed（执行前 pending →
  -- running；重启打断的 running 由引擎启动清扫落 failed"interrupted"——
  -- 同 Build 崩溃恢复先例，半途产物对象由保留窗终扫收口）。
  status      TEXT NOT NULL DEFAULT 'pending',
  error       TEXT NOT NULL DEFAULT '',
  started_at  TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX idx_backups_database ON backups(database_id, id);

ALTER TABLE databases ADD COLUMN last_backup_at TEXT NOT NULL DEFAULT '';
ALTER TABLE databases ADD COLUMN restore_from_backup TEXT NOT NULL DEFAULT '';
ALTER TABLE databases ADD COLUMN restore_error TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
