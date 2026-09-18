package state

import "strings"

// fleetly.* label 最小契约（state-model §2.4）：保留命名空间 fleetly.*
// 为平台独占（用户占用 → E_LABEL_RESERVED 422，映射随 API 面落地）；写者
// 唯一 = 适配器 Marker 端口（本阶段仅节点身份锚经 DockerClient.UpdateNodeLabel
// 写入，业务对象 label 下发随 T2.14）；密钥/payload 永不入 label；平台保留
// 未知 fleetly.* 键不改写。
const (
	// LabelNamespace 是平台 label 保留命名空间前缀。
	LabelNamespace = "fleetly."

	// LabelManaged 标记平台受管对象（服务 label，"true"）：归属判定、
	// 孤儿检测、删除保护的依据。
	LabelManaged = "fleetly.managed"
	// LabelApp 标记归属应用（服务/容器 label，值为应用名）。
	LabelApp = "fleetly.app"
	// LabelProcess 标记 compose 服务名（应用内进程名）。
	LabelProcess = "fleetly.process"
	// LabelDeployment 标记发布归属（值 = deployment ID）。
	LabelDeployment = "fleetly.deployment"
	// LabelDesiredHash 是服务级期望态哈希（对账变更判据的落点 label，
	// state-model §2.5 desired-hash 纪律；service-label 变更不触发任务
	// 重建——归位零成本的配套，Spike B2）。
	LabelDesiredHash = "fleetly.desired-hash"
	// LabelCron 标记定时任务 schedule（v0.2 契约，常量先行，state-model §2.4）。
	LabelCron = "fleetly.cron"

	// LabelNodeID 是节点身份锚（node label，值 = 平台节点 ID n_<ULID>，
	// state-model §2.3）。
	LabelNodeID = "fleetly.node-id"

	// LabelDomains 是平台约定路由域名列表（compose 服务 label，逗号分隔）。
	LabelDomains = "fleetly.domains"
	// LabelPlacementNode 是放置意图（compose 服务 label，v0.2 多节点消费，
	// 常量先行）。
	LabelPlacementNode = "fleetly.placement.node"
)

// IsReservedLabel 报告 key 是否落在平台保留命名空间（fleetly.*）：
// 用户声明该命名空间内的 label 时上游应拒绝（E_LABEL_RESERVED）。
func IsReservedLabel(key string) bool {
	return strings.HasPrefix(key, LabelNamespace)
}

// ManagedLabelValue 是 LabelManaged 的约定取值。
const ManagedLabelValue = "true"
