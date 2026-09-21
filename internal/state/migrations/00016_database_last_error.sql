-- E4 数据库托管——db_instances.last_error 列（managed-databases 设计 §2.1
-- 「provisioning → failed 收敛失败保留现场」的诚实诊断面，迁移 00016，只加
-- 法纪律）：库收敛器（internal/database，W4-S2）在健康门超时/镜像不可得/
-- 引擎错误等收敛失败时把人读原因写入本列（单行化、零凭据材料——错误文本
-- 纪律同 db_backups.error），EnterDbPhase 落 failed 与 reason 写入由收敛器
-- 分两步提交（状态机单写点不承载自由文本；reason 是诊断标注非状态位）。
-- ready 恢复（retry 重收敛通过健康门）时清空——列语义 = 最近一次失败的
-- 原因快照，'' = 当前无失败现场。
--
-- S1 备选口径的落地理由（S2 票据裁决）：failed 诊断是用户面需求（API view
-- last_error 字段 + fleetly databases get 展示），内存态随控制面重启丢失
-- ——failed 现场恰恰要跨重启存续（显式 retry/人工介入前一直是现场），故
-- 持久列而非内存。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
ALTER TABLE db_instances ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

-- delete_volumes 是删除受理时的卷处置选择（delete API 的两段式承载：API
-- 受理 → deleting 落位同事务置位 → reap duty 按位处置——默认 0 = 保留转
-- orphaned；1 = 删底座卷 + 台账记 discarded。数据处置选择必须跨重启存续
-- ——与 tombstone 两拍锚同列级纪律）。
ALTER TABLE db_instances ADD COLUMN delete_volumes INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE db_instances DROP COLUMN delete_volumes;
ALTER TABLE db_instances DROP COLUMN last_error;
