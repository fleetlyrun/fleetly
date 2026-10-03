package capability

import (
	"context"
	"errors"
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
	// 既有），含轮换。rotate=true 先作废全部现有材料（泄漏处置：旧
	// token 即刻失效）再返回新材料——活材料等价集群成员权，动词面与
	// 授权档位都按此敏感度对待（C3）。
	Enrollment(ctx context.Context, rotate bool) (EnrollKit, error)
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

// RuntimeInspector 是观测子面（ADR-0022 spec 对照 drift：按需实现；未
// 实现时引擎降级为 gen-only 对照，诚实明示不阻断）。
type RuntimeInspector interface {
	// InspectWorkloads 返回隔离域内平台管辖载体的观测 spec（标记还原
	// 平台身份；spec 字段是编排器原语的平台无关投影）。快照语义：调用
	// 即读，无流式承诺。
	InspectWorkloads(ctx context.Context, ns NamespaceRef) ([]WorkloadObservation, error)
}

// RuntimeHygiene 是载体卫生子面（收尾批 E29：孤儿 Secret 载体清理；按
// 需实现——未实现时卫生清扫静默跳过，与 Inspector 的降级文化一致）。
// 与 WorkloadOrphaned 的"只登记永不自动删"分立：那是 Workload 载体观测
// 面（归属不明的用户域状态须人裁）；这里是材料通道的派生副本——现役值
// 真源在平台侧（ADR-0014），无引用副本删除零信息损失。
type RuntimeHygiene interface {
	// SweepOrphanSecrets 删除非现役的受管 Secret 载体（现役集由 Provider
	// 依现存服务的引用关系自判定，平台无需下发期望集）。幂等：已不存在
	// 不计错。maxDelete 是单次调用删除上限（调用方节拍限流防 API 风暴）。
	// 返回实际删除数；列表级错误上抛，单体删除失败不中断（计入下一拍）。
	SweepOrphanSecrets(ctx context.Context, maxDelete int) (int, error)
}

// WorkloadObservation 是一条载体观测（ADR-0022：drift spec 对照的数据
// 面——字段只增；未观测字段零值 = 该 Provider 无此面）。
type WorkloadObservation struct {
	WorkloadID string
	Generation Generation
	Image      string
	// Command 是载体上的入口覆盖命令观测（ADR-0022 承诺的 spec 对照面；
	// nil 与空切片等价 = 无覆盖/镜像默认）。
	Command  []string
	Replicas int64
	State    WorkloadState
}

// ErrNodeNotFound 是 RuntimeAdmin 子面哨兵：平台节点 ID 对不上任何载体
// 节点。Provider 返回时 wrap 本哨兵，API 面映射 E_NOT_FOUND。
var ErrNodeNotFound = errors.New("node not found")

// Generation 是某次已下发 Spec 的单调编号（幂等与 Drift 判定的锚，
// CONTEXT.md Generation 词条）。
type Generation uint64

// NamespaceRef 标识 Provider 侧隔离域；字段为平台实体标识，不含编排器概念。
// .App 是 App 域主体（AppSpec 投影的 Workload 集合边界）；.Task 是 Task 域
// 主体（Run Workload 池边界，App 为空时有效——ADR-0025 决策 4：拒把 Task ID
// 塞 .App 字段，词汇污染）；.Database 是 Database 域主体（用户域受管数据
// 服务边界，App/Task 为空时有效——ADR-0029 同款词汇分立）。
type NamespaceRef struct {
	Team     string
	Project  string
	App      string
	Task     string
	Database string
}

// String 返回稳定展示形态（日志/审计用）。
func (n NamespaceRef) String() string {
	if n.Task != "" {
		return n.Team + "/" + n.Project + "/task:" + n.Task
	}
	if n.Database != "" {
		return n.Team + "/" + n.Project + "/db:" + n.Database
	}
	return n.Team + "/" + n.Project + "/" + n.App
}

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
	// NetworkRefs 是跨隔离域网络挂靠（受管面专用形态）：引用另一 Project
	// 的平台网络——载体名解析是 Provider 私有公式，engine 不拼载体名
	//（N0 修复批 B1：受管 Edge 挂全部活跃 Project 网络以达后端）。同域
	// 附件不由此面表达（同域直接用 Networks 平台名）。
	NetworkRefs []NetworkRef
	// Publish 是宿主端口发布声明（平台无关形态）。常规用户 Workload 不
	// 发布宿主端口（流量一律经 Edge，架构坑清单）；受管 Edge 自身例外
	//（80/443 入站是其部署形态的一部分）。
	Publish []PortPublish
	// Restart 是生命周期声明（ADR-0025 决策 1，语义按 ADR-0012 停止原因
	// 映射：长运行=any、one-shot Run=never——进程退出即终态，池形态由
	// 平台补足）。零值 = Provider 缺省（长运行 any）。
	Restart RestartPolicy
	// StopGrace 是停止宽限（SIGTERM 后强制 SIGKILL 前的等待窗；零值 =
	// Provider 缺省）。
	StopGrace time.Duration
	// Addressing 是平台标准 DNS 名声明（ADR-0025 决策 6/R-3：铸名公式住
	// engine——名字是平台 API 面，N4 换 Runtime 不变；Provider 把声明映射
	// 为自己的原语：swarm=网络别名、k8s=Service 名）。
	Addressing []Address
}

