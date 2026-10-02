package capability

import (
	"context"
	"io"
	"time"
)

// Builder 是构建 Capability 端口：从 Source 产出镜像（ADR-0019：Build 恒
// 在控制面节点执行，BuildKit + 本机 daemon，并发上限可配，缓存只在本机）。
// 状态机（queued → building → succeeded|failed|cancelled|expired）由
// engine 持有，端口只承接单次构建执行。
type Builder interface {
	Provider

	// Build 执行一次构建：w.Progress 流式接收构建日志帧（实时流，F0.9）；
	// 返回不可变 digest。ctx 取消即中止（cancelled 由 engine 落状态）。
	Build(ctx context.Context, req BuildRequest, w LogWriter) (BuildResult, error)
}

// BuildRequest 是一次构建的输入。
type BuildRequest struct {
	// BuildID 是平台构建 ID（进度/日志归属锚）。
	BuildID string
	// Source 是构建上下文（git 检出目录或上传产物目录，控制面本地路径）。
	ContextDir string
	// Dockerfile 是相对 ContextDir 的路径；空 = "Dockerfile"。
	Dockerfile string
	// Args 是构建参数（已解析的非敏感 ARG）。
	Args map[string]string
	// CacheFrom 是缓存来源引用。
	CacheFrom []string
	// Target 是镜像推送目标（含 tag；digest 由 Registry 回填）。
	Target string
	// PushCred 是推送目标仓库的凭证（nil = 匿名推送；受管仓库由平台
	// 注入，ADR-0019 附录 B.3）。
	PushCred *RegistryCredential
	// SecretFiles 是 Secret 材料落盘（路径 → 值；构建期临时，Provider
	// 负责不落最终镜像层）。
	SecretFiles map[string][]byte
}

// BuildResult 是构建产物。
type BuildResult struct {
	Digest string
}

// Edge 是流量接入 Capability 端口：Route 发布与证书管理（CONTEXT.md
// Edge/Route/Certificate 词条）。受管 Provider（traefik）以普通 Workload
// 形态自宿（ADR-0004），本端口承接配置发布。
type Edge interface {
	Provider

	// PublishRoutes 幂等发布全量 Route 集（控制面强制全量配置防裸 {}
	// 清空——旧 spike 教训）。Route.BackendAddr 由 engine 发布前经
	// Runtime.Addresses 解析填充（Provider 互不 import，地址经端口传入）。
	PublishRoutes(ctx context.Context, routes []Route) error
	// IssueCertificate 申请/续期一张证书（ACME 托管或上传材料入库）。
	IssueCertificate(ctx context.Context, req CertificateRequest, w ChallengeWriter) (CertificateStatus, error)
}

// Managed 是受管自宿 Provider 的部署形态声明接口（ADR-0004：唯一通用
// reconciler 驱动；Provider 只声明"我是一条普通 Workload"，不写部署器）。
// Describe().Managed == true 的 Provider 应实现本接口。
type Managed interface {
	// ManagedWorkloads 返回该 Provider 的受管部署形态（镜像钉版 + 完整
	// 声明；reconciler 经 Runtime Ensure 下发，与用户 Workload 同通道）。
	ManagedWorkloads() []Workload
	// ManagedNamespace 返回受管隔离域（平台系统域，与用户 Project 分离）。
	ManagedNamespace() NamespaceRef
}

// MaterialsSource 是受管 Provider 的材料声明子面（可选；ConfigSource 同款
// 形态）：需要向受管 Workload 注入文件材料（如 zot 的 config/htpasswd）的
// Provider 实现，reconciler 经 Runtime Ensure 的 Materials 通道下发（值走
// secret 载体，不落 env/label，ADR-0014）。
type MaterialsSource interface {
	// ManagedMaterials 返回受管域的材料集（幂等纯函数——值源自平台持久
	// 状态，如数据根凭证文件；值变更=载体指纹变更=滚动替换）。
	ManagedMaterials() Materials
}

