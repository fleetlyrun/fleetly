-- +goose Up
-- 幂等记录键空间收窄到调用方主体作用域（H11 / R14-H4）：00007 的
-- idem_key 全局主键使键空间无调用方维度——两队用同键同体调用创建型 RPC
-- 时，后到者命中先到者的完成态行并原样取回其响应（CreateToken /
-- SetGitHook / CreateInvitation 的响应含明文 secret）。作用域 = 调用方
-- 主体（application/idem 的 callerScope：token:<TokenID> 优先，其次
-- user:<UserID>，无身份 anonymous），同键同体只在同一主体内重放，跨
-- Token/跨 Team 互不可见。
--
-- SQLite 不能改主键，重建表（PRIMARY KEY (scope, idem_key)）。存量行落
-- scope=''：'' 不是任何调用方的作用域（无身份面是 'anonymous'，有身份
-- 面是 token:/user: 前缀），这些行对全部读写路径不可见，只能等保留窗到
-- 期由 janitor Sweep 清理——不迁到任何真实主体即不产生跨主体重放面。
-- pre-release 无兼容负担，保留行是为避免不必要的丢失（升级窗口内仍在
-- 24h 保留窗的行可供排查）。
CREATE TABLE idempotency_records_v2 (
    scope          TEXT NOT NULL,
    idem_key       TEXT NOT NULL,
    method         TEXT NOT NULL,
    fingerprint    TEXT NOT NULL,
    state          TEXT NOT NULL,            -- inflight | completed
    response_type  TEXT NOT NULL DEFAULT '', -- completed：响应消息 proto 全名（重放构造）
    response_body  BLOB,                     -- completed：响应消息确定性序列化
    created_at     TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    PRIMARY KEY (scope, idem_key)
);
INSERT INTO idempotency_records_v2
    (scope, idem_key, method, fingerprint, state, response_type, response_body, created_at, expires_at)
    SELECT '', idem_key, method, fingerprint, state, response_type, response_body, created_at, expires_at
    FROM idempotency_records;
DROP TABLE idempotency_records;
ALTER TABLE idempotency_records_v2 RENAME TO idempotency_records;
CREATE INDEX idx_idempotency_expires ON idempotency_records (expires_at);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
