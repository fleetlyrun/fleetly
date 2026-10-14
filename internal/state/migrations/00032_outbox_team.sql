-- +goose Up
-- 事件 Team 轴（P0-2，S2 跨队事件流泄漏止血）：team_id 是资源轴归属
-- Team（资源所属 Project 的 Team），'' = 平台级事件（对全部 Team 可见）
-- ——口径与 audit 面 team 轴一致（ADR-0035 决策 6）。读面三径
-- （ListEvents / StreamEvents / SSE 票据）按 team_id = ? OR team_id = ''
-- 过滤；写点逐步补铸，未铸写点落 ''（平台级可见，不劣于止血前行为），
-- 全量补齐由 P2 事实 kernel 承载。team 是内部过滤列，不进 Event 消息
-- （proto 契约不动）。不回填存量——旧行无归属事实，回填即编造（同
-- 00017 口径）。索引 (team_id, seq) 服务过滤后的升序重放。
ALTER TABLE outbox ADD COLUMN team_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_outbox_team_seq ON outbox(team_id, seq);

-- +goose Down
-- goose 只前滚（架构 §8）。