// ConfigSource 是受管 Edge 的配置拉取数据面（Provider 经 HTTP provider
// 轮询控制面时，控制面端点从本子面取全量配置快照；非拉取型 Provider
// 不实现）。
type ConfigSource interface {
	// ConfigSnapshot 返回当前全量动态配置（永不返回裸空对象——控制面
	// 强制全量配置，防清空事故）。
	ConfigSnapshot() []byte
}

// Route 是 host/path → Process 端口映射（附协议与 TLS 模式）。
type Route struct {
	// Host 是完整主机名（含 sslip.io 形态）；Path 是路径前缀（空 = /）。
	Host string
	Path string
	// Target 是目的 Project/App/Process + 端口。
	Target  NamespaceRef
	Process string
	Port    int32
	// Protocol 是后端协议（http/h2c/tcp；h2c = 明文 HTTP/2，messageloop
	// 形态）。
	Protocol Protocol
	// TLS 模式：auto（ACME）/ none（明文，仅 sslip.io 调试）。
	TLS string
	// BackendAddr 是发布时解析的后端地址（Runtime.Addresses 产物，
	// host:port 形态；Edge Provider 不再反查 Runtime）。
	BackendAddr string
}

// CertificateRequest 是证书申请。
type CertificateRequest struct {
	// Domains 是 SAN 集。
	Domains []string
	// HTTP01 通过受管 Edge 自身完成 challenge；材料写入由 ChallengeWriter
	// 承接。
	HTTP01 bool
}

// ChallengeWriter 是 ACME challenge 材料接收端（http-01 落盘）。
type ChallengeWriter interface {
	WriteChallenge(ctx context.Context, token, keyAuth string) error
}

// CertificateStatus 是证书状态。
type CertificateStatus struct {
	Domains   []string
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
}

// Registry 是 OCI 镜像仓库 Capability 端口（拉取来源与推送目标；zot 受管
// 自宿为默认）。
type Registry interface {
	Provider

	// Endpoint 返回推送/拉取端点（含凭据材料，Ensure 分发用）。
	Endpoint(ctx context.Context) (RegistryEndpoint, error)
}

// RegistryEndpoint 是仓库端点描述。
type RegistryEndpoint struct {
	Addr string
	Cred RegistryCredential
}

// Logging 是日志 Capability 端口（VictoriaLogs 受管自宿为默认；N2 持久
// 检索，N0 诚实标注"仅实时+最近缓冲"）。
type Logging interface {
	Provider

	// Ingest 摄入一批日志帧（采集回拨通道）。
	Ingest(ctx context.Context, frames []LogFrame) error
	// Query 持久化检索（时间/文本/容器过滤；N2 落地）。
	Query(ctx context.Context, q LogQuery, w LogWriter) error
}

// Metrics 是指标 Capability 端口（victoria 系受管自宿；容器 CPU/内存基础
// 图表 + 阈值告警，N2）。
type Metrics interface {
	Provider

	// QuerySeries 查询指标序列（PromQL 子集透传；N2 落地）。
	QuerySeries(ctx context.Context, query string, start, end time.Time) (Series, error)
}

// Series 是一段指标序列。
type Series struct {
	Metric map[string]string
	Points []SeriesPoint
}

// SeriesPoint 是一个采样点。
type SeriesPoint struct {
	Time  time.Time
	Value float64
}

// ObjectStore 是 S3 兼容对象存储 Capability 端口（Backup 与产物承载；
// 默认本地备份目标开箱即用，外置 S3 可配，ADR-0020）。
type ObjectStore interface {
	Provider

	// Put 上传一个对象。
	Put(ctx context.Context, key string, r io.Reader) error
	// Get 下载一个对象。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List 列举前缀下对象键。
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete 删除对象（保留策略执行器用）。
	Delete(ctx context.Context, key string) error
}
