package errcode

// builtins 是文档域清单的全量录入（唯一真源为注册表；此处每码注明文档
// 出处）。分布核对：release-semantics §2.7（17 E + 3 W）、stateful-placement
// §2.8（8 E + 1 W）与 §2.9（1 E）、state-model §2.7/§2.9/§2.4/§2.2（4 E）、
// architecture §2.4（3 码）+ §2.3（E_STATE_VERSION_CONFLICT）。
// 计 35 个 E_ + 5 个 W_ = 40 码（文档外实现期新增三码：T2.15 的
// E_ROUTE_PUBLISH_FAILED、MG-C3 的 E_DEPLOY_CONFIRM_REQUIRED、M4-6 的
// E_TOKEN_LAST_ADMIN——见各自分节注记，待 T0.5 契约冻结确认）。
//
// HTTP 默认映射：文档显式给定的照文档（E_DOMAIN_CONFLICT/E_STATE_VERSION_
// CONFLICT/E_VOLUME_NODE_MISMATCH/E_PLACEMENT_MOVE_REQUIRES_ACK→409、
// E_EVENT_CURSOR_EXPIRED→410、E_LABEL_RESERVED/E_PLACEMENT_LABEL_CONFLICT/
// E_PLACEMENT_NODE_INVALID/E_PLACEMENT_NODE_NOT_FOUND→422）；文档未给定的
// 取保守缺省（请求契约违约→400、请求与状态冲突→409、底座/管线故障→500/
// 503），待 T0.5 契约冻结复核。
var builtins = []Code{
	// ── compose 契约（architecture §2.4、release-semantics §2.7/§2.8）──
	{ID: "E_COMPOSE_UNSUPPORTED", HTTP: 400,
		Summary:    "compose declaration falls outside the controlled subset/deny list; validation rejects it explicitly",
		Suggestion: "Remove or rewrite the unsupported compose fields and retry; see the error code docs for the controlled subset and deny list."},
	{ID: "E_COMPOSE_MANAGED_FIELD", HTTP: 400,
		Summary:    "compose managed-field value violates platform governance (failure_action/monitor)",
		Suggestion: "Set deploy.update_config.failure_action to pause (or omit it) and monitor to 5s (or omit it), then retry."},
	{ID: "E_COMPOSE_UNSAFE_STRATEGY", HTTP: 400,
		Summary:    "explicit start-first conflicts with platform-enforced stop-first (volumes/fixed host ports/global)",
		Suggestion: "Set deploy.update_config.order to start-first or omit it; services with volumes or fixed host ports are forced to stop-first by the platform."},

	// ── 域名/TLS（architecture §2.4）──
	{ID: "E_DOMAIN_CONFLICT", HTTP: 409,
		Summary:    "the same domain appears on two services of the same app",
		Suggestion: "The domain is already claimed by another service of this app: remove it from one of them before declaring it."},
	{ID: "E_DOMAIN_UNSUPPORTED", HTTP: 400,
		Summary:    "domain form unsupported (e.g. wildcards require DNS-01 from v0.2)",
		Suggestion: "Use a concrete domain; see the error code docs for wildcard and certificate support scope."},

	// ── 发布管线（release-semantics §2.7 场景矩阵）──
	{ID: "E_BUILD_FAILED", HTTP: 500,
		Summary:    "build stage failed (Railpack/BuildKit)",
		Suggestion: "Check the build logs to locate the compile/dependency error, fix it, and deploy again."},
	{ID: "E_IMAGE_PULL_FAILED", HTTP: 500,
		Summary:    "image pull failed (registry unreachable/invalid credentials/network failure)",
		Suggestion: "Verify the image reference and registry credentials are valid and the node can reach the registry, then retry."},
	{ID: "E_IMAGE_UNAVAILABLE", HTTP: 500,
		Summary:    "rollback target image is no longer available (digest stale/pruned)",
		Suggestion: "The rollback target image is no longer available: replay the most recent usable revision instead, or rebuild and deploy."},
	{ID: "E_SCHEDULER_PENDING_TIMEOUT", HTTP: 500,
		Summary:    "task stuck in PENDING past the deploy watchdog (placement constraints/insufficient resources/node unavailable)",
		Suggestion: "Check node resources and placement constraints (whether the bound node is ready); free resources or adjust constraints and retry."},
	{ID: "E_TASK_START_FAILED", HTTP: 500,
		Summary:    "new task failed to start (image entrypoint/resource limits/runtime error)",
		Suggestion: "Check the task logs to locate the start failure, fix it, and retry."},
	{ID: "E_HEALTH_TIMEOUT", HTTP: 500,
		Summary:    "health gate timeout: new task did not pass healthcheck within budget",
		Suggestion: "Verify the healthcheck command and port are correct and the app can pass the health check within budget."},
	{ID: "E_OBSERVE_CRASH_LOOP", HTTP: 500,
		Summary:    "observe window verdict: crash loop",
		Suggestion: "The process exited repeatedly during the observe window: check app logs to locate the crash cause and roll back manually if needed."},
	{ID: "E_OBSERVE_UNHEALTHY", HTTP: 500,
		Summary:    "observe window verdict: unstable (never reached healthy)",
		Suggestion: "The health verdict failed during the observe window: check app logs and health endpoints, and roll back manually if needed."},
	{ID: "E_DEPLOY_INTERRUPTED", HTTP: 500,
		Summary:    "deploy interrupted (control plane stopped/node failure), state machine not completed",
		Suggestion: "Start a new deploy to recover; if the traffic switch had not happened yet, traffic was unaffected."},
	{ID: "E_DEPLOY_POST_WINDOW_UNSTABLE", HTTP: 500,
		Summary:    "unstable verdict after the observe window passed (platform only alerts, no automatic rollback)",
		Suggestion: "Check app logs to assess the impact; manual rollback is advised (no automatic rollback after the observe window)."},
	{ID: "E_DEPLOY_DOWNTIME_FAILED", HTTP: 500,
		Summary:    "stop-first downtime switch failed and forced recovery failed (permanent outage risk path)",
		Suggestion: "Fix the start failure and deploy again; this path forces recovery and cannot roll back."},
	{ID: "E_ROLLBACK_FAILED", HTTP: 500,
		Summary:    "rollback failed (single-layer replay of the revision)",
		Suggestion: "Check the deployment record and logs to locate the replay failure; rollback can be started again."},
	{ID: "E_ROLLBACK_NO_TARGET", HTTP: 409,
		Summary:    "no replayable rollback target (outside the 5 most recent verified revisions/first deploy failed)",
		Suggestion: "No replayable revision in history: deploy a new revision instead (default retention is 5 revisions)."},
	{ID: "E_RUNTIME_UNAVAILABLE", HTTP: 503,
		Summary:    "Swarm/node substrate unreachable or operation failed",
		Suggestion: "Check Docker Engine and node status and retry after the substrate recovers; the reconciler converges the observation cache automatically."},

	// ── 破坏性变更门控（MG-C3 实现期新增，architecture §2.4 plan/apply
	//    语义「破坏性操作要求 --confirm-destructive」；文档外码单独列出，
	//    待 T0.5 契约冻结确认）──
	{ID: "E_DEPLOY_CONFIRM_REQUIRED", HTTP: 409,
		Summary:    "deploy contains destructive changes (service removal/volume unbind) without the confirmation flag; rejected from queueing",
		Suggestion: "This deploy will remove services or unbind volumes relative to the latest revision: confirm the intent and retry with --confirm-destructive."},

	// ── 有状态放置（stateful-placement §2.8/§2.5/§2.6）──
	{ID: "E_PLACEMENT_NODE_INVALID", HTTP: 422,
		Summary:    "failed to resolve node name/ID in the placement label (422 + candidate list)",
		Suggestion: "Check the fleetly.placement.node value (a node name or platform node ID); see context for the candidate node list."},
	{ID: "E_PLACEMENT_NODE_NOT_FOUND", HTTP: 422,
		Summary:    "node referenced by the placement label does not exist or was removed",
		Suggestion: "Pick a valid node from the node list and retry."},
	{ID: "E_PLACEMENT_NODE_UNAVAILABLE", HTTP: 503,
		Summary:    "bound node is currently not ready (deploy preflight fails fast, no queueing)",
		Suggestion: "Wait for the bound node to become ready, or change the binding via rebind after confirming data safety."},
	{ID: "E_PLACEMENT_NODE_GONE", HTTP: 409,
		Summary:    "bound node was removed (blocked(node_gone)); in-flight deploys fail with this code",
		Suggestion: "rebind --data-restored after data recovery, or rebind --discard after confirming discard (admin+confirm+audited)."},
	{ID: "E_PLACEMENT_NO_ELIGIBLE_NODE", HTTP: 503,
		Summary:    "no candidate for automatic placement (no ready node satisfies the constraints)",
		Suggestion: "Check node readiness and placement constraints; retry after a candidate node recovers."},
	// 预留：v0.1 单机无第二候选（MoveBinding 走 E_CAPABILITY_REQUIRES_
	// MULTI_NODE 守卫拒绝）；带确认换点随 v0.2 rebind/备份恢复迁移接线。
	{ID: "E_PLACEMENT_MOVE_REQUIRES_ACK", HTTP: 409,
		Summary:    "placement label differs from the current binding: cross-node moves require explicit confirmation (only path = backup-restore migration)",
		Suggestion: "The only supported path for a cross-node move is backup-restore migration: confirm explicitly via PUT placement (--data-restored/--discard)."},
	{ID: "E_PLACEMENT_LABEL_CONFLICT", HTTP: 422,
		Summary:    "placement labels of multiple services in one app point to different nodes",
		Suggestion: "Set fleetly.placement.node to the same node for all services of the app."},
	{ID: "E_VOLUME_NODE_MISMATCH", HTTP: 409,
		Summary:    "volume data node ≠ target deploy node (data-safety sentinel that turns the empty-volume incident into a 409)",
		Suggestion: "Declare a data disposition and retry: --data-restored (rebuilt by the restore flow) or --discard (admin+confirm; the old volume becomes orphaned)."},
	{ID: "E_CAPABILITY_REQUIRES_MULTI_NODE", HTTP: 400,
		Summary:    "multi-node operations are unavailable on a single-node topology (v0.1) and never silently succeed",
		Suggestion: "This operation requires a multi-node topology; v0.1 is single-node and multi-node support ships with v0.2."},

	// ── 备份与恢复（state-model §2.7）──
	// 预留：恢复器未实现（横切评审确认；usage_test 的豁免清单同理由）。
	{ID: "E_BACKUP_KEY_MISSING", HTTP: 500,
		Summary:    "restore verification failed: checksum/master key fingerprint mismatch; half-restored state is rejected",
		Suggestion: "Provide the correct backup set and master key; the restore flow rejects half-restored state — do not hand-assemble state."},

	// ── 事件流（state-model §2.9）──
	{ID: "E_EVENT_CURSOR_EXPIRED", HTTP: 410,
		Summary:    "event cursor predates the retention window (30 days); explicit gap",
		Suggestion: "Re-fetch all events starting from the oldest_seq in the response."},

	// ── 多节点 registry（E1 多节点设计 §5.2/D-MN-11，2026-09-20 冻结）：
	//    registry 模式部署前哨与推送的分层归因——registry 错 → 查 zot/网络/
	//    凭据，镜像缺 → 重建；快速失败不排队。manifest 缺失复用
	//    E_IMAGE_UNAVAILABLE（语义同「回滚目标不可得」），不另立新码 ──
	{ID: "E_REGISTRY_UNAVAILABLE", HTTP: 503,
		Summary:    "registry-mode deploy preflight: the platform registry is unreachable (fail fast, no queueing)",
		Suggestion: "The platform registry did not answer: check the fleetly-registry service on the manager and the registry.<base> route (Traefik); deploys fail fast until the registry responds."},
	{ID: "E_REGISTRY_PUSH_FAILED", HTTP: 500,
		Summary:    "pushing the build result to the platform registry failed (network/credentials/registry fault)",
		Suggestion: "The push to registry.<base> failed: check that the fleetly-registry service is healthy and the platform registry credentials (registry.auth_file) are current, then run the build again."},

	// ── 入口路由（T2.15；架构 §2.5/§2.6：路由发布严格晚于健康门，发布
	//    失败不回滚部署、单独告警——deployment 仍可成功，错误落审计与本码）──
	{ID: "E_ROUTE_PUBLISH_FAILED", HTTP: 503,
		Summary:    "ingress route publish failed (deploy unaffected; route alerts separately)",
		Suggestion: "Check the route.publish_failed event and fleetly ingress status; once the substrate/ingress recovers, it converges automatically with the next deploy or republish."},

	// ── label 契约（state-model §2.4）──
	{ID: "E_LABEL_RESERVED", HTTP: 422,
		Summary:    "user occupied the reserved namespace fleetly.*",
		Suggestion: "fleetly.* is a platform-reserved namespace: use a different label prefix."},

	// ── 状态与并发（state-model §2.2、architecture §2.3）──
	{ID: "E_STATE_VERSION_CONFLICT", HTTP: 409,
		Summary:    "read-before-write optimistic concurrency conflict (object version token invalid)",
		Suggestion: "The object was modified concurrently: re-read the latest state and retry with the new version token."},

	// ── token 管理（M4-6 实现期新增，评审整改 B5；文档外码单独列出，
	//    待 T0.5 契约冻结确认）──
	{ID: "E_TOKEN_LAST_ADMIN", HTTP: 409,
		Summary:    "last-admin guard: revoking would leave no unrevoked admin token on the platform; self-lockout rejected",
		Suggestion: "This is the last unrevoked admin token: create a new admin token before revoking this one (otherwise the platform becomes unmanageable, and the bootstrap token is not reseeded on restart)."},

	// ── 警告码（W_：资源/计划上的标注，不作为 HTTP 错误返回，HTTP=0）──
	{ID: "W_DEPLOY_INSTABILITY",
		Summary:    "post-observe-window instability alert (one source of app degraded)",
		Suggestion: "Instability detected at runtime: check app logs to assess; the platform alerts once and advises manual rollback (no escalation counter)."},
	{ID: "W_DEPLOY_NO_HEALTHCHECK",
		Summary:    "service declares no healthcheck, health_gate=none (health gate skipped)",
		Suggestion: "Add a healthcheck to the service; without one the health gate is skipped and the deploy passes straight through."},
	{ID: "W_ROLLBACK_IMAGE_RISK",
		Summary:    "rollback target declared by tag; its content may have changed (not an immutable digest)",
		Suggestion: "The rollback target image is declared by tag and its content may have changed: confirm the image content before replaying."},
	{ID: "W_PLACEMENT_STATELESS_PIN",
		Summary:    "stateless app auto-pinned because a new volume was added (data birthplace)",
		Suggestion: "The stateless app was pinned because of the new volume: remove the volume declaration if free scheduling is needed."},
	{ID: "W_ENV_PLATFORM_OVERRIDE",
		Summary:    "platform-layer env_vars overrides same-named keys from files (three-layer merge chain)",
		Suggestion: "Platform-layer env overrides same-named compose keys: the merge result follows the platform layer; check the platform env_vars."},
}
