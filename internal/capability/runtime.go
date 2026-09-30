package capability

import (
	"context"
	"time"
)

// Runtime 是编排器 Capability 端口（架构 §5，窄面契约：核心 6 方法 +
// 三个可选子面）。期望状态式：唯一写动词 Ensure，副本数/健康检查变化都
// 是新 Generation 的 Ensure；回滚在平台层是 Replay，Runtime 无回滚动词。
//
// NamespaceRef 是 Provider 侧隔离域锚（架构 §5 契约类型）：标识一次
// Ensure 所辖的 Workload 集合边界，Provider 在域内做增量收敛（域内多余
// 载体随 Ensure 移除）。命名/标记是 Provider 私有，平台永不解析。
type Runtime interface {
	Provider

	// Ensure 幂等下发期望状态：同 Generation 重放安全（进程被杀后按
	// Generation 幂等重下发，领域模型场景 1）。Materials 携带平台已解析
	// 的镜像凭证与注入材料，Provider 按节点分发（swarm
	// --with-registry-auth 等价）；凭证不落载体 label 或明文 env
	//（ADR-0014，旧 DT-2 真机 404 教训）。
	Ensure(ctx context.Context, ns NamespaceRef, ws []Workload, gen Generation, m Materials) error

	// Remove 拆除隔离域内全部载体（幂等；已不存在的对象不报错）。
	Remove(ctx context.Context, ns NamespaceRef) error

	// Watch 返回全集群状态流：Workload 状态 + 节点加入/离开事件
	//（含 node.joined 锚定上报）；engine 按 ID 归属过滤。Drift 对照最近
	// Ensure 的 Generation 在此流上报（drift 信号，非平台解析载体命名）。
	Watch(ctx context.Context) (<-chan WorkloadEvent, error)

	// Addresses 返回隔离域的可达地址（VIP/DNS 端点等平台无关形态）。
	Addresses(ctx context.Context, ns NamespaceRef) ([]Endpoint, error)

	// DescribeCluster 返回集群观测视图（节点缓存，非权威——平台以 ID
	// 查权威表判定归属）。
	DescribeCluster(ctx context.Context) (ClusterView, error)

	// Enrollment 生成节点加入材料（swarm join 材料；k8s 节点 kubelet
	// 既有），含轮换。
	Enrollment(ctx context.Context) (EnrollKit, error)
}

// RuntimeLogs 是日志子面（F0.25 `fleetly logs` 消费；按需实现）。
type RuntimeLogs interface {
	// StreamLogs 流式读取容器日志；Follow 持续跟随；TailLines/容器过滤
	// 由调用方给定。
	StreamLogs(ctx context.Context, q LogQuery, w LogWriter) error
}

// RuntimeAdmin 是管理子面（`fleetly nodes` 运维操作；按需实现）。
type RuntimeAdmin interface {
	// Drain 把节点置为排空（不再调度新载体，存量按编排器语义迁移/回收）。
	Drain(ctx context.Context, nodeID string) error
	// Cordon 封锁节点（拒绝新调度，存量不动）。
	Cordon(ctx context.Context, nodeID string) error
	// Uncordon 解除封锁。
	Uncordon(ctx context.Context, nodeID string) error
}

// Generation 是某次已下发 Spec 的单调编号（幂等与 Drift 判定的锚，
// CONTEXT.md Generation 词条）。
type Generation uint64

// NamespaceRef 标识 Provider 侧隔离域（Team/Project/App 级）；字段为平台
// 实体标识，不含编排器概念。
type NamespaceRef struct {
	Team    string
	Project string
	App     string
}

// String 返回稳定展示形态（日志/审计用）。
func (n NamespaceRef) String() string { return n.Team + "/" + n.Project + "/" + n.App }

// Protocol 是 Route/端口协议（CONTEXT.md Route 词条：http/h2c/tcp）。
type Protocol string

const (
	ProtocolHTTP Protocol = "http"
	ProtocolH2C  Protocol = "h2c"
	ProtocolTCP  Protocol = "tcp"
)

