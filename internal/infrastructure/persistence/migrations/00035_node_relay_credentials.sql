-- +goose Up
-- 节点个体 relay 凭证（H10 / R14-H3）：join token 是集群级共享串，
-- hello.carrier_node_id 又是客户端自报——持有 join token 者可顶替任意节点
-- 中继。本表把“凭证 → 节点身份”绑定落到平台侧：一个节点至多一条现役
-- 凭证；重铸 = UPSERT 替换 hash（旧凭证即刻失效）。
--
-- 不加 expires_at：凭证生命周期 = join token 同面（rotate/revoke/node.left
-- 才失效）；pre-release 无兼容负担。不落明文/盐：32B 随机串的 sha256 是
-- 查询键，熵足够，盐无收益（identity/token.go 同款）。
-- 不加 nodes 外键：nodes 是观测缓存（node/node.go:1-18），节点行可能先于
-- 凭证存在/长期保留 unavailable；凭证只在“节点已锚定”后铸造，行存在性
-- 由 handler 校验。
CREATE TABLE node_relay_credentials (
  platform_id   TEXT PRIMARY KEY,
  carrier_id    TEXT NOT NULL,
  secret_sha256 TEXT NOT NULL UNIQUE,
  created_at    TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8：迁移失败 = 恢复 Platform Backup 重放）。
