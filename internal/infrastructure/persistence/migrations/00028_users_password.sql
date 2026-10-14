-- +goose Up
-- 00028: 用户密码凭证第二形态（C6 第一期：密码会话；SSO 独立后续批）。
-- password_hash 空 = 未设密（密码登录诚实拒绝）；bcrypt（zot htpasswd
-- 同款）。登录成功 = 服务端铸常规 API token（复用 tokens 面执法/审计/
-- 吊销），不引入独立会话表。
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
