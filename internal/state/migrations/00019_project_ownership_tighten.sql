-- 归属收紧：apps / db_instances 表重建（rbac-teams 设计 §3.4/§4.3/§8，迁移
-- 00019；D-W0-4 二修 + D-W0-5 fresh-install 前提的执行面）。与 00018 的
-- NOT NULL 注释切分说明呼应（§8 实现切分：00018 建列可空——W1 部署路径未
-- 接归属；W2-S3 命名/归属管道落地时本迁移收紧）：
--
--   1. project_id / team_id 收紧为 NOT NULL——归属是 v0.3 资源行的定义
--      部分（首次 Deploy / CreateDatabase 必须携带 project）；
--   2. 去**全局** UNIQUE(name)（两表同）——app/库名唯一性降为 project 内
--      （D-W0-4 二修：每个项目各有自己的 web/api/db；全局唯一由底座命名
--      三段 fleetly-<team>-<prj>-<app>-* 承载，rbac-teams §4.3）；
--   3. 保留 UNIQUE(project_id,name)（改以表约束声明——00018 的具名唯一
--      索引 idx_*_project_name 随旧表重建消失，语义原样保留）。
--
-- 表重建采用 SQLite 标准十二步法的收缩形态：建新表 → INSERT..SELECT 拷贝
-- → DROP 旧表 → RENAME。**fresh-install 前提（D-W0-5 修订，§8）**：v0.3 不
-- 提供 v0.2→v0.3 升级路径，现役 staging 清空重建——拷贝语句只为迁移链完整
-- 性存在（fresh 链上两表恒空，零行拷贝）；若在带 NULL 归属行的库上执行，
-- NOT NULL 收紧会显性失败（设计意图：不做收编/回填等升级兼容机制）。
-- 其余列（含 CHECK 词典与 DEFAULT）逐字保留 00001/00004/00005/00007/00015/
-- 00016 的终态口径；索引在重建后按需重铸（lifecycle/state 扫描索引 +
-- 00018 的归属加速索引）。
--
-- Down 仅供 goose 演练（生产回滚 = 恢复快照）：恢复 00018 终态形态（全局
-- UNIQUE(name) + 可空归属列 + 具名唯一索引）。

-- +goose Up
CREATE TABLE apps_new (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    lifecycle         TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deleting', 'deleted')),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    deleting_at       INTEGER,
    deleted_at        INTEGER,
    derived_state     TEXT NOT NULL DEFAULT '',
    drift_converge    INTEGER NOT NULL DEFAULT 0,
    git_branch        TEXT NOT NULL DEFAULT 'main',
    webhook_secret    TEXT,
    source_url        TEXT NOT NULL DEFAULT '',
    source_auth_kind  TEXT NOT NULL DEFAULT 'none',
    source_auth_secret TEXT NOT NULL DEFAULT '',
    -- 归属收紧（NOT NULL）+ project 内唯一（表约束；NULL 全局唯一索引的
    -- 00018 切分形态就此收敛为终态）。
    project_id        TEXT NOT NULL,
    team_id           TEXT NOT NULL,
    UNIQUE (project_id, name)
);
INSERT INTO apps_new
    (id, name, lifecycle, created_at, updated_at, deleting_at, deleted_at,
     derived_state, drift_converge, git_branch, webhook_secret,
     source_url, source_auth_kind, source_auth_secret, project_id, team_id)
SELECT
    id, name, lifecycle, created_at, updated_at, deleting_at, deleted_at,
    derived_state, drift_converge, git_branch, webhook_secret,
    source_url, source_auth_kind, source_auth_secret, project_id, team_id
FROM apps;
DROP TABLE apps;
ALTER TABLE apps_new RENAME TO apps;
CREATE INDEX idx_apps_lifecycle ON apps (lifecycle);
CREATE INDEX idx_apps_project ON apps (project_id);
CREATE INDEX idx_apps_team ON apps (team_id);

