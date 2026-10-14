-- +goose Up
-- ADR-0048 决策 1/2：blue-green 双代窗需要基线 Generation 锚——窗口
-- Ensure 的调用 gen = from_generation（旧代成员标签不翻新、旧代载体名/
-- ID 原样重现），服务代推导（observing = 已切换）也读它。存量行回填 0
-- = 无基线代（rolling 存量部署语义不变；首个 blue-green 部署起受理位
-- 从最近 succeeded 行取 gen 落列）。
ALTER TABLE deployments ADD COLUMN from_generation INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE deployments DROP COLUMN from_generation;
