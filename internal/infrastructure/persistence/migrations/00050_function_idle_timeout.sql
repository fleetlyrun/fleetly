-- +goose Up
-- P2-2 通用伸缩决策环（设计 v2 §2/§1.3）：functions.idle_timeout_secs 容量
-- 行旋钮——缩零判定与缩容防抖的 per-Function 窗（0 = 平台缺省 300s；
-- 「容量 = 行面旋钮」平面律 ADR-0102 收编条款，行面可调口径）。
ALTER TABLE functions ADD COLUMN idle_timeout_secs INTEGER NOT NULL DEFAULT 0;
