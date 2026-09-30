package eventcode

// builtins 是事件集（只增）。首批随部署链 state 批次入册：Outbox 落库
// （internal/state/outbox）是首个消费方；各事件名的生产发射点随 engine
// admission/构建批次（下一 commit）接入，接入后在 usage_test 豁免清单
// 移除对应条目。
var builtins = []Event{
	// Deployment 状态机迁移（领域模型 §4；含 admission 排队态）。
	{Name: "deployment.queued", Summary: "Deployment accepted by admission queue.", Source: "added during implementation"},
	{Name: "deployment.preparing", Summary: "Deployment started preparing spec and materials.", Source: "added during implementation"},
	{Name: "deployment.building", Summary: "Deployment entered build stage.", Source: "added during implementation"},
	{Name: "deployment.releasing", Summary: "Deployment started releasing workloads to runtime.", Source: "added during implementation"},
	{Name: "deployment.observing", Summary: "Deployment entered health observation window.", Source: "added during implementation"},
	{Name: "deployment.succeeded", Summary: "Deployment reached succeeded terminal state.", Source: "added during implementation"},
	{Name: "deployment.failed", Summary: "Deployment reached failed terminal state.", Source: "added during implementation"},
	{Name: "deployment.rolling_back", Summary: "Deployment started replaying last successful revision.", Source: "added during implementation"},
	{Name: "deployment.superseded", Summary: "Deployment superseded by a newer one.", Source: "added during implementation"},
	{Name: "deployment.cancelled", Summary: "Deployment cancelled while queued or in flight.", Source: "added during implementation"},

	// Build 状态机迁移（领域模型 §4）。
	{Name: "build.queued", Summary: "Build accepted into build queue.", Source: "added during implementation"},
	{Name: "build.building", Summary: "Build started executing.", Source: "added during implementation"},
	{Name: "build.succeeded", Summary: "Build produced an image digest.", Source: "added during implementation"},
	{Name: "build.failed", Summary: "Build failed.", Source: "added during implementation"},
	{Name: "build.cancelled", Summary: "Build cancelled.", Source: "added during implementation"},
	{Name: "build.expired", Summary: "Build expired by timeout watchdog.", Source: "added during implementation"},

	// 集群观测（架构 §5 节点身份锚定；nodes 表是观测缓存）。
	{Name: "node.joined", Summary: "Node joined the cluster and was anchored with a platform node ID.", Source: "added during implementation"},
	{Name: "node.left", Summary: "Node left the cluster.", Source: "added during implementation"},

	// Drift 信号（per-Workload 粒度 + 去抖，2026-09-30 裁决；检测默认开、
	// 收敛默认 opt-in，ADR-0005）。
	{Name: "workload.drift_detected", Summary: "Observed workload state diverged from the ensured generation.", Source: "added during implementation"},
}
