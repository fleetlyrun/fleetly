-- +goose NO TRANSACTION
-- +goose Up
-- P2-3 构建源与就绪门（设计 v2 §3）：两处行面扩面。
--
-- ① functions.pending_revision_id 是意图锚（就绪门机制三件之一）：
-- DeployFunction(upload 源) 事务 = 冻结 revision + 落 pending（不换轨）；
-- function 环推进步见 Build 终态 succeeded → 一事务 SwapCurrent + 清位。
-- 指向行并入 ADR-0101 保留窗 protect 集（漏并 = pending 指 GC 悬空）。
-- 软引用口径同 current_revision_id（聚合终局全清后悬空是设计内终态）。
--
-- ② builds owner 泛化：AppID 之外加 FunctionID + owner kind（Function 构
-- 建行 owner = 函数行；app_id 留空）。拾取前置检按 owner kind 分支——
-- function 构建的「归属活跃」判定 = 函数行 pending 锚指向该 revision。
ALTER TABLE functions ADD COLUMN pending_revision_id TEXT NOT NULL DEFAULT '';

-- builds 重建（00047 先例——SQLite 不能 ALTER 列约束）：app_id 可空化
--（function 构建行 app_id 无归属——NULL 跳过 FK 检查是 SQLite 语义）+
-- owner kind/function id 两列；存量行 app_id 非空原样保持。
-- 孤儿行处置与 00047 同款：复制期 foreign_keys 关闭，末尾全库复核 fail-closed。
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
CREATE TABLE builds_new (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  -- app_id 可空（owner kind = function 时 NULL——两类 owner 互斥；
  -- SQLite FK 对 NULL 跳过检查）。
  app_id      TEXT REFERENCES apps(id) ON DELETE RESTRICT,
  function_id TEXT NOT NULL DEFAULT '',
  owner_kind  TEXT NOT NULL DEFAULT 'app',
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
    (id, seq, app_id, function_id, owner_kind, revision_id, state, digest, error, created_at, updated_at, finished_at, repo)
  SELECT id, seq, app_id, '', 'app', revision_id, state, digest, error, created_at, updated_at, finished_at, repo
  FROM builds;
DROP TABLE builds;
ALTER TABLE builds_new RENAME TO builds;
DROP INDEX idx_builds_new_seq;
CREATE UNIQUE INDEX idx_builds_seq ON builds(seq);
CREATE INDEX idx_builds_app ON builds(app_id);
CREATE INDEX idx_builds_state ON builds(state);
CREATE INDEX idx_builds_function ON builds(function_id);
COMMIT;
PRAGMA foreign_keys = ON;
