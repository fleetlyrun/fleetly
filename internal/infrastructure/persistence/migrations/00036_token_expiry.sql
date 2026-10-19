-- +goose Up
-- Login 会话 Token 的过期时刻（R14-M1 另一半 / S18-B）：Login 铸造的密码
-- 会话 Token 此前与 bootstrap/CI Token 同形——永不过期，口令泄露即永久
-- 接管；本列给会话 Token 一个服务端可执法的时效锚（authn 拦截器逐请求
-- 比对 expires_at，过期即 E_UNAUTHENTICATED）。
--
-- 语义：'' = 永不过期（bootstrap Token、tokens create/CLI/SDK 铸造的
-- Agent/CI Token 保持原语义——升级零扰动），非空 = UTC RFC3339 秒精度
-- 文本（state.FormatTime 同口径）。不做存量回填/编造过期时刻；不建索引：
-- 查询键是 sha256 唯一索引（逐请求查表），expires_at 只作行内判定。
ALTER TABLE tokens ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
