-- +goose Up
-- Schedule（F1.7，ADR-0018 时区 cron）：周期触发规则，到期拍从冻结
-- TaskSpec 模板铸一条 one-shot Task（Run 机制复用 F1.5：补足/观测/TTL
-- 全走 taskStep 既有链）。next_fire_at 是绝对时刻（RFC3339 UTC 落库，
-- 控制面重启后按墙钟续算，不依赖进程内计时器——ADR-0018 同款口径；
-- 重启错过窗口补跑一拍，见 ADR-0018 附录 A）。last_task_id 指向最近一拍
-- 铸出的 Task（重叠 skip 判定锚：上一拍 Run 未终态即跳过本拍）。行
-- tombstone 化（deleted），活跃名唯一。
CREATE TABLE schedules (
    id           TEXT PRIMARY KEY,      -- ULID（创建序 = 游标轴）
    project_id   TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL,         -- active | deleted
    cron_expr    TEXT NOT NULL,         -- 5 字段表达式（无 TZ 前缀；时区随行存）
    timezone     TEXT NOT NULL,         -- IANA 时区名（ADR-0018：随 Schedule 持久化，DST 按墙钟解释）
    spec         BLOB NOT NULL,         -- 冻结 TaskSpec 模板（protojson；task ref 空——fire 时铸新 Task）
    next_fire_at TEXT NOT NULL DEFAULT '',  -- RFC3339 绝对时刻（空 = 不再触发）
    last_task_id TEXT NOT NULL DEFAULT '',  -- 最近一拍铸出的 Task（重叠判定锚）
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX idx_schedules_project ON schedules (project_id, id);
CREATE UNIQUE INDEX idx_schedules_name ON schedules (project_id, name) WHERE name != '' AND state != 'deleted';

-- +goose Down
DROP TABLE schedules;
