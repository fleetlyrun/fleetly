-- T2-3（T2.4 对象标记 + T2.7 密钥/env + T2.14 placement 单机切面）加法迁移：
-- 只新增列，不改已有列、不动约束（架构 §2.8 契约版本化纪律：迁移只加法，
-- 回滚 = 恢复快照）。00001_init.sql 的既有行语义不受影响（全部新列带缺省）。

-- +goose Up

-- env_vars：生效状态位（三层合并链只消费 effective；fleetly env set 创建
-- pending、随下次部署生效——消费点在发布引擎 T2.10）。既有行视为已生效。
ALTER TABLE env_vars ADD COLUMN status TEXT NOT NULL DEFAULT 'effective'
    CHECK (status IN ('pending', 'effective'));
CREATE INDEX idx_env_vars_app_status ON env_vars (app_id, status);

-- placements：放置专项 §2.4 表结构补齐——绑定来源（platform|label）、label
-- 原值、派生原因、乐观令牌 etag、钉住时刻。state 词表沿用 00001 的
-- bound/blocked/unresolved（'bound' 即设计文档的 ok 态；ALTER 不能改约束，
-- 词表不回收）。
ALTER TABLE placements ADD COLUMN source TEXT NOT NULL DEFAULT 'platform'
    CHECK (source IN ('platform', 'label'));
ALTER TABLE placements ADD COLUMN label_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE placements ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE placements ADD COLUMN etag TEXT NOT NULL DEFAULT '';
ALTER TABLE placements ADD COLUMN pinned_at INTEGER;

-- volumes：卷注册表补齐挂载语义——kind（named|bind）、容器内挂载点、宿主
-- 路径（bind 卷）。docker_name 对应既有 name 列（命名约定值）。
ALTER TABLE volumes ADD COLUMN kind TEXT NOT NULL DEFAULT 'named'
    CHECK (kind IN ('named', 'bind'));
ALTER TABLE volumes ADD COLUMN mount_path TEXT NOT NULL DEFAULT '';
ALTER TABLE volumes ADD COLUMN host_path TEXT NOT NULL DEFAULT '';
