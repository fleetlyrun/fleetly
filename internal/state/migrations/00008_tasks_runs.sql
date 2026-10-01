-- +goose Up
-- Task/Run（ADR-0012 双形态 + ADR-0025 契约；F1.5/F1.6）：程序化工作负载
-- 与其一次执行。Run 状态机 pending → running → stopping → stopped | failed，
-- 终态携带停止原因七枚举；TTL 与 Owner Lease 落绝对 deadline（ADR-0018：
-- 控制面重启后按墙钟续算，不依赖进程内计时器）。owner_token_id 是 Token
-- 行引用（非明文——吊销排空经 task 环周期扫 revoked 属主，P1-8 拉式口径）。
CREATE TABLE tasks (
    id                  TEXT PRIMARY KEY,      -- ULID（创建序 = 游标轴）
    project_id          TEXT NOT NULL,
    name                TEXT NOT NULL DEFAULT '',
    form                TEXT NOT NULL,         -- one-shot | resident
    state               TEXT NOT NULL,         -- active | draining | completed | failed | drained | deleted
    spec                BLOB NOT NULL,         -- 冻结 TaskSpec（protojson）
    owner_token_id      TEXT NOT NULL DEFAULT '',
    desired_concurrency INTEGER NOT NULL DEFAULT 1,
    network_group       TEXT NOT NULL DEFAULT '',
    dns_name            TEXT NOT NULL DEFAULT '',    -- per-Task 池级稳定 DNS（engine 铸名）
    lease_deadline      TEXT NOT NULL DEFAULT '',    -- RFC3339 绝对 deadline（空 = 无租约约束）
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    finished_at         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tasks_project ON tasks (project_id, id);
CREATE INDEX idx_tasks_driving ON tasks (state);
CREATE UNIQUE INDEX idx_tasks_name ON tasks (project_id, name) WHERE name != '' AND state != 'deleted';

CREATE TABLE runs (
    id              TEXT PRIMARY KEY,          -- ULID（创建序 = 游标轴）
    task_id         TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    state           TEXT NOT NULL,             -- pending | running | stopping | stopped | failed
    stop_reason     TEXT NOT NULL DEFAULT '',  -- completed|failed|stopped_by_user|ttl_expired|lease_expired|owner_revoked|platform_drained（stopping 起因，终态携带）
    exit_code       INTEGER,                   -- NULL = 未观测
    workload_id     TEXT NOT NULL DEFAULT '',  -- Run 载体 Workload ID（观测对账锚）
    dns_name        TEXT NOT NULL DEFAULT '',  -- per-Run 稳定 DNS（engine 铸名）
    deadline        TEXT NOT NULL DEFAULT '',  -- 双语义绝对 deadline：活跃 = TTL；stopping = 停止收口兜底
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    started_at      TEXT NOT NULL DEFAULT '',
    finished_at     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_runs_task ON runs (task_id, id);
CREATE INDEX idx_runs_driving ON runs (state);
CREATE INDEX idx_runs_deadline ON runs (deadline) WHERE deadline != '';

-- +goose Down
DROP TABLE runs;
DROP TABLE tasks;
