-- 自动扩缩（B 线 W5 设计 §1，D-V3W5-2，v0.3 W5-S1；迁移只加法纪律）：
-- scaling_policies（per app × per compose service 的扩缩策略权威态）+
-- scaling_replica_overrides（运行期副本覆盖层——autoscaler 平台写通道的
-- 期望落点）。
--
-- 列口径（设计 §1.1/§1.2 清单）：
--   scaling_policies.app_id           应用行 FK（apps tombstone 生命周期——
--                                     行永不 DELETE，无级联面）；
--   service                           compose 服务名（用户面权威态键；
--                                     [A-Za-z0-9._-] 词表由 state 写入通道
--                                     校验——naming.validateComponent 同字
--                                     符集，校验在本包单点）；
--   min_replicas / max_replicas       副本下限/上限（min ≥1、max ≤16——
--                                     swarm 单服务上限口径，防误配爆炸；
--                                     max ≥ min；词表由 state 层写入通道
--                                     双保险，SQLite CHECK 不改词表——
--                                     webhook_deliveries.status 同纪律）；
--   target_cpu_pct / target_mem_pct   扩缩目标水位（百分数 ∈ [20,90]；
--                                     0 = 该维度不设目标——至少一维必设，
--                                     校验在 state 层）；
--   cooldown_seconds                  冷却窗（∈ [60,3600]s；缺省 180）；
--   created_at/updated_at             UnixNano（仓内时间列约定）。
--
--   scaling_replica_overrides 是设计 §1.2「spec 的运行期副本覆盖层」的落点：
--   autoscaler 调整副本 = 底座 ServiceUpdate + 本表 upsert（deployment_id 钉
--   期望态来源部署）。漂移对账以「平台自己写的运行期副本」为期望（覆盖行
--   deployment_id == 期望来源部署时生效），外部 docker service scale 的漂移
--   判据不因此误报、也不被吞——部署换代后旧覆盖自然失活。service 列是
--   compose 服务名（与 scaling_policies 同键域——策略撤销联动清场按同键
--   命中；引擎侧经服务 label fleetly.process 与 Swarm 服务名互译）。
--
-- Down 仅供 goose 演练；生产回滚 = 恢复快照（架构 §2.8 契约版本化纪律）。

-- +goose Up
CREATE TABLE scaling_policies (
    app_id           TEXT NOT NULL REFERENCES apps (id),
    service          TEXT NOT NULL,
    min_replicas     INTEGER NOT NULL,
    max_replicas     INTEGER NOT NULL,
    target_cpu_pct   INTEGER NOT NULL DEFAULT 0,
    target_mem_pct   INTEGER NOT NULL DEFAULT 0,
    cooldown_seconds INTEGER NOT NULL DEFAULT 180,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    PRIMARY KEY (app_id, service)
);

CREATE TABLE scaling_replica_overrides (
    app_id        TEXT NOT NULL REFERENCES apps (id),
    service       TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    replicas      INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (app_id, service)
);

-- +goose Down
DROP TABLE scaling_replica_overrides;
DROP TABLE scaling_policies;