// Workload 是 Runtime 接受的最小执行单元（由 Spec 投影而来；平台不感知
// 载体形态）。探针、卷钉住、网络附件在 IR 是声明，映射成编排器原语是
// Provider 的事。
type Workload struct {
	// ID 是平台 Workload ID（ULID）：归属与 Drift 判定的唯一锚，Provider
	// 把它搬运到载体标记上，平台永远查权威表、不解析载体命名。
	ID string
	// Process 是 App 内进程模板名（web/worker…）。
	Process string
	// Image 是镜像引用（digest 形态优先；Build 产物 digest 由平台解析后
	// 下发）。
	Image string
	// Command 覆盖镜像入口；空 = 镜像默认。
	Command []string
	// Env 是已解析的非敏感环境变量（变量两级合成的最终形态）。
	Env map[string]string
	// Ports 是进程声明的监听端口与协议（Route 投影的来源）。
	Ports []WorkloadPort
	// Replicas 是期望副本数。
	Replicas int64
	// Healthcheck 是声明式探针（http/tcp/exec + 宽限）。
	Healthcheck *Healthcheck
	// Resources 是每副本资源上限（v1 仅每 Workload 上限，架构 §10）。
	Resources *Resources
	// Placement 是调度意图（节点选择约束 + 卷钉住；平台节点 ID 为锚）。
	Placement Placement
	// Volumes 是持久存储附件（默认钉住节点）。
	Volumes []VolumeMount
	// Networks 是网络附件（Project 网络名或 taskGroup:<name> 跨挂）。
	Networks []string
	// Publish 是宿主端口发布声明（平台无关形态）。常规用户 Workload 不
	// 发布宿主端口（流量一律经 Edge，架构坑清单）；受管 Edge 自身例外
	//（80/443 入站是其部署形态的一部分）。
	Publish []PortPublish
}

// PortPublish 是一条宿主端口发布（PublishedPort 宿主侧；TargetPort 容器
// 侧；swarm 翻译为 routing mesh 发布，k8s 翻译为 NodePort/LoadBalancer）。
type PortPublish struct {
	PublishedPort int32
	TargetPort    int32
}

// WorkloadPort 是进程监听端口声明。
type WorkloadPort struct {
	Port     int32
	Protocol Protocol
}

// Healthcheck 是声明式健康探针（L1 健康门数据源）。
type Healthcheck struct {
	// 三选一：HTTPPath/TCPPort/Exec 至少其一。
	HTTPPath string
	TCPPort  int32
	Exec     []string
	// Interval/Timeout/StartPeriod 是探测节律；Retries 是连续失败阈值。
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int32
}

// Resources 是每副本资源上限。
type Resources struct {
	CPUMillis int64 // 毫核（1000 = 1 CPU）
	MemoryMB  int64
}

// Placement 是调度意图（CONTEXT.md Placement 词条：节点选择约束与卷
// 钉住；约束以平台节点 ID 为锚，映射为编排器约束语法是 Provider 私有）。
type Placement struct {
	// NodeIDs 是节点选择约束（平台节点 ID 集；空 = 不约束）。
	NodeIDs []string
}

// VolumeMount 是 Volume 附件。
type VolumeMount struct {
	// VolumeID 是平台 Volume ID（钉住节点解析由平台完成后经 Placement
	// 下发锚点）。
	VolumeID string
	// Target 是容器内挂载路径。
	Target string
}

// Materials 是 Ensure 携带的分发材料（ADR-0014）：镜像拉取凭证与 Secret
// 注入材料，Provider 按节点分发，不落载体 label 或明文 env。
type Materials struct {
	// RegistryAuth 是私有镜像拉取凭证（server 地址 → 凭证）。
	RegistryAuth map[string]RegistryCredential
}

// RegistryCredential 是一个 registry 的拉取凭证。
type RegistryCredential struct {
	Server   string
	Username string
	Secret   string
}

// WorkloadState 是载体观测状态（观测缓存，不参与决策——参与决策前必直读）。
type WorkloadState string

