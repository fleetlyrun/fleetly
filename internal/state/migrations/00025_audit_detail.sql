-- +goose Up
-- audit Detail 列（ADR-0049）——动作详情 JSON 面，首用 =
-- exec.session 行的进程/实例/节点/命令（exec 审计的命令面入账）。
-- 既有行 detail 空串（零值兼容，查询面 only-add）。
ALTER TABLE audit ADD COLUMN detail TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit DROP COLUMN detail;
