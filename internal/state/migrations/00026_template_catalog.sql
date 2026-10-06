-- +goose Up
-- App 模板目录快照表（F3.3，ADR-0050 决策 4）：RefreshTemplates 成功后的
-- 单行快照（id 恒 1——快照是"当前服务目录"的整体替换语义，非聚合行集）。
-- body 是清单 JSON 原文（digest 已在写入前逐条核对——坏目录不落行）。
-- 内嵌目录不落库（随二进制）；本表在场即优先于内嵌目录。
CREATE TABLE template_catalog (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  digest     TEXT NOT NULL,
  body       TEXT NOT NULL,
  fetched_at TEXT NOT NULL
);

-- +goose Down
-- goose 只前滚（架构 §8）；Down 留空是有意为之。
