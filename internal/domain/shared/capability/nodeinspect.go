package capability

import "context"

// RuntimeNodeInspect 是节点观测子面（按需实现）：按平台节点 ID 实时列在跑
// 容器（Nodes API ListNodeContainers 的承载面）。快照语义与 ExecInstances
// 同锚：每次调用直读编排器任务/pod 列表——观测缓存不参与。归属字段取自
// 载体归属标记（Provider 私有标记的平台投影），标记缺席的字段留空诚实呈现。
// 未实现时列表面精确失败（E_RUNTIME_FACE_UNSUPPORTED，与 Logs/Admin 同款
// 降级文化）。container 按 docker 本义使用（第三方专有词汇豁免，
// 2026-10-16 用户裁定）。
type RuntimeNodeInspect interface {
	// NodeContainers 返回节点上的在跑容器集（running 且锚定本节点；无在跑
	// 容器返回空切片——列表面诚实空）。行序按 (WorkloadID, Instance) 字典序
	// （确定性呈现）。nodeID 是平台节点 ID（RuntimeAdmin 同锚约定——Provider
	// 侧经节点锚定标记自行解析载体节点身份）；对不上任何载体节点返回
	// ErrNodeNotFound。
	NodeContainers(ctx context.Context, nodeID string) ([]NodeContainerObservation, error)
}

// 节点观测的归属域主体值（NodeContainerObservation.Domain 契约面；
// ADR-0025 决策 4 域主体四轴）。
const (
	NodeDomainApp      = "app"
	NodeDomainTask     = "task"
	NodeDomainDatabase = "database"
	NodeDomainBrowse   = "browse"
)

// NodeContainerObservation 是节点在跑容器的一行观测。
type NodeContainerObservation struct {
	// Instance 是编排器实例标识（swarm task ID / k3s pod 名；平台不解析）。
	Instance string
	// WorkloadID 是归属 Workload ID（载体归属标记投影；Task/Database/Browse
	// 域的 workload ID 即域主体）。
	WorkloadID string
	// Domain 是归属域主体：app | task | database | browse（NodeDomain*
	// 常量；标记缺席留空）。
	Domain string
	// Team/Project/AppID/Process 是隔离域与 App 域归属投影（App 域之外的
	// 域 AppID 留空）。
	Team    string
	Project string
	AppID   string
	Process string
	// Image 是载体镜像投影（观测面，非权威声明——声明真源在 Spec/Revision）。
	Image string
}