CREATE TABLE db_instances_new (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    template              TEXT NOT NULL,
    image_digest          TEXT NOT NULL,
    settings              TEXT NOT NULL DEFAULT '{}',
    credential_cipher     TEXT NOT NULL,
    credential_updated_at INTEGER,
    platform_node_id      TEXT NOT NULL DEFAULT '',
    state                 TEXT NOT NULL CHECK (state IN ('provisioning', 'ready', 'failed', 'degraded', 'paused', 'deleting', 'deleted')),
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    deleting_at           INTEGER,
    deleted_at            INTEGER,
    last_error            TEXT NOT NULL DEFAULT '',
    delete_volumes        INTEGER NOT NULL DEFAULT 0,
    -- 归属收紧（NOT NULL）+ project 内唯一（表约束，同 apps）。
    project_id            TEXT NOT NULL,
    team_id               TEXT NOT NULL,
    UNIQUE (project_id, name)
);
INSERT INTO db_instances_new
    (id, name, template, image_digest, settings, credential_cipher,
     credential_updated_at, platform_node_id, state, created_at, updated_at,
     deleting_at, deleted_at, last_error, delete_volumes, project_id, team_id)
SELECT
    id, name, template, image_digest, settings, credential_cipher,
    credential_updated_at, platform_node_id, state, created_at, updated_at,
    deleting_at, deleted_at, last_error, delete_volumes, project_id, team_id
FROM db_instances;
DROP TABLE db_instances;
ALTER TABLE db_instances_new RENAME TO db_instances;
CREATE INDEX idx_db_instances_state ON db_instances (state);
CREATE INDEX idx_db_instances_project ON db_instances (project_id);
CREATE INDEX idx_db_instances_team ON db_instances (team_id);

-- +goose Down
-- 仅 goose 演练：恢复 00018 终态形态（列全部可空回退 + 全局 UNIQUE(name)
-- + 具名唯一索引 idx_*_project_name）。演练库两表恒空（fresh 链），拷贝
-- 语句同 Up 只保链完整性。
CREATE TABLE apps_rollback (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    lifecycle   TEXT NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deleting', 'deleted')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleting_at INTEGER,
    deleted_at  INTEGER,
    derived_state TEXT NOT NULL DEFAULT '',
    drift_converge INTEGER NOT NULL DEFAULT 0,
    git_branch TEXT NOT NULL DEFAULT 'main',
    webhook_secret TEXT,
    source_url TEXT NOT NULL DEFAULT '',
    source_auth_kind TEXT NOT NULL DEFAULT 'none',
    source_auth_secret TEXT NOT NULL DEFAULT '',
    project_id TEXT,
    team_id TEXT
);
INSERT INTO apps_rollback SELECT * FROM apps;
DROP TABLE apps;
ALTER TABLE apps_rollback RENAME TO apps;
CREATE INDEX idx_apps_lifecycle ON apps (lifecycle);
CREATE UNIQUE INDEX idx_apps_project_name ON apps (project_id, name);
CREATE INDEX idx_apps_project ON apps (project_id);
CREATE INDEX idx_apps_team ON apps (team_id);

CREATE TABLE db_instances_rollback (
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
    deleting_at           INTEGER,
    deleted_at            INTEGER,
    last_error            TEXT NOT NULL DEFAULT '',
    delete_volumes        INTEGER NOT NULL DEFAULT 0,
    project_id            TEXT,
    team_id               TEXT
);
INSERT INTO db_instances_rollback SELECT * FROM db_instances;
DROP TABLE db_instances;
ALTER TABLE db_instances_rollback RENAME TO db_instances;
CREATE INDEX idx_db_instances_state ON db_instances (state);
CREATE UNIQUE INDEX idx_db_instances_project_name ON db_instances (project_id, name);
CREATE INDEX idx_db_instances_project ON db_instances (project_id);
CREATE INDEX idx_db_instances_team ON db_instances (team_id);
