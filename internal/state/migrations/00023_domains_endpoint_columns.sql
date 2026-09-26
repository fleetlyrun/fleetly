-- 00023：domains 表协议与证书模式列（T 线 IMPL-T1-1 域名资源面）。
-- 加法迁移纪律：只加列，不改写既有列/行语义；既有行取默认值 = 现行行为
-- （http + ACME HTTP-01）。
--   protocol   后端协议（http | h2c；h2c = Traefik 后端 scheme=h2c 直出）。
--   cert_mode  证书模式（http01 | wildcard；本票只落存储与校验，签发路径
--              零变化——DNS-01 app 级签发链沿 W5）。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up

ALTER TABLE domains ADD COLUMN protocol TEXT NOT NULL DEFAULT 'http';
ALTER TABLE domains ADD COLUMN cert_mode TEXT NOT NULL DEFAULT 'http01';

-- +goose Down
ALTER TABLE domains DROP COLUMN cert_mode;
ALTER TABLE domains DROP COLUMN protocol;
