package eventcode

// builtins 是文档域清单的全量录入（每事件注明文档出处）：
//   - release-semantics §2.7：deployment.* 16 个、app.* 3 个
//   - stateful-placement §2.8：placement.* 5 个、node.* 4 个、volume.* 4 个
//   - state-model §2.9/§2.10：reconcile.drift_detected、restore.completed
//   - architecture §4.3（cron 触发前哨「记 skipped + 事件」）：cron.skipped
//   - 实现期新增（单独列出）：route.* 2 个、app.webhook_fetch_failed、
//     engine.stale_nonterminal、build.stale_nonterminal（S18-A10）、
//     app.deleted（B6/H10，MG-3）、app.substrate_missing（T0-V2.2，R2）、
//     backup.upload_failed / backup.upload_recovered（E3-3，W3-S2）、
//     cron.triggered / cron.succeeded / cron.failed / cron.timed_out
//     （FZ-4 钉名，E5 Cron，W3-S5 接线）
//
// 计 52 个事件名。
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
	// T0-V2.2 实现期新增（调研 R2 运行期 DB↔Swarm 对账，引擎周期 duty）：
	// 派生态声称 running 的 app 其期望服务在 substrate 整体缺失（外部
	// docker service rm）——只披露与修正派生态（running → down），不自动
	// 重建；判据是 service 存在性而非副本数，底座读错误不算缺失。
	{Name: "app.substrate_missing", Summary: "app claimed running but its managed service(s) are absent from the substrate (external removal); view corrected to down"},

	// ── 放置与节点/卷（stateful-placement §2.8）──
	{Name: "placement.bound", Summary: "app node binding completed (including automatic pinning)"},
	// v0.2 E1-7 接线：显式换点 Rebind（multi-node §2.6）发出——绑定换绑、
	// 卷行 prev 登记、事件与审计同事务。
	{Name: "placement.changed", Summary: "binding changed (via the explicitly confirmed migration path)"},
	{Name: "placement.blocked", Summary: "bound node unavailable/removed; app blocked"},
	{Name: "placement.recovered", Summary: "bound node recovered; binding re-established automatically"},
	// 预留：DR 后绑定判定要求显式放置——随 v0.2 恢复阶梯（单机无场景）。
	{Name: "placement.unresolved", Summary: "binding undecidable after DR; explicit placement required (no guessing)"},
	// v0.2 E1-6 接线：节点观测事件族由锚定 duty 差分发出（v0.1 的「不产生
	// 产品事件」注记解除，multi-node §2.7）。
	{Name: "node.joined", Summary: "node joined the cluster (observed)"},
	{Name: "node.down", Summary: "node judged DOWN (Swarm loss-of-contact verdict)"},
	{Name: "node.up", Summary: "node back to ready"},
	{Name: "node.removed", Summary: "node removed (observed docker node rm)"},
	// v0.2 多节点（multi-node §2.7/§5.3）实现期接入：availability 转移
	//（active↔drain/pause）是 drain 维护窗口叙事的事件载体（载荷 old/new）。
	{Name: "node.availability_changed", Summary: "node availability changed (active/drain/pause transition; drain maintenance-window narrative; payload carries old/new)"},
	{Name: "volume.created", Summary: "volume registered (data birthplace; pins the hosting node)"},
	// 预留：卷声明移除的显性化随 v0.2 卷生命周期票（usage_test 豁免清单
	// 同理由）。
	{Name: "volume.detached", Summary: "volume detached (volume declaration removed; data is not deleted with the declaration)"},
	{Name: "volume.orphaned", Summary: "volume orphaned (old volume kept by default after app deletion / after --discard)"},
	// v0.2 E1-7 接线：换点 discarded 处置声明（admin+confirm）由显式换点
	// 路径发出。
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

	// ── 定时任务（E5 Cron，架构 §4.3 细则 + object-storage 设计 §8 事件面；
	//    W3-S5 接线。发出来源 = internal/cron 触发链与完成检测——事件与
	//    cron_runs 台账行同事务（Outbox）。FZ-4 钉名：cron.timed_out）──
	{Name: "cron.triggered", Summary: "cron schedule fired: one-shot swarm job created (payload carries app/service/run id/job service; scheduled_at is the hit cron point)"},
	{Name: "cron.succeeded", Summary: "cron run completed (task complete; job service removed)"},
	{Name: "cron.failed", Summary: "cron run failed (task failed/rejected; no retry — restart-condition=none; job service removed)"},
	{Name: "cron.timed_out", Summary: "cron run exceeded its watchdog budget (default 10m, fleetly.cron.timeout label overrides; job service removed)"},
	{Name: "cron.skipped", Summary: "cron trigger skipped (overlap / node unavailable / missed during downtime / interrupted by restart); no catch-up run"},

	// S18-A10 实现期新增（评审类 A 运行时断言层，§9 裁决并入 janitor）：
	// 非终态行超龄停留的显性化告警（只告警不自愈——恢复路径已有 S8/S9 兜底，
	// 这层是未来新状态机漏洞的观测面）。
	{Name: "engine.stale_nonterminal", Summary: "deploy stayed non-terminal past 2x the (deploy watchdog + observe window) budget (surfaces state-machine bugs; no self-healing)"},
	{Name: "build.stale_nonterminal", Summary: "build stayed queued/building past 2x the build timeout budget (surfaces state-machine bugs; no self-healing)"},

	// ── 对象存储 S3 面（E3 对象存储专项设计 §5.3，2026-09-21 裁决轮落定，
	//    注册表只增；s3.updated 由 E3-2 接线，rustfs duty 差分事件由 E3-5
	//    接线——W3-S3）──
	// 发出来源：platform_settings 的 S3 设置保存事务（internal/state/
	// s3settings.go，与业务写同事务 = Outbox 模式）。payload 只带模式与
	// 布尔开关，绝不带凭证材料（state-model §2.9 secret 值禁止进事件）。
	{Name: "s3.updated", Summary: "object storage settings changed (payload carries the mode and toggles, never credentials)"},
	// E3-5 rustfs duty 差分事件（node.* 同型；发出来源 = internal/rustfs
	// 的收敛拍——服务缺失创建/spec 漂移更新发 deployed（payload 带 reason
	// created|updated），mode 离开 rustfs 服务移除发 removed（payload 带
	// volume_retained=true——数据卷保留语义的显性化面）。payload 不含任何
	// 凭据材料（凭据指纹只在日志面）。
	{Name: "s3.rustfs_deployed", Summary: "managed RustFS deployed or converged to the desired spec (payload carries service/image/reason, never credentials)"},
	{Name: "s3.rustfs_removed", Summary: "managed RustFS removed after s3.mode left rustfs (data volume retained; payload carries volume_retained=true)"},

	// ── 状态备份上传轨（E3-3，§2.3/D-S3-4；W3-S2 接线）──
	// 发出来源：备份 Manager 上传步（internal/statebackup/restic.go）。
	// 口径：restic backup 成功但回读校验失败/容器执行失败 → failed；本地
	// 份不受影响（verify_status 不回写）。payload 带 backup id 与错误摘要
	//（scrubText 擦除后），绝不带 restic env 凭证材料。
	{Name: "backup.upload_failed", Summary: "remote state backup upload failed (local snapshot unaffected; payload carries the backup id and a redacted error summary, never credentials)"},
	// 恢复绿：上一份上传 failed、本份 ok 时发出（配对 failed 形成红→绿
	// 闭环）。payload 带 backup id。
	{Name: "backup.upload_recovered", Summary: "remote state backup upload recovered (previous upload had failed; payload carries the backup id)"},
}
