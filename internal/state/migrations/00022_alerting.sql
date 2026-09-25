-- 告警（B 线 W5 设计 §2，D-V3W5-1，v0.3 W5-S2；迁移只加法纪律）：
-- alert_rules（Prometheus 规则的平台权威态——平台级，vmalert rule 文件的
-- 渲染源）+ webhook_deliveries.alert_payload（告警投递载荷列，可空加列）。
--
-- 列口径（设计 §2.2 清单）：
--   alert_rules.id            ULID 主键；
--   name                      人读规则名 UNIQUE（**平台级**唯一——告警面向
--                             平台管理员，与 notifications 端点同域，设计
--                             §2.2「project 内唯一?平台级」的裁决形态）；
--   expr                      PromQL 表达式（非空、长度 ≤2048——state 写入
--                             通道校验；语法合法性由 vmalert/VM 裁决，平台
--                             不做 PromQL 解析——查询面透传口径同源）；
--   for_duration_seconds      告警持续时长（秒，≥0；0 = 规则无 for 子句。
--                             渲染为 Prometheus duration「Ns」形态）；
--   labels                    JSON 对象（string→string；含 severity——
--                             severity 进通知文案的源头）；空对象合法；
--   channels                  JSON 字符串数组（通知端点 id 集；空数组 =
--                             缺省投全部启用端点——接收器映射语义，设计
--                             §2.3；引用端点的存在性不在保存期校验——端点
--                             可后删，投递期解析缺失即跳过）；
--   created_at/updated_at     UnixNano（仓内时间列约定）。
--
--   webhook_deliveries.alert_payload 是告警投递的载荷列（§2.3「映射到既有
--   notify 投递」的台账承载）：事件投递行 payload 经 events 表按 event_seq
--   解析；告警投递**零事件**（防回环红线——vmalert 不经事件流），载荷物
--   化在台账行自身（NULL = 事件投递行，语义不变）。event_seq=0 是告警行
--   的哨兵值（事件 seq 自 1 起单调，0 恒不冲突）。
--
-- 通知零事件红线延伸：本迁移无任何事件面改动；alerting 全链（规则 CRUD/
-- mode 切换/告警投递）只落审计行，events 注册表零新增。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE alert_rules (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL UNIQUE,
    expr                 TEXT NOT NULL,
    for_duration_seconds INTEGER NOT NULL DEFAULT 0,
    labels               TEXT NOT NULL DEFAULT '{}',
    channels             TEXT NOT NULL DEFAULT '[]',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

ALTER TABLE webhook_deliveries ADD COLUMN alert_payload TEXT;

-- +goose Down
ALTER TABLE webhook_deliveries DROP COLUMN alert_payload;
DROP TABLE alert_rules;
