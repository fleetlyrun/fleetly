package eventcode

// builtins 是文档域清单的全量录入（每事件注明文档出处）：
//   - release-semantics §2.7：deployment.* 16 个、app.* 3 个
//   - stateful-placement §2.8：placement.* 5 个、node.* 4 个、volume.* 4 个
//   - state-model §2.9/§2.10：reconcile.drift_detected、restore.completed
//   - architecture §4.3（cron 触发前哨「记 skipped + 事件」）：cron.skipped
//   - 实现期新增（单独列出）：route.* 2 个、app.webhook_fetch_failed、
//     engine.stale_nonterminal、build.stale_nonterminal（S18-A10）、
//     app.deleted（B6/H10，MG-3）
//
// 计 38 个事件名。
var builtins = []Event{
	// ── 发布（release-semantics §2.7）──
	{Name: "deployment.queued", Summary: "deploy queued (per-app mutually exclusive queueing)"},
	{Name: "deployment.release_started", Summary: "release started (build/update kicking off)"},
	{Name: "deployment.healthy", Summary: "new revision passed the health gate"},
	{Name: "deployment.switched", Summary: "traffic switch completed"},
	{Name: "deployment.observe_started", Summary: "observe window started (default 60s)"},
	{Name: "deployment.succeeded", Summary: "deploy succeeded (terminal state)"},
	{Name: "deployment.failed", Summary: "deploy failed (terminal state; reason=error code)"},
	{Name: "deployment.warning", Summary: "deploy warning (W_ code; observe window alert-only semantics)"},
	{Name: "deployment.cancelled", Summary: "deploy cancelled (replays the prior state first, then lands cancelled)"},
	{Name: "deployment.rollback_started", Summary: "rollback started (single-layer replay of the revision)"},
	{Name: "deployment.rollback_finished", Summary: "rollback finished"},
	{Name: "deployment.rollback_failed", Summary: "rollback failed"},
	// 预留：自动恢复排队事件随 v0.2 恢复器（v0.1 失败分流不建新
	// deployment，D-REL-6 默认只告警；usage_test 豁免清单同理由）。
	{Name: "deployment.recovery_scheduled", Summary: "recovery from systemic failure queued (automatic retry action)"},
	{Name: "deployment.recovery_blocked", Summary: "recovery blocked (e.g. bound node unavailable)"},
	{Name: "deployment.substrate_halted", Summary: "substrate failure halted the deploy (Swarm/node unavailable)"},
	// 预留：同 app 互斥排队下无「在途被新目标取代」路径，随 v0.2 并发策略
	//（usage_test 豁免清单同理由）。
	{Name: "deployment.superseded", Summary: "deploy superseded by a newer target (stale terminal state)"},

	// ── 应用状态机（release-semantics §2.7、state-model §2.10）──
	{Name: "app.degraded", Summary: "app entered degraded (observe window failure / post-window instability / W_DEPLOY_INSTABILITY)"},
	{Name: "app.instability_detected", Summary: "instability detected at runtime"},
	{Name: "app.recovered", Summary: "app left degraded, back to running"},
	// S17-D1 实现期新增（评审类 D；webhook 受理转异步后拉源失败只能走
	// 事件流披露——官方不重投 202，redeliver 靠人工）。
	{Name: "app.webhook_fetch_failed", Summary: "async source fetch failed after webhook acceptance (retry manually via redeliver on the same delivery)"},
	// B6/H10 实现期新增（MG-3 横切结构修复）：app 删除生命周期第二拍的
	// 终局事件——此前 api DeleteApp 只落第一拍（deleting），第二拍
	// （deleting → deleted + 受管服务移除）无执行者，服务永久运行。
	{Name: "app.deleted", Summary: "app deletion completed (tombstone second beat: managed services removed, name enters retention hold)"},

	// ── 放置与节点/卷（stateful-placement §2.8）──
	{Name: "placement.bound", Summary: "app node binding completed (including automatic pinning)"},
	// 预留：绑定变更只在显式确认的迁移路径发出——单机无第二候选
	//（MoveBinding 守卫拒绝），rebind 随 v0.2（usage_test 豁免清单同理由）。
	{Name: "placement.changed", Summary: "binding changed (via the explicitly confirmed migration path)"},
	{Name: "placement.blocked", Summary: "bound node unavailable/removed; app blocked"},
	{Name: "placement.recovered", Summary: "bound node recovered; binding re-established automatically"},
	// 预留：DR 后绑定判定要求显式放置——随 v0.2 恢复阶梯（单机无场景）。
	{Name: "placement.unresolved", Summary: "binding undecidable after DR; explicit placement required (no guessing)"},
	// 预留：节点观测事件族由节点观测器发出——v0.1 单节点无观测器循环
	//（节点状态经放置 Preflight 直读），随 v0.2 多节点。
	{Name: "node.joined", Summary: "node joined the cluster (observed)"},
	{Name: "node.down", Summary: "node judged DOWN (Swarm loss-of-contact verdict)"},
	{Name: "node.up", Summary: "node back to ready"},
	{Name: "node.removed", Summary: "node removed (observed docker node rm)"},
	{Name: "volume.created", Summary: "volume registered (data birthplace; pins the hosting node)"},
	// 预留：卷声明移除的显性化与 admin 丢弃 CLI 随 v0.2 卷生命周期票
	//（v0.1 对账只动服务面；usage_test 豁免清单同理由）。
	{Name: "volume.detached", Summary: "volume detached (volume declaration removed; data is not deleted with the declaration)"},
	{Name: "volume.orphaned", Summary: "volume orphaned (old volume kept by default after app deletion / after --discard)"},
	{Name: "volume.discarded", Summary: "volume explicitly discarded (admin+confirm+audited)"},

	// ── 对账（state-model §2.9 对账/漂移）──
	{Name: "reconcile.drift_detected", Summary: "drift detected between desired and actual state (including manual docker operations); convergence is per-app opt-in"},

	// ── 备份恢复（state-model §2.7）──
	// 预留：恢复器未实现（同 E_BACKUP_KEY_MISSING 的预留裁决；usage_test
	// 豁免清单同理由）。
	{Name: "restore.completed", Summary: "control plane restore completed (after exiting read-only observation)"},

	// ── 入口路由（T2.15；架构 §2.5 不变量：路由发布严格晚于健康门——
	//    发布失败不回滚部署，route.publish_failed 单独告警 + 审计；证书
	//    签发/续期不设新事件名，走审计记录）──
	{Name: "route.published", Summary: "app ingress routes published (after the health gate passed, all dynamic config converged)"},
	{Name: "route.publish_failed", Summary: "app ingress route publish failed (deploy unaffected; alerts separately)"},

	// ── 定时任务（architecture §4.3：触发前哨「记 skipped + 事件」）──
	// 预留：cron 整体入 v0.2（usage_test 豁免清单同理由）。
	{Name: "cron.skipped", Summary: "cron trigger skipped (node not ready / control plane down at the tick / overlapping skip); no catch-up run"},

	// S18-A10 实现期新增（评审类 A 运行时断言层，§9 裁决并入 janitor）：
	// 非终态行超龄停留的显性化告警（只告警不自愈——恢复路径已有 S8/S9 兜底，
	// 这层是未来新状态机漏洞的观测面）。
	{Name: "engine.stale_nonterminal", Summary: "deploy stayed non-terminal past 2x the (deploy watchdog + observe window) budget (surfaces state-machine bugs; no self-healing)"},
	{Name: "build.stale_nonterminal", Summary: "build stayed queued/building past 2x the build timeout budget (surfaces state-machine bugs; no self-healing)"},
}
