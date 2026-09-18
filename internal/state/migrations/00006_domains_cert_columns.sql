-- 00006：domains 表路由/证书列（T2.15 路由发布 + T2.16 集中 ACME）。
-- 加法迁移纪律：只加列，不改写既有列/行语义。
--   port            路由目标端口（compose expose 首端口，架构 §2.4）；
--                    '' = 尚未随发布同步。
--   cert_sha256     证书 PEM（链 + 叶）内容 sha256 hex；'' = 尚无证书。
--   cert_not_after  叶证书 NotAfter（UTC UnixNano）；NULL = 尚无证书。
--   cert_updated_at 证书登记最近一次写入时间（UTC UnixNano）；NULL = 从未。

-- +goose Up

ALTER TABLE domains ADD COLUMN port TEXT NOT NULL DEFAULT '';
ALTER TABLE domains ADD COLUMN cert_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE domains ADD COLUMN cert_not_after INTEGER;
ALTER TABLE domains ADD COLUMN cert_updated_at INTEGER;
