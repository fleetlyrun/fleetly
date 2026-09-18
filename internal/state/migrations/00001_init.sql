-- fleetly 控制面核心表 v0.1（T2 阶段，单条 init 迁移，只加法纪律）。
--
-- 设计依据：architecture §2.3 核心表草案 + v0.1 冻结清单 §2.3（scope-freeze）
-- + state-model §2.1/§2.2/§2.3/§2.6/§2.9。业务语义随后续票填充的表
-- （deployments/revisions/env_vars/domains/placements/volumes/tokens/
-- state_backups/orphans）本迁移只建结构 + 最小读写通道。
--
-- 约定：
--   * 主键 = 平台 ULID 文本（nodes.swarm_node_id 除外——观测缓存以底座
--     节点 ID 为键，可整表重建）；
--   * 时间列一律 INTEGER（UnixNano，UTC），避免文本时间排序歧义；
--   * 迁移只加法：后续版本只允许新增表/列/索引（回滚 = 恢复快照，
--     不写 down migration，架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- 权威态：应用（期望态根）。lifecycle = tombstone-first 状态位
-- （state-model §2.6：deleting → deleted + 保留期，恢复不复活）。
CREATE TABLE apps (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    lifecycle   TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deleting', 'deleted')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleting_at INTEGER,
    deleted_at  INTEGER
);
CREATE INDEX idx_apps_lifecycle ON apps (lifecycle);

-- 权威态：版本快照（归一化 compose + 平台覆盖层，发布专项；最近 5 个已
-- 验证版本支持单层重放回滚）。
CREATE TABLE revisions (
    id                 TEXT PRIMARY KEY,
    app_id             TEXT NOT NULL REFERENCES apps (id),
    seq                INTEGER NOT NULL,
    compose_normalized TEXT NOT NULL,
    overlay            TEXT NOT NULL DEFAULT '{}',
    desired_hash       TEXT NOT NULL,
    verified           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    UNIQUE (app_id, seq)
);

