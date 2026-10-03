-- +goose Up
-- 部署幂等键收窄到 App 作用域（N1 审查 B10）：idx_deployments_idem_active
-- 的唯一维度只有键本身——admission 的 FindActiveByIdempotencyKey 按键全局
-- 查，跨 App 复用同键时后到 App 的受理被静默去重成先到 App 的在途部署
-- （响应跨 App 串台）。幂等键是调用方原文透传（Deploy RPC 的
-- idempotency_key，服务端不加随机后缀），客户端模板复用导致跨 App 撞键
-- 是现实形态，作用域必须由平台钉死在 App 维。
--
-- 存量论证：新索引 (app_id, idempotency_key) 是旧索引 (idempotency_key)
-- 的松弛——旧全局唯一成立的任意数据集必然满足 App 内唯一（同 App 同键
-- 活跃双行是旧索引已禁止的真子集），故 CREATE 无需清数必然成立，无需
-- backfill。先建新后删旧：唯一防线零空窗（00016 是删后建——方向相反：
-- 那次是收紧且 host 数据已保证唯一，本次新索引恒可建，建后删才无空窗）。
CREATE UNIQUE INDEX idx_deployments_app_idem_active ON deployments(app_id, idempotency_key)
  WHERE idempotency_key != ''
    AND state IN ('queued','preparing','building','releasing','observing','rolling-back');
DROP INDEX idx_deployments_idem_active;

-- +goose Down
-- goose 只前滚（架构 §8）。
