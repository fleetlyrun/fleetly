-- +goose Up
-- 审计游标索引 + 单调 seq 计数器（R05-M1 修复）。
--
-- 1) 审计读面游标：ListAudit 新→旧按 seq 翻页（before_seq），Team 轴过滤
--    与游标同条件（team_id 等值 + seq 范围）——单索引承载，避免排序后回表
--    过滤。既有 idx_audit_seq 保留（全局 seq 唯一/排序面）。
CREATE INDEX idx_audit_team_seq ON audit(team_id, seq);

-- 2) seq_counters 是每表创建序的单调真源：next = 下一可分配序号。
--    铸造 = UPDATE ... SET next = next + 1 RETURNING next - 1（单语句原子，
--    非事务执行面同样安全）；计数器只增，硬删行不回收序号（ADR-0070
--    never-reuse 承诺）。回填取 COALESCE(MAX(seq),0)+1：存量表从当前最大
--    序号之后继续，空表从 1 起。
--
--    只收“表级全局 seq”聚合；revisions 是 per-App 修订号（UNIQUE(app_id,
--    seq)），outbox 是 AUTOINCREMENT 事件队号，均不在此列。后续迁移若
--    新增带全局 seq 的表或直插带 seq 的存量行，必须同批插入/推进本表
--    计数器（否则 NextSeq 从 1 起重号，由 UNIQUE(seq) fail-closed）。
CREATE TABLE seq_counters (
  name TEXT PRIMARY KEY,
  next INTEGER NOT NULL
);

INSERT INTO seq_counters (name, next) VALUES
  ('projects',              (SELECT COALESCE(MAX(seq), 0) + 1 FROM projects)),
  ('apps',                  (SELECT COALESCE(MAX(seq), 0) + 1 FROM apps)),
  ('deployments',           (SELECT COALESCE(MAX(seq), 0) + 1 FROM deployments)),
  ('builds',                (SELECT COALESCE(MAX(seq), 0) + 1 FROM builds)),
  ('audit',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM audit)),
  ('nodes',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM nodes)),
  ('routes',                (SELECT COALESCE(MAX(seq), 0) + 1 FROM routes)),
  ('secrets',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM secrets)),
  ('configs',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM configs)),
  ('volumes',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM volumes)),
  ('networks',              (SELECT COALESCE(MAX(seq), 0) + 1 FROM networks)),
  ('users',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM users)),
  ('teams',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM teams)),
  ('roles',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM roles)),
  ('memberships',           (SELECT COALESCE(MAX(seq), 0) + 1 FROM memberships)),
  ('tokens',                (SELECT COALESCE(MAX(seq), 0) + 1 FROM tokens)),
  ('invitations',           (SELECT COALESCE(MAX(seq), 0) + 1 FROM invitations)),
  ('tasks',                 (SELECT COALESCE(MAX(seq), 0) + 1 FROM tasks)),
  ('runs',                  (SELECT COALESCE(MAX(seq), 0) + 1 FROM runs)),
  ('schedules',             (SELECT COALESCE(MAX(seq), 0) + 1 FROM schedules)),
  ('network_peers',         (SELECT COALESCE(MAX(seq), 0) + 1 FROM network_peers)),
  ('change_freezes',        (SELECT COALESCE(MAX(seq), 0) + 1 FROM change_freezes)),
  ('source_uploads',        (SELECT COALESCE(MAX(seq), 0) + 1 FROM source_uploads)),
  ('databases',             (SELECT COALESCE(MAX(seq), 0) + 1 FROM databases)),
  ('backups',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM backups)),
  ('alert_rules',           (SELECT COALESCE(MAX(seq), 0) + 1 FROM alert_rules)),
  ('notification_channels', (SELECT COALESCE(MAX(seq), 0) + 1 FROM notification_channels)),
  ('shared_variables',      (SELECT COALESCE(MAX(seq), 0) + 1 FROM shared_variables)),
  ('app_variables',         (SELECT COALESCE(MAX(seq), 0) + 1 FROM app_variables)),
  ('github_apps',           (SELECT COALESCE(MAX(seq), 0) + 1 FROM github_apps)),
  ('browse_sessions',       (SELECT COALESCE(MAX(seq), 0) + 1 FROM browse_sessions)),
  ('pubsubs',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM pubsubs)),
  ('bucket_stores',         (SELECT COALESCE(MAX(seq), 0) + 1 FROM bucket_stores)),
  ('topics',                (SELECT COALESCE(MAX(seq), 0) + 1 FROM topics)),
  ('streams',               (SELECT COALESCE(MAX(seq), 0) + 1 FROM streams)),
  ('bucket_resources',      (SELECT COALESCE(MAX(seq), 0) + 1 FROM bucket_resources));

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
