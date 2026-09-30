-- +goose Up
-- 核心域表（随 state 批次落地；routes/networks/volumes/secrets/configs 等
-- 表随各自功能批次增量迁移——goose 只加法，每张表落地即有使用方）。
-- 约定：主键一律 ULID（时间有序，兼列表排序键）；时间戳一律 UTC RFC3339
-- 秒精度文本（ADR-0018）；结构资源软删（deleted_at tombstone，四件一拍
-- 同事务落），部署类记录永不删。

CREATE TABLE projects (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  team_id    TEXT NOT NULL DEFAULT 'default',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE UNIQUE INDEX idx_projects_name ON projects(name) WHERE deleted_at = '';

CREATE TABLE apps (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE UNIQUE INDEX idx_apps_project_name ON apps(project_id, name) WHERE deleted_at = '';

-- Revision 冻结体：spec 为 AppSpec 的 protojson 规范序列化（blob 落库
-- 裁决，2026-09-30）；digest 是该序列化的 sha256（幂等去重锚）。
CREATE TABLE revisions (
  id         TEXT PRIMARY KEY,
  app_id     TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  digest     TEXT NOT NULL,
  spec       BLOB NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(app_id, seq)
);
-- 同 App 同内容只冻结一份（内容寻址复用；跨 App 天然异 digest——spec 含 app.id）。
CREATE UNIQUE INDEX idx_revisions_app_digest ON revisions(app_id, digest);

-- Deployment 状态机行（含 queued：admission 持久真源——库为真源+进程内
-- 索引裁决，2026-09-30，ADR-0016）。generation 是已下发 Spec 的单调编号
-- （幂等与 Drift 判定的锚）。observe_deadline 是 L3 观察窗绝对截止
-- （ADR-0018 绝对 deadline 落库）。
CREATE TABLE deployments (
  id               TEXT PRIMARY KEY,
  app_id           TEXT NOT NULL,
  from_revision    TEXT NOT NULL DEFAULT '',
  to_revision      TEXT NOT NULL,
  state            TEXT NOT NULL,
  generation       INTEGER NOT NULL DEFAULT 0,
  idempotency_key  TEXT NOT NULL DEFAULT '',
  commit_sha       TEXT NOT NULL DEFAULT '',
  superseded_by    TEXT NOT NULL DEFAULT '',
  error            TEXT NOT NULL DEFAULT '',
  observe_deadline TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL,
  finished_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_deployments_app ON deployments(app_id);
CREATE INDEX idx_deployments_state ON deployments(state);
-- 幂等键在活跃态（排队/在途/回滚中）唯一：防并发重复入队；完成态同键
-- 重复提交由 admission 查询去重（返回既有或新建，N0 口径）。
CREATE UNIQUE INDEX idx_deployments_idem_active ON deployments(idempotency_key)
  WHERE idempotency_key != ''
    AND state IN ('queued','preparing','building','releasing','observing','rolling-back');

CREATE TABLE builds (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL,
  revision_id TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL,
  digest      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_builds_app ON builds(app_id);
CREATE INDEX idx_builds_state ON builds(state);

-- Outbox：状态迁移事件的单调 seq 队列（架构 §6；消费面 events list/follow）。
CREATE TABLE outbox (
  seq          INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT NOT NULL,
  aggregate    TEXT NOT NULL,
  aggregate_id TEXT NOT NULL,
  payload      TEXT NOT NULL,
  created_at   TEXT NOT NULL
);

-- 审计：一切写操作留痕（操作者/来源枚举 manual|api|cli|webhook|schedule/
-- 前后值指纹；账号批接入完整执法，写路径四件一拍自本批起落行）。
CREATE TABLE audit (
  id         TEXT PRIMARY KEY,
  actor      TEXT NOT NULL DEFAULT '',
  source     TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  resource   TEXT NOT NULL,
  before_fp  TEXT NOT NULL DEFAULT '',
  after_fp   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX idx_audit_created ON audit(created_at);

-- nodes 是观测缓存（非权威——权威归属判定永远查平台表；Provider Watch
-- 流的 node.joined/leave 在此落观测行）。
CREATE TABLE nodes (
  platform_id   TEXT PRIMARY KEY,
  carrier_id    TEXT NOT NULL DEFAULT '',
  hostname      TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT '',
  available     INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）；Down
-- 留空是有意为之，不提供逐表回退。