const (
	WorkloadPending  WorkloadState = "pending"
	WorkloadRunning  WorkloadState = "running"
	WorkloadDegraded WorkloadState = "degraded" // 副本部分失联/重启循环
	WorkloadStopped  WorkloadState = "stopped"
	WorkloadOrphaned WorkloadState = "orphaned" // 对不上账：只登记永不自动删
)

// WorkloadEvent 是 Watch 流元素：状态迁移的既成事实。
type WorkloadEvent struct {
	// WorkloadID 是平台 Workload ID（归属判定锚）。
	WorkloadID string
	// Generation 是观测到的 Generation（载体标记搬运值）；Drift 时与平台
	// 已下发值不一致。
	Generation Generation
	// State 是观测状态。
	State WorkloadState
	// Drift 标记实际状态偏离已下发 Spec（检测默认开，收敛默认 opt-in，
	// ADR-0005）。
	Drift bool
	// Node 是观测节点（平台节点 ID）。
	Node string
	// Message 是人读补充（容器退出原因等；用户可见文本英文）。
	Message string

	// NodeJoined 非空时是节点加入事件（Provider 完成平台 ID 锚定后的
	// node.joined 上报：首次观测 → 铸造平台 ID → 写回载体标记 → 事件）。
	NodeJoined *NodeJoined
}

// NodeJoined 是节点加入事实（架构 §5 节点身份锚定契约义务）。
type NodeJoined struct {
	// NodeID 是平台节点 ID（永不复用）。
	NodeID string
	// CarrierID 是 Provider 载体上的节点标识（swarm NodeID 等；观测数据）。
	CarrierID string
	// Minted 为 true 表示本次事件由无标记节点铸造新平台 ID（写回已由
	// Provider 完成）。
	Minted bool
}

// Endpoint 是平台无关的可达地址形态。
type Endpoint struct {
	// Host:Port（VIP/DNS 形态）。
	Addr string
	// Process/Port 来源标注。
	Process string
	Port    int32
}

// ClusterView 是集群观测快照。
type ClusterView struct {
	// Nodes 是节点观测缓存（权威归属判定永远查平台表）。
	Nodes []NodeView
}

// NodeView 是节点观测（CONTEXT.md Node 词条：集群内一台机器，观测对象）。
type NodeView struct {
	// NodeID 是平台节点 ID（锚定产物）。
	NodeID string
	// CarrierID 是 Provider 载体节点标识。
	CarrierID string
	// Hostname 是观测主机名。
	Hostname string
	// Role 是编排器角色观测（manager/worker；镜像内事实，非平台语义）。
	Role string
	// Available 是节点可用性观测。
	Available bool
	// Labels 是节点标记观测（含 fleetly.* 平台锚定标记）。
	Labels map[string]string
}

// EnrollKit 是节点加入材料（由 Runtime Provider 生成与轮换）。
type EnrollKit struct {
	// Command 是工作节点执行的完整加入命令（节点零平台安装物：加入材料
	// 即全部所需）。
	Command string
	// ManagerCommand 是追加 manager 的加入命令（HA 扩容用；v1 延后）。
	ManagerCommand string
	// ExpiresAt 是材料时效观测。
	ExpiresAt time.Time
}

// LogQuery 是日志读取查询（时间窗/tail/容器过滤）。
type LogQuery struct {
	// Namespace 限定隔离域；WorkloadID 进一步限定单个 Workload。
	Namespace  NamespaceRef
	WorkloadID string
	// Since/Until 是时间窗（零值 = 不限）。
	Since, Until time.Time
	// TailLines 是尾部行数（0 = 全量）。
	TailLines int64
	// Follow 持续跟随。
	Follow bool
}

// LogWriter 是日志帧接收端（流式背压由实现负责）。
type LogWriter interface {
	// WriteLog 写一帧；返回错误终止流。
	WriteLog(ctx context.Context, frame LogFrame) error
}

// LogFrame 是一帧容器日志。
type LogFrame struct {
	WorkloadID string
	Container  string
	Node       string
	Time       time.Time
	Line       []byte
}
