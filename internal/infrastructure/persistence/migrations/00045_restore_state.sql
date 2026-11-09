-- +goose Up
-- 恢复显式终态机（ADR-0039 决策 6 修订，2026-11-10）：databases/pubsubs
-- 增 restore_state 列——''（无恢复）| pending | running | succeeded |
-- failed。旧语义「restore_from_backup 在场 = 恢复挂起」无法区分在途与
-- 终态：permanent 失败每拍重试、半恢复态静默收敛。新语义下：
--   * restore_from_backup 保留为**源历史**（成功/失败都不清位）；
--   * pending/running 是备份环枚举的在途活态；
--   * succeeded/failed 是终态——failed 即终止重试，preseed 门控继续挡住
--     Ensure（半恢复卷不得首启）；
--   * 重启打断的 running 由启动清扫回退 pending（重试一次是安全上界）。
--
-- 回填：存量行 restore_from_backup 在场 = 旧语义的在途恢复 → pending
-- （旧失败路径已清位，故非空即挂起）。pubsubs 表与 databases 同形。
ALTER TABLE databases ADD COLUMN restore_state TEXT NOT NULL DEFAULT '';
ALTER TABLE pubsubs ADD COLUMN restore_state TEXT NOT NULL DEFAULT '';

UPDATE databases SET restore_state = 'pending' WHERE restore_from_backup != '';
UPDATE pubsubs SET restore_state = 'pending' WHERE restore_from_backup != '';

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
