-- E6 通知 Webhook（observability 设计 §5.1/§5.2，W5-S4，迁移 00017，只加法
-- 纪律）：webhook_endpoints（订阅端点权威态）/ webhook_deliveries（投递台账
-- ——pending|ok|failed 三态 + 重试簿记）/ webhook_state（投递器单行状态——
-- 事件消费游标）。
--
-- 列口径（设计 §5.1/§5.2 清单）：
--   webhook_endpoints.id        ULID 主键；
--   name                        人读端点名 UNIQUE（CLI/Console 以名定位）；
--   url                         接收端 URL（scheme http/https 校验在 state
--                               写入通道；单操作员信任模型、无 SSRF 过滤
--                               ——设计 §5.1 诚实口径）；
--   secret_cipher               age 密文（envelope，HMAC 签名密钥；明文
--                               只在创建/轮换响应一次性返回，绝不落库/
--                               进日志/事件/审计——state-model §2.9）；
--   secret_fingerprint          明文 sha256 前 8 hex（读侧指纹，app_secrets
--                               hash8 同口径——只判「是不是那个 secret」，
--                               不回传材料）；
--   event_patterns              JSON 字符串数组（事件名 glob 订阅集，如
--                               ["deployment.*"]；字符白名单 + `*` 通配，
--                               匹配器侧再转义——防注入双保险）；
--   enabled                     订阅开关（0/1；停用端点暂停投递不删台账）；
--   created_at/updated_at       UnixNano（仓内时间列约定）。
--
--   webhook_deliveries.id       ULID 主键；
--   event_seq                   消费的事件 seq（裸 INTEGER——events 表按
--                               保留期清理，台账 7d 窗更短，不建 FK 以免
--                               阻碍事件清理）；
--   endpoint_id                 端点 FK（端点删除 = 台账行同事务显式清理
--                               ——业务动作承载级联，db_references 同型）；
--   status                      pending|ok|failed（CHECK 词典 + state 层
--                               写入通道双保险；failed = 终态：3 次尝试
--                               耗尽——设计 §5.2 重试语义）；
--   attempts                    已尝试次数（重启恢复时保留——prompt 口径）；
--   response_code               最近一次 HTTP 响应码（传输失败为 NULL）；
--   last_error                  最近一次失败的单行化摘要（零凭据材料）；
--   next_retry_at               下次重试 UnixNano（NULL = 无待重试——终态
--                               或等待首发）；
--   created_at/updated_at       UnixNano。
--
--   webhook_state.key/value     投递器单行状态（key='last_seq' 消费位）。
--                               游标推进与 pending 行落账同事务（Outbox
--                               消费侧对偶：崩溃either全有or全无）。
--
-- 通知自身零事件（设计 §5.2 红线）：投递失败不发事件——可见面 = 台账 +
-- system status notifications 组件 + Console。events 注册表零新增。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE webhook_endpoints (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    url               TEXT NOT NULL,
    secret_cipher     TEXT NOT NULL,
    secret_fingerprint TEXT NOT NULL,
    event_patterns    TEXT NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE TABLE webhook_deliveries (
    id            TEXT PRIMARY KEY,
    event_seq     INTEGER NOT NULL,
    endpoint_id   TEXT NOT NULL REFERENCES webhook_endpoints (id),
    status        TEXT NOT NULL CHECK (status IN ('pending', 'ok', 'failed')),
    attempts      INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER,
    last_error    TEXT NOT NULL DEFAULT '',
    next_retry_at INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
-- 到期重试扫描（投递器每拍）与端点侧台账读面（Console deliveries 抽屉）。
CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries (status, next_retry_at);
CREATE INDEX idx_webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, created_at);

CREATE TABLE webhook_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE webhook_state;
DROP TABLE webhook_deliveries;
DROP TABLE webhook_endpoints;
