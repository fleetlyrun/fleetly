-- T2.19（git push(SSH) 与 webhook 两条部署触发入口）加法迁移：只新增表/
-- 列/索引，不改/不删既有列（架构 §2.8 契约版本化纪律：迁移只加法，回滚 =
-- 恢复快照；词表无 CHECK，约束语义由 state 层词表常量承载——与 00005 同款）。

-- +goose Up

-- 平台管理的 git 公钥（SSH push 认证；v0.1 全局级 key，admin scope 管理，
-- 可推所有 app 仓库；per-app key 留 v0.2）。fingerprint = SHA256 指纹
-- （ssh-keygen -lf 同格式，"SHA256:<base64>"）——SSH 认证按指纹匹配，
-- public_key 本体为公开材料入库（list 展示与指纹复算），私钥永不经过平台。
CREATE TABLE git_keys (
    id          TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    public_key  TEXT NOT NULL,
    key_type    TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

-- git push 触发分支（app 配置分支，默认 main；push 到其他分支只收不发，
-- webhook 分支过滤共用此列）。
ALTER TABLE apps ADD COLUMN git_branch TEXT NOT NULL DEFAULT 'main';

-- per-app webhook 签名密钥（HMAC-SHA256）。NULL = 未配置即未启用（该
-- app 的 webhook 端点 404 语义）。值以平台 envelope 密文落库（age），
-- 明文不落库、永不回读——show 面只回 configured 位。
ALTER TABLE apps ADD COLUMN webhook_secret TEXT;

-- webhook 拉源配置（remote url + 认证形态 + 认证材料）。
-- auth_kind 词表（无 CHECK，state 层承载）：none | https_token | ssh_key；
-- auth_secret 为 envelope 密文（'' = 无），明文材料不落库。
ALTER TABLE apps ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN source_auth_kind TEXT NOT NULL DEFAULT 'none';
ALTER TABLE apps ADD COLUMN source_auth_secret TEXT NOT NULL DEFAULT '';

-- 部署来源（T2.19 绑定口径：deployments.source 字段记录 git 触发来源）。
-- sha = 40 位 commit；ref = refs/heads/<branch>。空串 = API/CLI 直传
-- compose 的部署（非 git 触发）。(app, sha) 维度的 webhook 幂等去重与
-- 轨迹回查均以此两列为据（索引配套）。
ALTER TABLE deployments ADD COLUMN source_git_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN source_git_ref TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_deployments_app_source_sha ON deployments (app_id, source_git_sha);
