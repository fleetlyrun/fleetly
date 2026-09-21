-- cron_runs 运行台账（E5 Cron，object-storage 设计 §8 契约面补记，迁移
-- 00013，只加法纪律）：每 schedule 一次触发一行的留存面（架构 §4.3 留存
-- 行：每 schedule 最近 20 条，保留窗清理由 janitor 增项承载）。
--
-- 列口径（§8 列清单）：
--   id           ULID 主键；
--   app_id/service  归属应用与 compose 服务（FK 对齐 builds 形态）；
--   expression   触发时的五段标准表达式（触发面如实抄录——schedule 变更后
--                历史行的表达式不回写）；
--   scheduled_at 命中的 cron 点（UnixNano；手动触发 = 触发时刻）；
--   started_at   job 服务创建成功时刻（UnixNano；skipped 行为空）；
--   finished_at  终态收口时刻（UnixNano；started 在途行与 skipped 行为空）；
--   status       started（在途）| succeeded | failed | timeout | skipped；
--                §8 词表列的是终态值，started 是在途位（完成检测与启动残
--                留收口的扫描谓词，设计 §8「调度核抽取」的在途 run 语义）；
--   skip_reason  skipped 行的原因：overlap | node_unavailable |
--                missed_during_downtime | interrupted；
--   job_service  一次性 Swarm job 服务名（fleetly-cron-<app>-<svc>-<ulid8>；
--                触发链残留收口与完成检测按行↔服务对账）；
--   error        失败/超时原因摘要（任务 Err 单行化；成功/skipped 为空）。
--
-- 词表不加 CHECK（00001 词表先例：状态词表由 state 包常量承载）；时间列
-- 沿用 UnixNano 整数仓内约定。Down 仅供 goose 演练；生产回滚 = 恢复快照。

-- +goose Up
CREATE TABLE cron_runs (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL REFERENCES apps (id),
    service      TEXT NOT NULL,
    expression   TEXT NOT NULL,
    scheduled_at INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER,
    status       TEXT NOT NULL,
    skip_reason  TEXT,
    job_service  TEXT,
    error        TEXT
);
CREATE INDEX idx_cron_runs_app_sched ON cron_runs (app_id, service, scheduled_at);
CREATE INDEX idx_cron_runs_status ON cron_runs (status);

-- +goose Down
DROP TABLE cron_runs;
