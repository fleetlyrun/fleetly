-- +goose Up
-- 通用幂等记录（ADR-0024）：Idempotency-Key 头 → 请求体指纹 → 响应引用，
-- 拦截器级执法的存储面。key 全局唯一（一键一请求语义：跨方法/异体复用
-- 同键 = 409）。expires_at 语义随 state：inflight = 认领 TTL（崩溃自愈）；
-- completed = 24h 重放保留窗。清理走 janitor（Sweep）+ 读路径惰性清理。
CREATE TABLE idempotency_records (
    idem_key       TEXT PRIMARY KEY,
    method         TEXT NOT NULL,
    fingerprint    TEXT NOT NULL,
    state          TEXT NOT NULL,          -- inflight | completed
    response_type  TEXT NOT NULL DEFAULT '', -- completed：响应消息 proto 全名（重放构造）
    response_body  BLOB,                   -- completed：响应消息确定性序列化
    created_at     TEXT NOT NULL,
    expires_at     TEXT NOT NULL
);
CREATE INDEX idx_idempotency_expires ON idempotency_records (expires_at);

-- +goose Down
DROP TABLE idempotency_records;
