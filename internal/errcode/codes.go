package errcode

// builtins 是文档域清单的全量录入（唯一真源为注册表；此处每码注明文档
// 出处）。分布核对：release-semantics §2.7（17 E + 3 W）、stateful-placement
// §2.8（8 E + 1 W）与 §2.9（1 E）、state-model §2.7/§2.9/§2.4/§2.2（4 E）、
// architecture §2.4（3 码）+ §2.3（E_STATE_VERSION_CONFLICT）。
// 计 32 个 E_ + 5 个 W_ = 37 码。
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
		Summary:    "compose 声明落在受控子集/拒绝清单之外，校验显式拒绝",
		Suggestion: "移除或改写不受支持的 compose 字段后重试；受控子集与拒绝清单见错误码文档。"},
	{ID: "E_COMPOSE_MANAGED_FIELD", HTTP: 400,
		Summary:    "compose 受管字段取值违反平台治理（failure_action/monitor）",
		Suggestion: "将 deploy.update_config.failure_action 改为 pause（或省略）、monitor 改为 5s 或省略后重试。"},
	{ID: "E_COMPOSE_UNSAFE_STRATEGY", HTTP: 400,
		Summary:    "显式 start-first 与平台强制 stop-first 的场景冲突（有卷/固定 host 端口/global）",
		Suggestion: "将 deploy.update_config.order 改为 start-first 或省略；有卷/固定端口服务由平台强制 stop-first。"},

	// ── 域名/TLS（architecture §2.4）──
	{ID: "E_DOMAIN_CONFLICT", HTTP: 409,
		Summary:    "同一域名出现在同一应用的两个服务上",
		Suggestion: "该域名已被同应用其他服务占用：从其中一处移除后再声明。"},
	{ID: "E_DOMAIN_UNSUPPORTED", HTTP: 400,
		Summary:    "域名形态不受支持（如通配符 v0.2 起需 DNS-01）",
		Suggestion: "改用具体域名；通配符与证书形态支持范围见错误码文档。"},

	// ── 发布管线（release-semantics §2.7 场景矩阵）──
	{ID: "E_BUILD_FAILED", HTTP: 500,
		Summary:    "构建阶段失败（Railpack/BuildKit）",
		Suggestion: "查看构建日志定位编译/依赖错误，修复后重新部署。"},
	{ID: "E_IMAGE_PULL_FAILED", HTTP: 500,
		Summary:    "镜像拉取失败（registry 不可达/凭证无效/网络故障）",
		Suggestion: "确认镜像引用与 registry 凭证有效、节点可访问 registry 后重试。"},
	{ID: "E_IMAGE_UNAVAILABLE", HTTP: 500,
		Summary:    "回滚目标镜像已不可用（digest 失效/被清理）",
		Suggestion: "回滚目标镜像已不可用：改为重放最近可用版本或重新构建后部署。"},
	{ID: "E_SCHEDULER_PENDING_TIMEOUT", HTTP: 500,
		Summary:    "任务滞留 PENDING 超过发布看门狗（调度约束/资源不足/节点不可用）",
		Suggestion: "检查节点资源与放置约束（绑定节点是否 ready），释放资源或调整约束后重试。"},
	{ID: "E_TASK_START_FAILED", HTTP: 500,
		Summary:    "新任务启动失败（镜像入口/资源限额/运行时错误）",
		Suggestion: "查看任务日志定位启动失败原因，修复后重试。"},
	{ID: "E_HEALTH_TIMEOUT", HTTP: 500,
		Summary:    "健康门超时：新任务在预算内未通过 healthcheck",
		Suggestion: "确认 healthcheck 命令与端口正确、应用能在预算内通过健康检查。"},
	{ID: "E_OBSERVE_CRASH_LOOP", HTTP: 500,
		Summary:    "观察窗判定 crash loop",
		Suggestion: "观察窗内进程反复退出：查看应用日志定位崩溃原因，必要时手动回滚。"},
	{ID: "E_OBSERVE_UNHEALTHY", HTTP: 500,
		Summary:    "观察窗判定 unhealthy",
		Suggestion: "观察窗内健康判定未通过：检查应用日志与健康端点，必要时手动回滚。"},
	{ID: "E_DEPLOY_INTERRUPTED", HTTP: 500,
		Summary:    "发布被中断（控制面停止/节点故障），未完成状态机",
		Suggestion: "重新发起部署以恢复；未切流场景流量未受影响。"},
	{ID: "E_DEPLOY_POST_WINDOW_UNSTABLE", HTTP: 500,
		Summary:    "观察窗通过后出现不稳定判定（平台只告警、不自动回滚）",
		Suggestion: "查看应用日志评估影响，建议手动回滚（平台观察窗后不自动回滚）。"},
	{ID: "E_DEPLOY_DOWNTIME_FAILED", HTTP: 500,
		Summary:    "stop-first 停机切换失败且强制归位失败（永久宕机风险路径）",
		Suggestion: "修复启动失败原因后重新部署；该路径强制归位、不可回滚。"},
	{ID: "E_ROLLBACK_FAILED", HTTP: 500,
		Summary:    "回滚（快照单层重放）失败",
		Suggestion: "查看部署记录与日志定位重放失败原因，可再次发起回滚。"},
	{ID: "E_ROLLBACK_NO_TARGET", HTTP: 409,
		Summary:    "没有可重放的回滚目标（保留最近 5 个已验证版本之外/首发失败）",
		Suggestion: "没有可重放的历史版本：请改用新版本部署（版本保留数默认 5）。"},
	{ID: "E_RUNTIME_UNAVAILABLE", HTTP: 503,
		Summary:    "Swarm/节点底座不可达或操作失败",
		Suggestion: "检查 Docker Engine 与节点状态，底座恢复后重试；对账器会自动收敛观测缓存。"},

	// ── 有状态放置（stateful-placement §2.8/§2.5/§2.6）──
	{ID: "E_PLACEMENT_NODE_INVALID", HTTP: 422,
		Summary:    "放置 label 的节点名/ID 解析失败（422 + 候选清单）",
		Suggestion: "检查 edgefleet.placement.node 取值（可写节点名或平台节点 ID）；候选节点清单见 context。"},
	{ID: "E_PLACEMENT_NODE_NOT_FOUND", HTTP: 422,
		Summary:    "放置 label 指向的节点不存在或已移除",
		Suggestion: "从节点列表中选择有效节点后重试。"},
	{ID: "E_PLACEMENT_NODE_UNAVAILABLE", HTTP: 503,
		Summary:    "绑定节点当前非 ready（部署前哨快速失败，不排队）",
		Suggestion: "等待绑定节点恢复 ready，或确认数据安全后通过 rebind 更改绑定。"},
	{ID: "E_PLACEMENT_NODE_GONE", HTTP: 409,
		Summary:    "绑定节点已被移除（blocked(node_gone)；进行中部署以此失败）",
		Suggestion: "恢复数据后 rebind --data-restored，或确认丢弃后 rebind --discard（admin+confirm+审计）。"},
	{ID: "E_PLACEMENT_NO_ELIGIBLE_NODE", HTTP: 503,
		Summary:    "自动选点无候选（无 ready 节点满足条件）",
		Suggestion: "检查节点 ready 状态与放置约束，恢复候选节点后重试。"},
	{ID: "E_PLACEMENT_MOVE_REQUIRES_ACK", HTTP: 409,
		Summary:    "放置 label 与当前绑定不一致：跨点移动必须显式确认（唯一路径=备份恢复迁移）",
		Suggestion: "跨点移动唯一受支持路径是备份恢复迁移：通过 PUT placement 显式确认（--data-restored/--discard）。"},
	{ID: "E_PLACEMENT_LABEL_CONFLICT", HTTP: 422,
		Summary:    "同一应用多个服务的放置 label 指向不同节点",
		Suggestion: "将同一应用全部服务的 edgefleet.placement.node 统一为相同节点。"},
	{ID: "E_VOLUME_NODE_MISMATCH", HTTP: 409,
		Summary:    "卷数据节点 ≠ 目标部署节点（数据安全前哨，把空卷事故变成 409）",
		Suggestion: "声明数据处置后重试：--data-restored（恢复流程已重建）或 --discard（admin+confirm，旧卷转孤儿）。"},
	{ID: "E_CAPABILITY_REQUIRES_MULTI_NODE", HTTP: 400,
		Summary:    "多节点操作在单节点拓扑（v0.1）上不可用，不静默成功",
		Suggestion: "该操作需要多节点拓扑；v0.1 为单节点，多节点能力随 v0.2 提供。"},

	// ── 备份与恢复（state-model §2.7）──
	{ID: "E_BACKUP_KEY_MISSING", HTTP: 500,
		Summary:    "恢复校验失败：校验和/主密钥指纹不匹配，拒绝半恢复",
		Suggestion: "提供正确的备份集与主密钥；恢复流程拒绝半恢复，不要手工拼凑状态。"},

	// ── 事件流（state-model §2.9）──
	{ID: "E_EVENT_CURSOR_EXPIRED", HTTP: 410,
		Summary:    "事件游标早于保留期（30 天），显式断档",
		Suggestion: "以响应中的 oldest_seq 为起点重新拉取全量事件。"},

	// ── label 契约（state-model §2.4）──
	{ID: "E_LABEL_RESERVED", HTTP: 422,
		Summary:    "用户占用了保留命名空间 edgefleet.*",
		Suggestion: "edgefleet.* 为平台保留命名空间：请改用其他 label 前缀。"},

	// ── 状态与并发（state-model §2.2、architecture §2.3）──
	{ID: "E_STATE_VERSION_CONFLICT", HTTP: 409,
		Summary:    "写前直读乐观并发冲突（对象版本令牌失效）",
		Suggestion: "对象已被并发修改：重新读取最新状态后以新版本令牌重试。"},

	// ── 警告码（W_：资源/计划上的标注，不作为 HTTP 错误返回，HTTP=0）──
	{ID: "W_DEPLOY_INSTABILITY",
		Summary:    "观察窗后不稳定告警（app degraded 的来源之一）",
		Suggestion: "运行期出现不稳定：查看应用日志评估，平台只告警一次、建议手动回滚（不做计数升级）。"},
	{ID: "W_DEPLOY_NO_HEALTHCHECK",
		Summary:    "服务未声明 healthcheck，health_gate=none（健康门跳过）",
		Suggestion: "建议为服务补充 healthcheck；未声明时健康门跳过、发布直通。"},
	{ID: "W_ROLLBACK_IMAGE_RISK",
		Summary:    "回滚目标以 tag 声明、内容可能已变更（非 digest 不可变）",
		Suggestion: "回滚目标镜像以 tag 声明、内容可能已变更：确认镜像内容后再重放。"},
	{ID: "W_PLACEMENT_STATELESS_PIN",
		Summary:    "无状态应用因新增卷被自动钉住（数据诞生点）",
		Suggestion: "无状态应用因新增卷被钉住：如需自由调度请移除卷声明。"},
	{ID: "W_ENV_PLATFORM_OVERRIDE",
		Summary:    "平台层 env_vars 覆盖了文件同名键（三层合并链）",
		Suggestion: "平台层 env 覆盖了 compose 同名键：合并结果以平台层为准，请检查平台 env_vars。"},
}