-- 历史：部署记录（kind=deploy|rollback 与 recovery 字段按发布专项：归位
-- 不创建新记录；substrate_halted 对应首发失败 scale=0 保留现场）。
CREATE TABLE deployments (
    id               TEXT PRIMARY KEY,
    app_id           TEXT NOT NULL REFERENCES apps (id),
    kind             TEXT NOT NULL CHECK (kind IN ('deploy', 'rollback')),
    status           TEXT NOT NULL,
    revision_id      TEXT REFERENCES revisions (id),
    recovery_of      TEXT REFERENCES deployments (id),
    substrate_halted INTEGER NOT NULL DEFAULT 0,
    error_code       TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX idx_deployments_app ON deployments (app_id, created_at);

-- 权威态：平台层环境变量（三层合并链的平台层；value 密文形态随 T2 密钥
-- 票落地，本表结构不变）。
CREATE TABLE env_vars (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL REFERENCES apps (id),
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'platform',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (app_id, key)
);

-- 权威态：路由域名（域名列表契约；同域名冲突检查在 API 用例层）。
CREATE TABLE domains (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL REFERENCES apps (id),
    service    TEXT NOT NULL,
    domain     TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_domains_app ON domains (app_id);

-- 权威态：放置绑定（平台节点 ID 为锚，D16；state ∈ bound/blocked/
-- unresolved 派生语义见 state-model §2.10）。
CREATE TABLE placements (
    app_id           TEXT PRIMARY KEY REFERENCES apps (id),
    platform_node_id TEXT NOT NULL,
    state            TEXT NOT NULL DEFAULT 'bound' CHECK (state IN ('bound', 'blocked', 'unresolved')),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

-- 权威态：卷注册表（数据诞生点钉住所在节点；status 孤儿状态位对应
-- 冻结清单「卷注册表含孤儿状态位」）。
CREATE TABLE volumes (
    id               TEXT PRIMARY KEY,
    app_id           TEXT NOT NULL REFERENCES apps (id),
    key              TEXT NOT NULL,
    name             TEXT NOT NULL UNIQUE,
    platform_node_id TEXT,
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'orphaned', 'discarded')),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    UNIQUE (app_id, key)
);

-- 派生缓存：节点观测快照（state-model §2.2：禁止用于决策，可整表重建）。
-- state/availability 逐字镜像 Swarm；last_seen_at 是平台观测语义时间
-- （平台不承诺最后心跳时间戳——Swarm 不暴露该概念，文案与 API 亦不得
-- 如此声称）；observed_at/stale 是读契约字段。
CREATE TABLE nodes (
    swarm_node_id     TEXT PRIMARY KEY,
    hostname          TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL DEFAULT 'unknown',
    availability      TEXT NOT NULL DEFAULT 'active',
    is_manager        INTEGER NOT NULL DEFAULT 0,
    substrate_version INTEGER NOT NULL DEFAULT 0,
    labels            TEXT NOT NULL DEFAULT '{}',
    last_seen_at      INTEGER NOT NULL,
    observed_at       INTEGER NOT NULL,
    stale             INTEGER NOT NULL DEFAULT 0
);

-- 适配器映射：平台节点 ID ↔ Swarm node ID（state-model §2.3：Swarm
-- node ID 仅存适配器映射，映射本身可从节点 label 反建）。
CREATE TABLE runtime_node_refs (
    platform_id   TEXT PRIMARY KEY,
    swarm_node_id TEXT NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    anchored_at   INTEGER
);

-- 权威态：API token（哈希存储，明文永不落库）。
CREATE TABLE tokens (
    id           TEXT PRIMARY KEY,
    token_hash   TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL DEFAULT '',
    scopes       TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);

-- 历史与叙事：平台事件（seq 单调永不复用 = SSE 游标；30 天保留期）。
CREATE TABLE events (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,
    at      INTEGER NOT NULL,
    name    TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_events_at ON events (at);

-- 历史与叙事：审计日志（fail-closed：与业务写同事务，state-model §2.9；
-- actor/action/result 非空为数据库级约束——审计写不出合法行即操作失败）。
CREATE TABLE audit_log (
    id             TEXT PRIMARY KEY,
    at             INTEGER NOT NULL,
    actor          TEXT NOT NULL CHECK (length(actor) > 0),
    actor_token_id TEXT,
    action         TEXT NOT NULL CHECK (length(action) > 0),
    target         TEXT NOT NULL DEFAULT '',
    result         TEXT NOT NULL CHECK (result IN ('ok', 'error')),
    error_code     TEXT,
    request_id     TEXT,
    diff_summary   TEXT
);
CREATE INDEX idx_audit_at ON audit_log (at);

-- 权威态：状态备份台账（热备 + sha256 回读校验 + 失败红色告警，
-- state-model §2.7；备份目标本地路径，S3 上传随 v0.2）。
CREATE TABLE state_backups (
    id            TEXT PRIMARY KEY,
    created_at    INTEGER NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'hot' CHECK (kind IN ('hot', 'cold')),
    path          TEXT NOT NULL,
    sha256        TEXT NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    verify_status TEXT NOT NULL DEFAULT 'pending' CHECK (verify_status IN ('pending', 'verified', 'failed')),
    error         TEXT
);

-- 孤儿登记（state-model §2.6：managed=true 但 DB 无 app 的底座对象——
-- 只登记、永不自动删除，处理走人工）。
CREATE TABLE orphans (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('service', 'container', 'volume', 'network')),
    substrate_id TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL DEFAULT '',
    detail       TEXT NOT NULL DEFAULT '{}',
    detected_at  INTEGER NOT NULL,
    status       TEXT NOT NULL DEFAULT 'registered' CHECK (status IN ('registered', 'resolved')),
    resolved_at  INTEGER
);
