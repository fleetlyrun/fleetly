-- fleetly 状态备份台账扩展（T2.22，备份基线与信任闭环）。
--
-- 00001 的 state_backups.kind CHECK 枚举是 ('hot','cold')——占位形态。
-- T2.22 触发面落定三类：daily（每日定时）、pre_upgrade（升级前热备快照，
-- upgrade.sh 编排步骤 ②）、post_deploy（部署成功后异步挂钩），外加手动
-- 触发 manual（fleetly backups create）。SQLite 不能改 CHECK，唯一正路
-- = 重建表换约束（同事务内 建新→搬行→删旧→改名；本表无外键引用者，
-- 无连带）。verify_status 枚举维持 ('pending','verified','failed') 不变。
--
-- 只加法纪律注记：本迁移不改写任何已应用文件，只新增 00008；旧 kind 值
-- ('hot','cold') 保留合法（历史行兼容）。

-- +goose Up
CREATE TABLE state_backups_new (
    id            TEXT PRIMARY KEY,
    created_at    INTEGER NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'daily'
                  CHECK (kind IN ('hot', 'cold', 'daily', 'pre_upgrade', 'post_deploy', 'manual')),
    path          TEXT NOT NULL,
    sha256        TEXT NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    verify_status TEXT NOT NULL DEFAULT 'pending' CHECK (verify_status IN ('pending', 'verified', 'failed')),
    error         TEXT
);
INSERT INTO state_backups_new (id, created_at, kind, path, sha256, size_bytes, verify_status, error)
    SELECT id, created_at, kind, path, sha256, size_bytes, verify_status, error FROM state_backups;
DROP TABLE state_backups;
ALTER TABLE state_backups_new RENAME TO state_backups;
CREATE INDEX idx_state_backups_created ON state_backups (created_at);
