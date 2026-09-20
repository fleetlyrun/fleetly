-- T2-5a（T2.10 发布状态机与对账核心 + T2.11 窗口与失败语义）加法迁移：
-- 只新增列与索引，不改/不删既有列（架构 §2.8 契约版本化纪律：迁移只加法，
-- 回滚 = 恢复快照；ALTER 不能改约束、词表冻结于既有 CHECK）。

-- +goose Up

-- 发布状态机的行驱动字段（release-semantics §2.3）。status 列（00001 无
-- CHECK）承载状态机主状态词表：queued → preparing → building → releasing →
-- observing → succeeded | failed | cancelled；releasing 的 blocked_waiting
-- 子状态落 phase 列（不动 status 词表）。
ALTER TABLE deployments ADD COLUMN phase TEXT NOT NULL DEFAULT '';

-- 失败分流唯一判据（release-semantics §2.3/D-REL-4）：首个目标实例健康
-- （切流点）；NULL = 未切流。
ALTER TABLE deployments ADD COLUMN first_healthy_at INTEGER;

-- 同记录恢复记录（归位不建新 deployment，D-REL-7）：restore = 已归位重放；
-- blocked = 引擎不可达/恢复被阻塞。kind=rollback 的新记录不落此列。
ALTER TABLE deployments ADD COLUMN recovery TEXT;

-- 已切流观察窗失败的判定（verdict=unstable；词表只此一个——unstable 仅为
-- deployment verdict，不进 app 状态词表，state-model §2.10）。
ALTER TABLE deployments ADD COLUMN verdict TEXT;

-- stop-first 停机如实账（release-semantics §2.6）：判定→归位完成窗口。
ALTER TABLE deployments ADD COLUMN downtime_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN downtime_started_at INTEGER;
ALTER TABLE deployments ADD COLUMN downtime_ended_at INTEGER;

-- L2 看门狗（deployTimeout=300s；2026-09-20 更名前为 releaseTimeout）：
-- releasing 起点 + 当前 deadline
-- （blocked_waiting 暂停计时 = 恢复时按暂停时长顺延重写）。
ALTER TABLE deployments ADD COLUMN release_started_at INTEGER;
ALTER TABLE deployments ADD COLUMN watchdog_deadline_at INTEGER;

-- L3 观察窗（observe=60s）起点（切流即起算）。
ALTER TABLE deployments ADD COLUMN observe_started_at INTEGER;

-- 期望态快照与哈希（release-semantics §2.4 单层重放的执行依据）：
--   * spec_hash          —— 归一化 compose 的 spec_hash（compose 层）；
--   * env_snapshot_hash  —— env 三层合并结果快照哈希（key:sha256+来源，
--                           值本身不落）；
--   * desired_hash       —— spec + env + 镜像 digest + secret 引用的合成
--                           期望态哈希（对账/漂移判据，state-model §2.5）；
--   * desired_spec       —— 目标 Swarm 服务集快照（box envelope 密文 JSON：
--                           含合并 env 明文——重放/归位的唯一依据，明文纪律
--                           与 env_vars 同：只以密文落库）；
--   * compose_path       —— 入队 compose 文件绝对路径（preparing 重载归一
--                           化：daemon 侧二次校验 + env 明文提取）。
ALTER TABLE deployments ADD COLUMN spec_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN env_snapshot_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN desired_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN desired_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN compose_path TEXT NOT NULL DEFAULT '';

-- cancel 请求位（CLI 置位、引擎消费执行归位；拒绝 = 曾健康，清位并审计）。
ALTER TABLE deployments ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;

-- 部署级告警/标志位（bitmask，只增位不回收）：
--   bit0 = post_window_alerted —— L4 只告警一次落库位（观察窗后不稳定，
--          release-semantics §2.2：只告警、不计数升级）；
--   bit1 = instability_warning —— 观察窗警告通过（W_DEPLOY_INSTABILITY，
--          §2.2 判定细则「单次退出且窗末自愈」；app=degraded 的来源之一，
--          state-model §2.10）。
ALTER TABLE deployments ADD COLUMN flags INTEGER NOT NULL DEFAULT 0;

-- 非终态扫描（控制面重启恢复 + 互斥检查的候选集）。
CREATE INDEX idx_deployments_status ON deployments (status);

-- app 派生状态缓存（state-model §2.10：running/degraded/blocked/down，
-- 优先级 down > blocked > degraded > running；落库值是事件触发的比较基准
-- ——进入/退出 degraded 的 app.degraded/app.recovered 由此判定，读面可
-- 随时由部署记录 + placement 状态重推导）。
ALTER TABLE apps ADD COLUMN derived_state TEXT NOT NULL DEFAULT '';
