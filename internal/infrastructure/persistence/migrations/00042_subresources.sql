-- +goose Up
-- B8（ADR-0096 决策 10 / design §4.7）：三族子资源表——Topic（kafka 协议
-- 族）/Stream（nats）/Bucket（bucket store）。声明行 + admin 工具容器收敛
-- 的存储面：行即期望态（收敛段对照实况 ensure），软删 tombstone = 声明式
-- GC 挂起（收敛段删实况后硬删行）。version 无关（方言按父实例 engine 分
-- 派）；status 三值 pending|ready|error 由收敛段推进。前向迁移（非
-- flag-day：新表新行，存量零触，design §7.6——goose 只前滚）。
CREATE TABLE topics (
  id           TEXT PRIMARY KEY,
  seq          INTEGER NOT NULL,
  project_id   TEXT NOT NULL,
  pubsub_id    TEXT NOT NULL,
  name         TEXT NOT NULL,
  -- partitions 期望分区数（引擎约束可增不可减——方言层校验）。
  partitions   INTEGER NOT NULL,
  -- retention_ms 期望 retention.ms（NULL = 不管理，引擎缺省）。
  retention_ms INTEGER,
  status       TEXT NOT NULL DEFAULT 'pending',
  status_error TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  deleted_at   TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_topics_seq ON topics(seq);
CREATE UNIQUE INDEX idx_topics_pubsub_name ON topics(pubsub_id, name) WHERE deleted_at = '';
CREATE INDEX idx_topics_pubsub ON topics(pubsub_id);

CREATE TABLE streams (
  id          TEXT PRIMARY KEY,
  seq         INTEGER NOT NULL,
  project_id  TEXT NOT NULL,
  pubsub_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  -- subjects 是 nats 主题过滤器 JSON 数组（升序稳定）。
  subjects    TEXT NOT NULL DEFAULT '[]',
  -- retention: limits | interest | workqueue（创建后不可变）。
  retention   TEXT NOT NULL DEFAULT 'limits',
  -- storage: file | memory（创建后不可变）。
  storage     TEXT NOT NULL DEFAULT 'file',
  -- max_bytes 期望 max_stream_size（NULL = 无上限；可变面）。
  max_bytes   INTEGER,
  status      TEXT NOT NULL DEFAULT 'pending',
  status_err  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  deleted_at  TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_streams_seq ON streams(seq);
CREATE UNIQUE INDEX idx_streams_pubsub_name ON streams(pubsub_id, name) WHERE deleted_at = '';
CREATE INDEX idx_streams_pubsub ON streams(pubsub_id);

CREATE TABLE bucket_resources (
  id              TEXT PRIMARY KEY,
  seq             INTEGER NOT NULL,
  project_id      TEXT NOT NULL,
  bucket_store_id TEXT NOT NULL,
  name            TEXT NOT NULL,
  -- quota_bytes 期望 hard quota（NULL = 无配额；live 定案：silo mcli
  -- quota set --size 在册）。
  quota_bytes     INTEGER,
  status          TEXT NOT NULL DEFAULT 'pending',
  status_err      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  deleted_at      TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_bucket_resources_seq ON bucket_resources(seq);
CREATE UNIQUE INDEX idx_bucket_resources_store_name ON bucket_resources(bucket_store_id, name) WHERE deleted_at = '';
CREATE INDEX idx_bucket_resources_store ON bucket_resources(bucket_store_id);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
