-- 团队与多用户 RBAC（rbac-teams 设计 §2.1/§2.2/§2.3/§3.1/§3.3/§8，迁移
-- 00018，只加法纪律）：七新表 users（用户权威态 + argon2id 口令哈希）/
-- sessions（Console 浏览器会话）/ teams / team_members / team_invites /
-- projects / project_members（队内覆写形，D-W0-2）；apps 与 db_instances
-- 加 project_id + team_id（databases 族实际表名 = db_instances）、tokens 加
-- user_id + project_id、git_keys 加 user_id。
--
-- 加列全部可空（设计 §8 实现切分：W1 部署路径未接归属，NOT NULL 收敛是
-- W2 的 00019 表重建收紧；SQLite UNIQUE 对 NULL 互异不阻塞——可空列上的
-- UNIQUE(project_id, name) 在归属接入前不会误伤既有行，NULL 归属行彼此
-- 共存）。外键关系应用层维护（与既有表一致，SQLite 不开硬约束）。
--
-- 列口径（设计 §2.1/§3.1 清单）：
--   users.id                   ULID 主键；
--   users.email                登录标识，小写归一后 UNIQUE（归一在 state 写入通道）；
--   users.password_hash        argon2id PHC 串（m=64MiB, t=2, p=1, keyLen=32,
--                              salt 16B 随机；明文口令永不落库/进日志/事件/审计）；
--   users.display_name         人读显示名（'' = 缺省取 email 本地部分）；
--   users.is_platform_admin    平台管理员标志（用户标志非角色，设计 §3.2；
--                              首注册用户由写入通道强制置 1——无用户窗口恒开）；
--   users.disabled_at          禁用时间戳（NULL = 在册；禁用 = 会话行同事务
--                              清空 + PAT 认证路径联动拒认，设计 §2.1/§10）；
--   created_at 族              UnixNano（仓内时间列约定）。
--
--   sessions                   设计 §2.2：token_hash = 随机 32B cookie 值的
--                              sha256 hex（明文只随创建响应交调用方）；TTL
--                              策略（7 天滑动 + 30 天绝对上限）在调用面计算；
--                              last_seen_at 节流盖写（60s 窗口，A2 同款）；
--                              过期行由 janitor 清扫（认证路径本就拒认过期）。
--
--   teams.slug                 单词制 [a-z0-9]{2,32}、不可变（D-W0-4：slug
--                              进底座命名公式，禁连字符 = 拼接无歧义）；约束
--                              只做 UNIQUE，字符集/保留字校验留上层
--                              （E_TEAM_SLUG_RESERVED）；
--   projects.slug              单词制 [a-z0-9]{2,32}、不可变（D-W0-4 二修：
--                              进底座命名公式三段第二位）；team 内唯一
--                              UNIQUE(team_id, slug)——同名项目跨团队允许
--                              （D-W0-9，引用解析用 team/prj 限定形）；
--   team_members.role          owner/admin/developer/viewer 四档（CHECK 词典
--                              + state 写入通道双保险；最后一名 owner 守卫
--                              E_TEAM_LAST_OWNER 在 state 写入通道）；
--   project_members.role       admin/developer/viewer 队内覆写（owner 不可
--                              覆写——owner 是团队级概念，设计 §3.3）；
--   team_invites.role          无 CHECK（与设计逐字对齐；角色词表校验在
--                              state 写入通道）；token_hash = 一次性邀请
--                              token 的 sha256 hex，7 天过期（expires_at），
--                              accepted_at/revoked_at 记消费/吊销。
--
--   tokens.user_id             NULL = 平台机具令牌（设计 §2.3：平台管理员
--                              显式创建的平台级凭据，非兼容残留）；非 NULL =
--                              用户 PAT；tokens.project_id NULL = 不绑定；
--   git_keys.user_id           GitKeys 用户化列（W2 语义迁移，§2.3），本票
--                              先落列（可空，存量行不回填）。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE users (
    id                TEXT PRIMARY KEY,
    email             TEXT NOT NULL UNIQUE,  -- 小写归一
    password_hash     TEXT NOT NULL,         -- argon2id PHC 串
    display_name      TEXT NOT NULL DEFAULT '',
    is_platform_admin INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    disabled_at       INTEGER
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    token_hash   TEXT NOT NULL UNIQUE,
    user_id      TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER
);

