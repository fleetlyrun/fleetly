-- +goose Up
-- ADR-0074 决策 7：compose 受理语言的原文随 Revision 冻结（其他源空串）
-- ——用户亲笔部署伪影可回读（console Spec tab 渲染，收 W-9 挂账）。内容
-- 寻址仍以 IR protojson 为锚（source_text 不参与 digest）：同 IR 不同注释
-- 的两份 compose 去重为一行，source_text 留首次冻结的原文（IR 等价优先，
-- provenance 歧义在此口径下接受）。存量行回填空串 = 无原文（历史冻结
-- 只有 IR，诚实边界）。
ALTER TABLE revisions ADD COLUMN source_text TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE revisions DROP COLUMN source_text;
