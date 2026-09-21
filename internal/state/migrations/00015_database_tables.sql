-- E4 数据库托管——库实例四表（managed-databases 设计 §5.4 迁移②，迁移
-- 00015，只加法纪律）：db_instances（库实例权威态 + 七态状态机列）/
-- db_references（引用倒排登记——引用方 compose 派生、可整表重建的索引面）
-- / app_secrets（平台密钥库，external-only compose secrets 的唯一来源）/
-- db_backups（库备份台账，restic snapshot 寻址非文件路径）。
--
-- 列口径（设计 §5.4 清单 + §2.1/§2.3 状态机行）：
--   db_instances.id         ULID 主键；
--   name                    实例名 UNIQUE（与 app 名同字符集规则；对象前缀
--                           族 fleetly-db-* 与 app 名族解耦——app 与库实例
--                           可重名，§2.1 名字空间独立）；
--   template                模板 ID（internal/dbtemplate 内置注册表：
--                           postgres-16 / redis-7；用户不可改镜像——受管面）；
--   image_digest            模板镜像钉定引用（升级 = 受控重建换值，非版本重放）；
--   settings                JSON 文本（限额 + 备份计划；state.DatabaseSettings
--                           序列化形态；'{}' = 全模板缺省）；
--   credential_cipher       age 密文（引擎凭据；明文永不落库/进日志/事件——
--                           state-model §2.9 secret 纪律）；
--   credential_updated_at   UnixNano（NULL = 创建后从未轮换——轮换仅手动，
--                           §2.5）；
--   platform_node_id        放置绑定内嵌（D-DB-1：不写 placements 表——该表
--                           以 app_id 为主键）；'' = 未绑定；
--   state                   七态状态机位（CHECK 词典列举 + state 层写入通道
--                           双保险；转移表唯一真源 = state.DatabaseTransitions，
--                           EnterDbPhase 单写点咬合，穷举测试钉死）；
--   created_at/updated_at   UnixNano（仓内时间列约定）；
--   deleting_at/deleted_at  tombstone 时间戳（INTEGER UnixNano 可空，与 apps
--                           表同型〔00001:30-31〕；NULL = 未进入——对应设计
--                           §2.1「deleting 无失败出边、deleted 名字保留期
--                           占用」的两拍锚）。
--
-- db_references 无 ON DELETE 级联：引用清理是业务动作（引用 app 删除 =
-- 行级联清理由 tombstone 流程显式执行；label 移除 + 部署 = 行删除——
-- §2.4「planner 同事务维护」），库删除守卫按 app_id 反查给出引用清单。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE db_instances (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL UNIQUE,
    template              TEXT NOT NULL,
    image_digest          TEXT NOT NULL,
    settings              TEXT NOT NULL DEFAULT '{}',
    credential_cipher     TEXT NOT NULL,
    credential_updated_at INTEGER,
    platform_node_id      TEXT NOT NULL DEFAULT '',
    state                 TEXT NOT NULL CHECK (state IN ('provisioning', 'ready', 'failed', 'degraded', 'paused', 'deleting', 'deleted')),
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    deleting_at          INTEGER,
    deleted_at           INTEGER
);
-- name 的 UNIQUE 约束自带查找索引（pragmatic：不为 name 重复建索引）；
-- state 是收敛器/调度扫描谓词（provisioning/deleting 两态的在途扫描）。
CREATE INDEX idx_db_instances_state ON db_instances (state);

CREATE TABLE db_references (
    db_id      TEXT NOT NULL REFERENCES db_instances (id),
    app_id     TEXT NOT NULL REFERENCES apps (id),
    service    TEXT NOT NULL,
    env_prefix TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (db_id, app_id, service)
);
-- app 侧反查（引用 app 部署重建引用 = 先清后插；app 删除 = 行级联清理）。
CREATE INDEX idx_db_references_app ON db_references (app_id);

CREATE TABLE app_secrets (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL REFERENCES apps (id),
    name         TEXT NOT NULL,
    value_cipher TEXT NOT NULL,
    hash8        TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (app_id, name)
);

CREATE TABLE db_backups (
    id              TEXT PRIMARY KEY,
    db_id           TEXT NOT NULL REFERENCES db_instances (id),
    kind            TEXT NOT NULL CHECK (kind IN ('daily', 'manual', 'pre_upgrade')),
    restic_snapshot TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    verify_status   TEXT NOT NULL DEFAULT 'unverified' CHECK (verify_status IN ('unverified', 'verified', 'failed')),
    error           TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
CREATE INDEX idx_db_backups_db ON db_backups (db_id, created_at);

-- +goose Down
-- 仅 goose 演练（生产回滚 = 恢复快照）：子表先删、实例表最后。
DROP TABLE db_backups;
DROP TABLE app_secrets;
DROP TABLE db_references;
DROP TABLE db_instances;