CREATE TABLE teams (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE, -- 单词制 [a-z0-9]{2,32}、不可变（底座命名公式段；校验在 state 写入通道）
    name       TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE team_members (
    team_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    created_at INTEGER NOT NULL,
    UNIQUE (team_id, user_id)
);

CREATE TABLE team_invites (
    id          TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  INTEGER NOT NULL,   -- 7d
    created_by  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    accepted_at INTEGER,
    revoked_at  INTEGER
);

CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL,
    slug        TEXT NOT NULL, -- 单词制 [a-z0-9]{2,32}、不可变（底座命名公式段；team 内唯一）
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    UNIQUE (team_id, slug)
);

CREATE TABLE project_members (          -- D-W0-2 修订：队内覆写形
    project_id TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'developer', 'viewer')),
    created_at INTEGER NOT NULL,
    UNIQUE (project_id, user_id)      -- 仅限团队成员（写入校验 + 移出团队联动清理）
);

-- 归属/用户化加列（全部可空——设计 §8 切分：00019 表重建收紧 NOT NULL）。
ALTER TABLE apps ADD COLUMN project_id TEXT;
ALTER TABLE apps ADD COLUMN team_id TEXT;
ALTER TABLE db_instances ADD COLUMN project_id TEXT;
ALTER TABLE db_instances ADD COLUMN team_id TEXT;
ALTER TABLE tokens ADD COLUMN user_id TEXT;    -- NULL = 平台机具令牌（设计 §2.3）
ALTER TABLE tokens ADD COLUMN project_id TEXT; -- NULL = 不绑定
ALTER TABLE git_keys ADD COLUMN user_id TEXT;  -- W2 GitKeys 用户化（§2.3）先落列

-- app/库名唯一性降为 project 内（D-W0-4 二修）：UNIQUE(project_id, name)，
-- 每个项目各有自己的 web/api/db；NULL 归属行互异不阻塞（SQLite 语义）。
CREATE UNIQUE INDEX idx_apps_project_name ON apps (project_id, name);
CREATE UNIQUE INDEX idx_db_instances_project_name ON db_instances (project_id, name);

-- 索引（设计 §8 清单）：成员/项目归属解析与列表加速 + 会话清扫扫描。
CREATE INDEX idx_apps_project ON apps (project_id);
CREATE INDEX idx_apps_team ON apps (team_id);
CREATE INDEX idx_db_instances_project ON db_instances (project_id);
CREATE INDEX idx_db_instances_team ON db_instances (team_id);
CREATE INDEX idx_tokens_user ON tokens (user_id);
CREATE INDEX idx_team_members_team ON team_members (team_id);
CREATE INDEX idx_team_members_user ON team_members (user_id);
CREATE INDEX idx_projects_team ON projects (team_id);
CREATE INDEX idx_project_members_project ON project_members (project_id);
CREATE INDEX idx_sessions_user ON sessions (user_id);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- +goose Down
-- 仅 goose 演练（生产回滚 = 恢复快照）：先摘加列表上的索引（DROP COLUMN
-- 不允许被索引引用），再删新表；新表自身索引随表删除。
DROP INDEX idx_apps_project_name;
DROP INDEX idx_apps_project;
DROP INDEX idx_apps_team;
DROP INDEX idx_db_instances_project_name;
DROP INDEX idx_db_instances_project;
DROP INDEX idx_db_instances_team;
DROP INDEX idx_tokens_user;
ALTER TABLE apps DROP COLUMN project_id;
ALTER TABLE apps DROP COLUMN team_id;
ALTER TABLE db_instances DROP COLUMN project_id;
ALTER TABLE db_instances DROP COLUMN team_id;
ALTER TABLE tokens DROP COLUMN user_id;
ALTER TABLE tokens DROP COLUMN project_id;
ALTER TABLE git_keys DROP COLUMN user_id;
DROP TABLE project_members;
DROP TABLE projects;
DROP TABLE team_invites;
DROP TABLE team_members;
DROP TABLE teams;
DROP TABLE sessions;
DROP TABLE users;
