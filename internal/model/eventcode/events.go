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

	// 跨 Project peer 声明三拍（F1.8，ADR-0013 附录 A.1：双向声明、接收方
	// 批准、撤销即时隔离）。
	{Name: "network.peer_declared", Summary: "A peer project declared intent to attach to a network (pending; needs receiver approval).", Source: "internal/api/fleetlygrpc/structure.go DeclareNetworkPeer"},
	{Name: "network.peer_approved", Summary: "The receiving project approved a peer attachment; references become projectable.", Source: "internal/api/fleetlygrpc/structure.go ApproveNetworkPeer"},
	{Name: "network.peer_revoked", Summary: "A peer attachment was revoked; existing attachments are isolated by an immediate isolate reconverge (ADR-0013 appendix A.4).", Source: "internal/api/fleetlygrpc/structure.go RevokeNetworkPeer"},

	// Git 触发（F0.13 webhook 接收链）。
	{Name: "hook.push_accepted", Summary: "A verified webhook push triggered a deployment.", Source: "internal/api/fleetlygrpc/webhook.go handlePush"},

	// Task 状态机（F1.5/F1.6，ADR-0012/0025：双形态程序化工作负载）。
	{Name: "task.created", Summary: "A task was created (one-shot or resident form).", Source: "internal/engine/events.go EventTaskCreated (emitted by the acceptance surface)"},
	{Name: "task.active", Summary: "A drained task was revived by an owner lease renewal.", Source: "internal/engine/task.go RenewTask"},
	{Name: "task.updated", Summary: "A task's desired concurrency was updated.", Source: "internal/engine/task.go ScaleTask"},
	{Name: "task.draining", Summary: "A task started draining: replenishment stopped; in-flight runs stop with grace or run out their TTL.", Source: "internal/engine/task.go drainTask"},
	{Name: "task.completed", Summary: "A one-shot task's run completed successfully.", Source: "internal/engine/task.go oneshotTerminal"},
	{Name: "task.failed", Summary: "A one-shot task's run failed.", Source: "internal/engine/task.go oneshotTerminal"},
	{Name: "task.drained", Summary: "A draining task reached drained state (all runs terminal).", Source: "internal/engine/task.go driveTask"},
	{Name: "task.deleted", Summary: "A task was deleted (carriers removed, row tombstoned).", Source: "internal/engine/task.go DeleteTask"},

	// Run 状态机（pending → running → stopping → stopped | failed，ADR-0012）。
	{Name: "run.created", Summary: "A run was created and queued for carrier ensure.", Source: "internal/engine/task.go createRun"},
	{Name: "run.running", Summary: "A run's workload was observed running.", Source: "internal/engine/taskobs.go handleRunObservation"},
	{Name: "run.stopping", Summary: "A run started stopping (stop reason carried on the row).", Source: "internal/engine/task.go stopRunRow"},
	{Name: "run.stopped", Summary: "A run reached the stopped terminal state.", Source: "internal/engine/taskobs.go handleRunObservation"},
	{Name: "run.failed", Summary: "A run reached the failed terminal state.", Source: "internal/engine/taskobs.go handleRunObservation"},

	// Owner Lease（F1.6：resident 池的心跳租约事实；deadline 绝对 RFC3339）。
	{Name: "lease.renewed", Summary: "A task's owner lease was renewed (deadline advanced).", Source: "internal/engine/task.go RenewTask"},
	{Name: "lease.expired", Summary: "A task's owner lease expired past grace; the pool drains.", Source: "internal/engine/task.go driveTask"},

	// Schedule 状态机（F1.7，ADR-0018 时区 cron：周期触发规则，到期拍从
	// 冻结模板铸 one-shot Task）。
	{Name: "schedule.created", Summary: "A schedule was created (timezone-aware cron, first fire time computed).", Source: "internal/engine/events.go EventScheduleCreated (emitted by the acceptance surface)"},
	{Name: "schedule.fired", Summary: "A schedule fired and spawned a one-shot task (source: cron or manual).", Source: "internal/engine/schedule.go spawnScheduleTask"},
	{Name: "schedule.skipped", Summary: "A due schedule fire was skipped (reason: overlap while the previous run is in flight, or quota_exceeded when the project is at its task quota).", Source: "internal/engine/schedule.go fireSchedule/skipSchedule"},
	{Name: "schedule.deleted", Summary: "A schedule was deleted (tombstoned; already-spawned tasks run to completion).", Source: "internal/api/fleetlygrpc/automation.go DeleteSchedule"},

	// Change Freeze（F1.9，ADR-0017 附录 A.3：变更冻结窗 set/lift——管理
	// 动作有界入册；命中拒绝不落事件（重试风暴自放大防护））。
	{Name: "freeze.set", Summary: "A change freeze was set for a team (or globally); change verbs in scope are refused with the reason.", Source: "internal/api/fleetlygrpc/governance.go SetChangeFreeze"},
	{Name: "freeze.lifted", Summary: "A change freeze was lifted; change verbs in scope are accepted again.", Source: "internal/api/fleetlygrpc/governance.go LiftChangeFreeze"},
}
