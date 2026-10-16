-- +goose Up
-- pre_deploy_jobs 词汇改名（ADR-0078）：deployments.first_boot 游标列更名
-- pre_deploy。值域与语义零变化：'' = 未开始/无 job；'<idx>:<taskID>' =
-- 等待第 idx 个 job 的 Task 终态；'done' = 全部完成。旧名与业界
-- "first boot = 生命周期仅一次" 固定含义相撞（实际语义是每次部署都执行）。
ALTER TABLE deployments RENAME COLUMN first_boot TO pre_deploy;

-- +goose Down
ALTER TABLE deployments RENAME COLUMN pre_deploy TO first_boot;
