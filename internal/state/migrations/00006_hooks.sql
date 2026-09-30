-- +goose Up
-- Git 触发聚合（F0.13）：per-App hook 配置与 webhook 重投去重。
-- token_sha256 是 URL token 查找键；secret_ciphertext 是 secret 的 age
-- 信封——HMAC 验签需原串（纯摘要不可逆），明文只在铸造响应出现一次。
CREATE TABLE app_hooks (
  app_id            TEXT PRIMARY KEY REFERENCES apps(id),
  repo              TEXT NOT NULL,
  branch            TEXT NOT NULL DEFAULT '',
  dockerfile        TEXT NOT NULL DEFAULT 'Dockerfile',
  watch_paths       TEXT NOT NULL DEFAULT '[]',
  token_sha256      TEXT NOT NULL UNIQUE,
  token_prefix      TEXT NOT NULL DEFAULT '',
  secret_ciphertext BLOB NOT NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

-- webhook 重投去重（admission 的 commit 去重只覆盖活跃部署；终态后同
-- delivery 重投靠本表拦截）。收侧顺带清理 7 天前旧行（防无界增长）。
CREATE TABLE hook_deliveries (
  app_id      TEXT NOT NULL REFERENCES app_hooks(app_id),
  delivery    TEXT NOT NULL,
  received_at TEXT NOT NULL,
  PRIMARY KEY (app_id, delivery)
);

-- +goose Down
-- goose 只前滚（架构 §8）。
