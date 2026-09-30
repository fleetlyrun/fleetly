-- +goose Up
-- Identity & Access 聚合（F0.5~F0.7）：users/teams/roles/memberships/
-- tokens/invitations。内置三角色与 default Team 由启动种子（Go 侧单一
-- 源，随词表保鲜）幂等落行，不进迁移 SQL。
CREATE TABLE users (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);

CREATE TABLE teams (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);

-- roles：命名的 Scope 集合。builtin=1 是平台级模板（team_id 空）；
-- 自定义 Role 属于某 Team。scopes 是 JSON 数组（colon 形态 resource:action）。
CREATE TABLE roles (
  id         TEXT PRIMARY KEY,
  team_id    TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL,
  builtin    INTEGER NOT NULL DEFAULT 0,
  scopes     TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(team_id, name)
);

-- memberships：User 在 Team 内经 Role 获权（一个 Team 一个 Role）。
CREATE TABLE memberships (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id),
  team_id    TEXT NOT NULL REFERENCES teams(id),
  role_id    TEXT NOT NULL REFERENCES roles(id),
  created_at TEXT NOT NULL,
  UNIQUE(user_id, team_id)
);

-- tokens：sha256 存储（明文只在创建响应出现一次）；prefix 是明文前缀
-- （泄露可扫描识别）；scopes 不落本表——Token 经 role 间接获权（CONTEXT.md
-- Token 词条）。user_id 可空（无属主用户的 Token：Agent/CI/bootstrap）。
-- bootstrap Token = name 'bootstrap'。
CREATE TABLE tokens (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  team_id      TEXT NOT NULL REFERENCES teams(id),
  user_id      TEXT REFERENCES users(id),
  role_id      TEXT NOT NULL REFERENCES roles(id),
  sha256       TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL,
  revoked      INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL
);
CREATE INDEX idx_tokens_team ON tokens(team_id);

-- invitations：一次性邀请（sha256 存储、时窗内单次有效、绑 team+role）。
CREATE TABLE invitations (
  id          TEXT PRIMARY KEY,
  token_sha256 TEXT NOT NULL UNIQUE,
  team_id     TEXT NOT NULL REFERENCES teams(id),
  role_id     TEXT NOT NULL REFERENCES roles(id),
  created_by  TEXT NOT NULL DEFAULT '',
  expires_at  TEXT NOT NULL,
  consumed_at TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8）。
