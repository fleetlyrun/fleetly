-- H11（架构评审：排队时长计入准备预算）加法迁移：deployments 新增
-- phase_started_at——queued → preparing 转换（引擎拾取）时刻的预算锚点。
--
-- 缺陷：准备/构建预算此前自 created_at（入队时间）起算，而同 app 互斥使
-- queued 行等待前序部署终态（releasing 300s + observing 60s，或
-- blocked_waiting 无上限）——排队超 300s 的部署被拾取后第一拍即假失败
-- E_RUNTIME_UNAVAILABLE（未触底座、无副作用，用户必须重发）。
--
-- 语义：锚点随拾取原子写入（CAS 补丁同拍），预算自拾取时刻起算，排队
-- 等待不计入；存量行（NULL）回落 created_at 保持旧语义。releasing 自有
-- release_started_at / watchdog_deadline_at 锚，与本列互不干扰——
-- blocked_waiting 等待期间锚点不重置也无消费（等待不计入预算即本意）。

-- +goose Up
ALTER TABLE deployments ADD COLUMN phase_started_at INTEGER;
