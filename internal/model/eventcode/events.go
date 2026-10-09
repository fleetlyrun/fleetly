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
	{Name: "deployment.first_boot_job", Summary: "A deploy-time first boot job was minted as a one-shot task; the deployment waits in releasing for its terminal state before materializing carriers (ADR-0030).", Source: "internal/engine/firstboot.go mintFirstBootJob"},

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

	// 滚动停摆观测（runbook 记录·二十七：Ensure 已被编排器接受但滚动停在
	// 中间态——swarm paused 形态下 spec 面恒一致，spec drift 无感；僵尸
	// task 叠加三天无人感知的观测面收口。只观测不纠正，处置 = 人工 resume
	// 或重部署）。
	{Name: "workload.rollout_stalled", Summary: "A runtime rollout is stalled mid-flight (e.g. swarm update paused by task failure); spec matches, so spec-drift cannot see it.", Source: "internal/engine/drift.go compareSpecs"},

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
	// 共享变量（F2.9，ADR-0043）：值明文可回显，事件只记事实不带值——
	// 与审计行同口径。
	{Name: "variable.updated", Summary: "A project shared variable was set (values are readable via the API; redeploy affected apps to pick up the new value).", Source: "internal/api/fleetlygrpc/structure.go PutSharedVariable"},
	{Name: "variable.deleted", Summary: "A project shared variable was deleted.", Source: "internal/api/fleetlygrpc/structure.go DeleteSharedVariable"},
	{Name: "volume.created", Summary: "A volume was created.", Source: "internal/api/fleetlygrpc/structure.go CreateVolume"},
	{Name: "network.created", Summary: "A project network was created.", Source: "internal/api/fleetlygrpc/networks.go CreateNetwork"},

	// 跨 Project peer 声明三拍（F1.8，ADR-0013 附录 A.1：双向声明、接收方
	// 批准、撤销即时隔离）。
	{Name: "network.peer_declared", Summary: "A peer project declared intent to attach to a network (pending; needs receiver approval).", Source: "internal/api/fleetlygrpc/networks.go DeclareNetworkPeer"},
	{Name: "network.peer_approved", Summary: "The receiving project approved a peer attachment; references become projectable.", Source: "internal/api/fleetlygrpc/networks.go ApproveNetworkPeer"},
	{Name: "network.peer_revoked", Summary: "A peer attachment was revoked; existing attachments are isolated by an immediate isolate reconverge (ADR-0013 appendix A.4).", Source: "internal/api/fleetlygrpc/networks.go RevokeNetworkPeer"},

	// 网络重建（ADR-0046，N2 评审批 P1-4）：平台中介的载体网络重建——
	// 存量非 attachable 项目网的 flag-day 通道（detach→rm→recreate→
	// re-attach 计数在载荷；失败无事件，行级错误即事实面）。
	{Name: "network.rebuilt", Summary: "A project network carrier was rebuilt through the platform in the attachable form; attached carriers were detached and re-attached.", Source: "internal/api/fleetlygrpc/networks.go RebuildNetwork"},

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

	// 上传产物（F1.10，ADR-0019 附录 A：内容寻址构建材料接入；重传去重
	// 不落第二行事实）。
	{Name: "upload.stored", Summary: "An uploaded source was stored (content-addressed; payload carries id, digest, size and deduplicated).", Source: "internal/api/fleetlygrpc/uploads.go UploadSource"},

	// 托管数据服务（F1.12，ADR-0029：状态迁移只落 created/deleted——观测
	// 状态由 status 列承载，停机告警走既有 workload.stopped 稳态看门狗）。
	{Name: "database.created", Summary: "A database was created from a template; the platform minted its credential secret (value never returned).", Source: "internal/api/fleetlygrpc/databases.go CreateDatabase"},
	{Name: "database.deleted", Summary: "A database was deleted (carriers torn down, row tombstoned; volume and credential secret retained as project materials).", Source: "internal/api/fleetlygrpc/databases.go DeleteDatabase"},

	// Backup 执行链（F2.2，ADR-0039：成功携带 ObjectStore 回执三元组；
	// 恢复失败不落事件——行 restore_error 是事实面）。
	{Name: "database.backup_succeeded", Summary: "A database backup completed; the payload carries the object key, sha256 digest and size (the restore-verification anchors).", Source: "internal/engine/backup.go executeOneBackup"},
	{Name: "database.backup_failed", Summary: "A database backup failed; the payload carries the error tail (utility container stderr included).", Source: "internal/engine/backup.go executeOneBackup"},
	{Name: "database.restored", Summary: "A database restore completed (stream into a running target or volume pre-seeding before first start).", Source: "internal/engine/backup.go restoreDatabase"},

	// Platform Backup（F2.2，ADR-0039：restic 链整体成败；快照细节在仓库自身）。
	{Name: "platform.backup_succeeded", Summary: "A platform backup (restic snapshot of the control-plane data root) completed on all configured repos.", Source: "internal/engine/backup.go platformBackupPass"},
	{Name: "platform.backup_failed", Summary: "A platform backup failed; the payload carries the restic error tail.", Source: "internal/engine/backup.go platformBackupPass"},

	// 阈值告警（F2.5，ADR-0041 决策 3：状态迁移沿才落——不逐拍轰炸；
	// resolved 携带同一规则锚）。
	{Name: "alert.fired", Summary: "A threshold alert rule transitioned to firing (breach held for the rule's for-window); notification channels were attempted.", Source: "internal/engine/metrics.go evaluateRules"},
	{Name: "alert.resolved", Summary: "A firing threshold alert rule transitioned back to ok (observed value fell below the threshold).", Source: "internal/engine/metrics.go evaluateRules"},
	{Name: "alert.channel_failed", Summary: "A notification channel delivery failed; the channel row records the error tail (diagnostics face).", Source: "internal/engine/metrics.go dispatchAlert"},

	// Exec 会话（F3.2，ADR-0049 决策 4：安全可见性——谁在何时进入了哪个
	// 进程；会话不是资源行，受理即唯一事件/审计落点，结束不落第二行）。
	{Name: "exec.session_opened", Summary: "An exec session was accepted into a running workload (actor, process, instance, node and command are in the payload; change freeze is exempt — diagnostics face).", Source: "internal/api/fleetlygrpc/exec.go CreateExecSession"},

	// App 模板（F3.3，ADR-0050）：实例化摘要事件——伴生资源各自的
	// app.created/secret.updated/database.created/route 事件由 create-or-reuse
	// 步骤自然发射，本事件钉"哪个模板在哪次部署落了地"的审计锚。
	{Name: "template.instantiated", Summary: "A template was instantiated into a deployment (payload carries the template name@version and the deployment id).", Source: "internal/api/fleetlygrpc/templates.go InstantiateTemplate"},
	// 目录刷新（F3.3，ADR-0050 决策 4）：操作员动词的事实面——快照 digest
	// 前后对照是"目录何时被谁换成什么"的唯一台账。
	{Name: "templates.refreshed", Summary: "The template catalog snapshot was refreshed (payload carries the previous and new aggregate digests and the entry count).", Source: "internal/api/fleetlygrpc/templates.go RefreshTemplates"},

	// 数据浏览器（F3.6，ADR-0051 决策 1：安全可见性——谁在何时打开了哪个
	// Database 的浏览器会话；会话非资源行，受理即唯一事件/审计落点，回收
	// 不落第二行）。
	{Name: "database.browser_opened", Summary: "A browse session was opened for a database (payload carries the browser tool, read-only flag and enforcement tier; change freeze is exempt — diagnostics face).", Source: "internal/api/fleetlygrpc/databases.go BrowseDatabase"},
}
