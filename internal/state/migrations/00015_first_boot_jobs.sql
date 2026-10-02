-- +goose Up
-- firstBootJobs 部署链接线（ADR-0030）：deployments 行加 first_boot 游标列。
-- 值域：'' = 未开始/无 job；'<idx>:<taskID>' = 等待第 idx 个 job 的 Task 终态
-- （0 基，铸造与游标推进同事务——游标非空 ⇒ Task 行必在）；'done' = 全部完成
-- （此后 carrier 子相位，重启不再重铸）。observe_deadline 承载三相位截止
-- （job 等待 / L1 / L3 观察窗），消歧真源 = 本列。
ALTER TABLE deployments ADD COLUMN first_boot TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE deployments DROP COLUMN first_boot;
