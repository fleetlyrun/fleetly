-- platform_settings 运行期设置 KV（E3 对象存储专项设计 §2.2/D-S3-2，迁移
-- 00011，唯一 DB 迁移，只加纪律）：S3 端点配置不落 config.yaml，落库内
-- 运行期设置——Console/CLI 配置 → 测试连接 → 保存即生效，无需 SSH 改
-- yaml + 重启 daemon（domains/env/tokens 同为「用户运行期资源」类）。
--
-- 词表只增（§2.2）：s3.mode / s3.endpoint_url / s3.region / s3.bucket /
-- s3.access_key_id / s3.secret_access_key / s3.path_style / s3.public_exposed。
-- 其中 s3.secret_access_key 存 envelope 密文（internal/secrets，state 层
-- 不解释密文）；明文绝不进日志/事件/审计/错误。
--
-- 时间列口径沿用仓内约定（UnixNano 整数存储；DATETIME 在 SQLite 中是
-- 名义类型）。Down 仅供 goose 演练（E3-2 验证「迁移前滚回」）；生产回滚
-- = 恢复快照（架构 §2.8）。

-- +goose Up
CREATE TABLE platform_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE platform_settings;
