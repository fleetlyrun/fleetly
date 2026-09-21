-- E4 数据库托管——volumes 归属泛化（managed-databases 设计 §5.4 迁移①，
-- 迁移 00014）。库实例成为独立一等资源（D-DB-1 用户终裁）后，卷注册表
-- 不再以 app 为唯一归属：owner_kind ∈ {app, database} + owner_id 取代
-- app_id（唯一键 (owner_kind, owner_id, key)），卷登记/孤儿/丢弃/命名防
-- 代际语义全复用（设计 §2.1 组件级复用清单）。
--
-- 00008 先例同型：SQLite 不能改 CHECK/唯一键，唯一正路 = 重建表（同事务
-- 内 建新→搬行→删旧→改名；volumes 表无外键被引用者，无连带）。
--
-- 裁决注记（设计 §5.4）：owner_id 不建外键——归属是多态的（apps 与
-- db_instances 两个主键空间），SQLite 多态外键只能靠触发器模拟，收益不抵
-- 复杂度；引用完整性由 state 层写入通道保证（调用方先建 app/db 行、再登记
-- 卷）。apps 对 volumes 的 REFERENCES 关系自此降级为「owner_kind='app'
-- 时 owner_id 即 apps.id」的约定，由 Go 写入通道维持。
--
-- 既有 app 卷行零语义变化：搬行 owner_kind='app'、owner_id=app_id，全部
-- 事实列原值平移（volume_migration_test.go 快照测试钉死）。
--
-- 只加法纪律注记：本迁移不改写任何已应用文件，只新增 00014；生产回滚 =
-- 恢复快照，不写 down migration（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE volumes_new (
    id                    TEXT PRIMARY KEY,
    owner_kind            TEXT NOT NULL DEFAULT 'app' CHECK (owner_kind IN ('app', 'database')),
    owner_id              TEXT NOT NULL,
    key                   TEXT NOT NULL,
    name                  TEXT NOT NULL UNIQUE,
    platform_node_id      TEXT,
    status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'orphaned', 'discarded')),
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    kind                  TEXT NOT NULL DEFAULT 'named' CHECK (kind IN ('named', 'bind')),
    mount_path            TEXT NOT NULL DEFAULT '',
    host_path             TEXT NOT NULL DEFAULT '',
    prev_platform_node_id TEXT NOT NULL DEFAULT '',
    UNIQUE (owner_kind, owner_id, key)
);
INSERT INTO volumes_new (id, owner_kind, owner_id, key, name, platform_node_id, status,
                         created_at, updated_at, kind, mount_path, host_path, prev_platform_node_id)
    SELECT id, 'app', app_id, key, name, platform_node_id, status,
           created_at, updated_at, kind, mount_path, host_path, prev_platform_node_id
    FROM volumes;
DROP TABLE volumes;
ALTER TABLE volumes_new RENAME TO volumes;

-- +goose Down
-- 反向重建（仅 goose 演练——DownTo/Up 往返测试跨 00011-00015；生产回滚 =
-- 恢复快照）。database 归属行在旧 schema 无宿主（无 owner_kind 列），
-- 只回滚 app 行——演练库不承载真实 database 卷，如实的丢弃面。
CREATE TABLE volumes_legacy (
    id                    TEXT PRIMARY KEY,
    app_id                TEXT NOT NULL REFERENCES apps (id),
    key                   TEXT NOT NULL,
    name                  TEXT NOT NULL UNIQUE,
    platform_node_id      TEXT,
    status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'orphaned', 'discarded')),
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    kind                  TEXT NOT NULL DEFAULT 'named' CHECK (kind IN ('named', 'bind')),
    mount_path            TEXT NOT NULL DEFAULT '',
    host_path             TEXT NOT NULL DEFAULT '',
    prev_platform_node_id TEXT NOT NULL DEFAULT '',
    UNIQUE (app_id, key)
);
INSERT INTO volumes_legacy (id, app_id, key, name, platform_node_id, status,
                            created_at, updated_at, kind, mount_path, host_path, prev_platform_node_id)
    SELECT id, owner_id, key, name, platform_node_id, status,
           created_at, updated_at, kind, mount_path, host_path, prev_platform_node_id
    FROM volumes WHERE owner_kind = 'app';
DROP TABLE volumes;
ALTER TABLE volumes_legacy RENAME TO volumes;
