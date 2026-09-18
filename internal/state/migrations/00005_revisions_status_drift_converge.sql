-- T2-5b（T2.12 版本快照读取与保留窗 + T2.13 漂移检测）加法迁移：只新增
-- 列与索引，不改/不删既有列（架构 §2.8 契约版本化纪律：迁移只加法，
-- 回滚 = 恢复快照；词表无 CHECK，约束语义由 state 层词表常量承载）。

-- +goose Up

-- 版本快照保留窗状态位（release-semantics §2.4：固定保留最近 5 次成功
-- 部署，列表即选项）。第 6 个成功快照固化时把最旧的标记 superseded
--（不物理删——审计与历史可读）；active = 可回滚选项集。
-- 00001 的 verified 列保持原义（成功固化位）；本列承载保留窗淘汰位。
ALTER TABLE revisions ADD COLUMN status TEXT NOT NULL DEFAULT 'active';

-- 回滚目标解析（ListRevisions / GetAppRevision 的主查询面）。
CREATE INDEX idx_revisions_app_status ON revisions (app_id, status);

-- 漂移收敛 per-app opt-in（state-model §2.5 D11：检测默认开、自动收敛
-- 默认关）。0 = 关（默认）；1 = 开（检测到漂移即按当前期望态收敛）。
-- 回滚失败时引擎强制清 0（D-REL-10：critical 后只检测不收敛，直至人工
-- 重置——CLI `fleetly drift enable` 为唯一重置入口，带审计）。
ALTER TABLE apps ADD COLUMN drift_converge INTEGER NOT NULL DEFAULT 0;
