-- +goose Up
-- 阈值告警面（F2.5，ADR-0041 决策 3/4）：alert_rules 是 per-App 阈值规则
-- （评估在 engine 采集遍内原生完成——状态机 ok|firing 落行，last_value 是
-- 最近一次评估观测值）；notification_channels 是通知通道（配置 age 信封
-- 入库——URL/bot_token 是凭证材料，ADR-0014；last_failure 是派发诊断面）。
-- 系统内置规则 platform-offsite-backup 不落行（代码内评估，ADR-0041）。
CREATE TABLE alert_rules (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL,
  metric      TEXT NOT NULL,            -- cpu_percent | memory_working_set_bytes
  threshold   REAL NOT NULL,
  for_secs    INTEGER NOT NULL DEFAULT 0,
  enabled     INTEGER NOT NULL DEFAULT 1,
  state       TEXT NOT NULL DEFAULT 'ok',  -- ok | firing
  state_since TEXT NOT NULL DEFAULT '',
  last_value  REAL NOT NULL DEFAULT 0,
  last_observed_at TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_alert_rules_app ON alert_rules(app_id);

CREATE TABLE notification_channels (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL,            -- webhook | telegram
  config_ciphertext BLOB NOT NULL,      -- age 信封：{"url"} 或 {"bot_token","chat_id"}
  enabled     INTEGER NOT NULL DEFAULT 1,
  last_failure TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
