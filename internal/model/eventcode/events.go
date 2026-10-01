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
	// 收敛默认 opt-in，ADR-0005；ADR-0022 升级为 gen 偏离 + spec 失配双路径）。
	{Name: "workload.drift_detected", Summary: "Observed workload diverged from the ensured generation or spec (ADR-0022).", Source: "internal/engine/observ.go detectDrift + internal/engine/drift.go compareSpecs"},

	// 稳态看门狗（ADR-0022：最近部署 succeeded 的 App 在当前 Generation
	// 观测到 stopped——只观测不迁移，处置由人/Agent 决定）。
	{Name: "workload.stopped", Summary: "A steady-state workload was observed stopped at the current generation.", Source: "internal/engine/drift.go emitSteadyStateStopped"},

	// Identity & Access（F0.5~F0.7 账号批）。
	{Name: "user.created", Summary: "A user was created and granted a role in a team.", Source: "added during implementation"},
	{Name: "team.created", Summary: "A team was created.", Source: "added during implementation"},
	{Name: "role.created", Summary: "A custom role was created from a scope set.", Source: "added during implementation"},
	{Name: "token.created", Summary: "A token was minted (secret shown once at creation).", Source: "added during implementation"},
	{Name: "token.revoked", Summary: "A token was revoked; its next call will be rejected.", Source: "added during implementation"},
	{Name: "invitation.created", Summary: "An invitation was issued (single-use, time-boxed, role-bound).", Source: "added during implementation"},
	{Name: "invitation.accepted", Summary: "An invitation was redeemed; the invitee became a user.", Source: "added during implementation"},

	// 结构面写操作（C5 补齐："一切状态迁移写 Event"——此前结构面只有
	// 审计无事件；与审计同事务落）。
	{Name: "project.created", Summary: "A project was created.", Source: "internal/api/fleetlygrpc/structure.go CreateProject"},
	{Name: "project.deleted", Summary: "A project was deleted (tombstoned).", Source: "internal/api/fleetlygrpc/structure.go DeleteProject"},
	{Name: "app.created", Summary: "An app was created.", Source: "internal/api/fleetlygrpc/structure.go CreateApp"},
	{Name: "app.deleted", Summary: "An app was deleted (teardown per ADR-0023).", Source: "internal/api/fleetlygrpc/structure.go DeleteApp"},
	{Name: "app.teardown_aborted", Summary: "An app delete was aborted after teardown because a deployment was admitted mid-delete; the in-flight deployment rebuilds the carriers (ADR-0023).", Source: "internal/api/fleetlygrpc/structure.go DeleteApp (recordTeardownAbort)"},
	{Name: "secret.updated", Summary: "A secret value was set (fingerprint, never the value).", Source: "internal/api/fleetlygrpc/structure.go PutSecret"},
	{Name: "secret.deleted", Summary: "A secret was deleted.", Source: "internal/api/fleetlygrpc/structure.go DeleteSecret"},
	{Name: "config.updated", Summary: "A config version was written.", Source: "internal/api/fleetlygrpc/structure.go PutConfig"},
	{Name: "volume.created", Summary: "A volume was created.", Source: "internal/api/fleetlygrpc/structure.go CreateVolume"},
	{Name: "network.created", Summary: "A project network was created.", Source: "internal/api/fleetlygrpc/structure.go CreateNetwork"},

	// Git 触发（F0.13 webhook 接收链）。
	{Name: "hook.push_accepted", Summary: "A verified webhook push triggered a deployment.", Source: "internal/api/fleetlygrpc/webhook.go handlePush"},
}
