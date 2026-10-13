-- +goose Up
-- GitHub App 接入材料聚合（ADR-0073 阶段 B）：私有 Git 仓库的构建期
-- 访问材料。双作用域：project_id 空 = 平台级行（平台管理授权，全项目
-- 可用），非空 = Project 级（行级 Project 授权）——两态各按属主唯一，
-- 解析时 Project 级遮蔽 Global。private_key_ciphertext 是 age 信封，
-- 读面只回指纹；account_login 受理即经 GitHub API 解析（系统填充），
-- 是构建期 host+属主解析的匹配键。
CREATE TABLE github_apps (
  id                      TEXT PRIMARY KEY,
  seq                     INTEGER NOT NULL,
  project_id              TEXT NOT NULL DEFAULT '',
  app_id                  INTEGER NOT NULL,
  installation_id         INTEGER NOT NULL,
  account_login           TEXT NOT NULL,
  api_base                TEXT NOT NULL DEFAULT '',
  private_key_ciphertext  BLOB NOT NULL,
  private_key_fingerprint TEXT NOT NULL,
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL,
  deleted_at              TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_github_apps_seq ON github_apps(seq);
CREATE UNIQUE INDEX idx_github_apps_global_owner ON github_apps(account_login) WHERE deleted_at = '' AND project_id = '';
CREATE UNIQUE INDEX idx_github_apps_project_owner ON github_apps(project_id, account_login) WHERE deleted_at = '' AND project_id != '';

-- +goose Down
-- goose 只前滚（架构 §8）。