// RestartPolicy 是 Workload 生命周期声明（ADR-0025）。
type RestartPolicy string

const (
	// RestartDefault 是零值：Provider 缺省（等价长运行语义）。
	RestartDefault RestartPolicy = ""
	// RestartAlways 是长运行语义：进程退出由编排器重启（swarm: any；
	// k8s: Always）。
	RestartAlways RestartPolicy = "always"
	// RestartNever 是一次性语义：退出即终态、不重启（swarm: none；k8s:
	// Never）——Run Workload 一律 never，补足由平台池语义承担。
	RestartNever RestartPolicy = "never"
)

// Address 是一条平台标准 DNS 名声明（engine 铸名，Provider 映射原语）。
type Address struct {
	// Name 是平台标准 DNS 裸名（网络内可解析；如 task-<id> 池级轮询、
	// run-<id> per-Run 稳定名）。
	Name string
}

// NetworkRef 是一条跨隔离域网络引用（Namespace 定位网络归属域，Name 是
// 该域内的平台网络名——与 Networks 元素同词汇，仅多域限定）。
type NetworkRef struct {
	Namespace NamespaceRef
	Name      string
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
	// ReadOnly 是只读挂载（compose 短语法 name:/target:ro；缺省可写）。
	ReadOnly bool
}

// Materials 是 Ensure 携带的分发材料（ADR-0014）：镜像拉取凭证与 Secret
// 注入材料，Provider 按节点分发，不落载体 label 或明文 env。
type Materials struct {
	// RegistryAuth 是私有镜像拉取凭证（server 地址 → 凭证）。
	RegistryAuth map[string]RegistryCredential
	// SecretFiles 是 Secret 注入材料（名 → 值；Provider 翻译为文件注入
	//（swarm: /run/secrets/<name>），值不落 label 或明文 env）。
	SecretFiles map[string][]byte
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
	WorkloadPending   WorkloadState = "pending"
	WorkloadRunning   WorkloadState = "running"
	WorkloadDegraded  WorkloadState = "degraded" // 副本部分失联/重启循环
	WorkloadStopped   WorkloadState = "stopped"
	WorkloadOrphaned  WorkloadState = "orphaned"  // 对不上账：只登记永不自动删
	WorkloadCompleted WorkloadState = "completed" // one-shot 正常完成终态（退出码 0，ADR-0025 决策 2）
	WorkloadFailed    WorkloadState = "failed"    // 一次性失败终态（退出码非 0 / rejected——不再被 degraded 吞并，ADR-0025 决策 2）
)

// Terminal 报告是否一次性终态观测（one-shot 语义面；长运行 Workload 的
// 失联/劣化仍走 degraded/stopped，不由此判定）。
func (s WorkloadState) Terminal() bool {
	return s == WorkloadCompleted || s == WorkloadFailed
}

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
	// ExitCode 是终态退出码（nil = 未观测/非终态观测；ADR-0025 决策 2）。
	ExitCode *int
	// Reason 是编排器侧终态补充原文（容器退出原因等；用户可见文本英文）。
	Reason string
	// Instance 是实例身份（编排器 task ID / slot——Run 观测对账锚，
	// ADR-0025 决策 2）。
	Instance string

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
