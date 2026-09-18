-- T2-4（T2.8 构建管线产品化 + T2.9 镜像身份）加法迁移：只新增表与索引，
-- 不改已有表（架构 §2.8 契约版本化纪律：迁移只加法，回滚 = 恢复快照）。

-- +goose Up

-- 历史与叙事：构建记录（架构 §2.2 构建行、task-breakdown T2.8）。状态机
-- queued → building → succeeded | failed（终态不可逆；无 cancel，v0.1 切面）。
--   * app_id 引用 apps（FK）；
--   * driver 取 railpack | dockerfile | passthrough（仅 image 模式无构建直通
--     时不建行，driver=passthrough 预留给直通记录形态，当前不产生）；
--   * image_digest 是不可变镜像 ID（`sha256:<hex>` 配置摘要，D9：部署一律以
--     `名称@sha256:` 引用；digest→ref 映射即本表 image_digest ↔ image_ref）；
--   * request 是构建输入的 JSON（归一化请求：context 绝对路径、dockerfile
--     相对路径、spec_hash、secrets_hash——daemon 队列 worker 的执行输入，
--     CLI 入队与 daemon 执行跨进程的唯一通道）；
--   * plan_path/log_path 是构建产物归档（railpack plan JSON / 构建日志），
--     目录由 config build.artifacts_dir 提供；
--   * error_code 为终态失败时的注册表错误码（E_BUILD_FAILED 等）。
-- 事件取舍：eventcode 注册表（35 个）无 build.* 事件名且注册表只增——构建
-- 状态不广播新事件，可观测性经本表（fleetly builds list）与审计
-- （build.start / build.finish，与状态迁移同事务）承载。
CREATE TABLE builds (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL REFERENCES apps (id),
    service      TEXT NOT NULL,
    driver       TEXT NOT NULL CHECK (driver IN ('railpack', 'dockerfile', 'passthrough')),
    status       TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'building', 'succeeded', 'failed')),
    image_ref    TEXT NOT NULL DEFAULT '',
    image_digest TEXT NOT NULL DEFAULT '',
    request      TEXT NOT NULL DEFAULT '{}',
    plan_path    TEXT NOT NULL DEFAULT '',
    log_path     TEXT NOT NULL DEFAULT '',
    error_code   TEXT,
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER
);
CREATE INDEX idx_builds_app_created ON builds (app_id, created_at);
CREATE INDEX idx_builds_status ON builds (status);
CREATE INDEX idx_builds_digest ON builds (image_digest);
