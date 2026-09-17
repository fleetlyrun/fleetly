package eventcode

// builtins 是文档域清单的全量录入（每事件注明文档出处）：
//   - release-semantics §2.7：deployment.* 16 个、app.* 3 个
//   - stateful-placement §2.8：placement.* 5 个、node.* 4 个、volume.* 4 个
//   - state-model §2.9/§2.10：reconcile.drift_detected、restore.completed
//   - architecture §4.3（cron 触发前哨「记 skipped + 事件」）：cron.skipped
//
// 计 35 个事件名。
var builtins = []Event{
	// ── 发布（release-semantics §2.7）──
	{Name: "deployment.queued", Summary: "部署入队（同 app 并发互斥排队）"},
	{Name: "deployment.release_started", Summary: "发布开始（构建/更新启动）"},
	{Name: "deployment.healthy", Summary: "新版本通过健康门"},
	{Name: "deployment.switched", Summary: "流量切换完成"},
	{Name: "deployment.observe_started", Summary: "观察窗开始（默认 60s）"},
	{Name: "deployment.succeeded", Summary: "发布成功终态"},
	{Name: "deployment.failed", Summary: "发布失败终态（reason=错误码）"},
	{Name: "deployment.warning", Summary: "发布警告（W_ 码；观察窗只告警语义）"},
	{Name: "deployment.cancelled", Summary: "部署被取消（先归位再落 cancelled）"},
	{Name: "deployment.rollback_started", Summary: "回滚开始（快照单层重放）"},
	{Name: "deployment.rollback_finished", Summary: "回滚完成"},
	{Name: "deployment.rollback_failed", Summary: "回滚失败"},
	{Name: "deployment.recovery_scheduled", Summary: "系统性失败恢复已排队（自动重试动作）"},
	{Name: "deployment.recovery_blocked", Summary: "恢复被阻塞（如绑定节点不可用）"},
	{Name: "deployment.substrate_halted", Summary: "底座故障导致发布停滞（Swarm/节点不可用）"},
	{Name: "deployment.superseded", Summary: "部署被更新目标取代（陈旧终态）"},

	// ── 应用状态机（release-semantics §2.7、state-model §2.10）──
	{Name: "app.degraded", Summary: "应用进入 degraded（观察窗失败/窗后不稳定/W_DEPLOY_INSTABILITY）"},
	{Name: "app.instability_detected", Summary: "运行期检测到不稳定"},
	{Name: "app.recovered", Summary: "应用退出 degraded，恢复 running"},

	// ── 放置与节点/卷（stateful-placement §2.8）──
	{Name: "placement.bound", Summary: "应用完成节点绑定（含自动钉住）"},
	{Name: "placement.changed", Summary: "绑定变更（经显式确认的迁移路径）"},
	{Name: "placement.blocked", Summary: "绑定节点不可用/已移除，应用 blocked"},
	{Name: "placement.recovered", Summary: "绑定节点恢复，自动回绑"},
	{Name: "placement.unresolved", Summary: "DR 后绑定无法判定，要求显式放置（不猜测）"},
	{Name: "node.joined", Summary: "节点加入集群（观测）"},
	{Name: "node.down", Summary: "节点判定 DOWN（Swarm 心跳语义）"},
	{Name: "node.up", Summary: "节点恢复 ready"},
	{Name: "node.removed", Summary: "节点被移除（docker node rm 观测）"},
	{Name: "volume.created", Summary: "卷注册（数据诞生点，钉住所在节点）"},
	{Name: "volume.detached", Summary: "卷解除挂载（移除卷声明，数据不随声明删除）"},
	{Name: "volume.orphaned", Summary: "卷转孤儿（应用删除默认保留/--discard 后旧卷）"},
	{Name: "volume.discarded", Summary: "卷被显式丢弃（admin+confirm+审计）"},

	// ── 对账（state-model §2.9 对账/漂移）──
	{Name: "reconcile.drift_detected", Summary: "检测到期望态与实际态漂移（含手动 docker 操作）；收敛 per-app opt-in"},

	// ── 备份恢复（state-model §2.7）──
	{Name: "restore.completed", Summary: "控制面恢复流程完成（只读观察退出后）"},

	// ── 定时任务（architecture §4.3：触发前哨「记 skipped + 事件」；cron 入 v0.2）──
	{Name: "cron.skipped", Summary: "定时任务触发跳过（节点不 ready/控制面停机错过点/重叠 skip），不补跑"},
}
