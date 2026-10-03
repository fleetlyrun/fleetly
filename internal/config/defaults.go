package config

// 缺省值集中在此（proto 无法携带默认）：WithDefaults 是唯一落点，装配
// （internal/assembly）经 UnmarshalConfig 间接触达。
const (
	// DefaultGRPCAddr 控制面 gRPC 监听地址（CLI 与 gateway 的上游）。
	DefaultGRPCAddr = ":9080"
	// DefaultHTTPAddr REST gateway 监听地址（与 gRPC 端口配对，避开常见
	// 开发机占用段 8080）。
	DefaultHTTPAddr = ":9081"
	// DefaultEdgeConfigAddr Edge config 拉取端点（traefik HTTP provider 的
	// 控制面侧）监听地址。缺省通配绑定维持 ADR-0019 批次的现状——收窄是
	// 显式配置动作，不静默改绑（ADR-0036）；无认证端点，公网必须由防火墙
	// 封死或钉内网地址。
	DefaultEdgeConfigAddr = ":9082"
	// DefaultDataRoot 平台私有状态（SQLite/密封密钥）落盘目录；容器形态
	// 经 FLEETLY_DATA_ROOT 覆盖为 bind 卷（F0.1 安装链）。
	DefaultDataRoot = "./data"
	// DefaultScheduleOverlapPolicy 是 Schedule 重叠策略缺省（ADR-0018 A.3
	// 修订：skip = 上一拍 Run 未终态时跳过本拍；fire = 照常拍）。值域
	// 执法在 engine.ParseScheduleOverlap（fail-fast）。
	DefaultScheduleOverlapPolicy = "skip"
)

// WithDefaults 就地填充空缺省字段，返回同一实例（链式）。
func WithDefaults(c *AppConfig) *AppConfig {
	if c == nil {
		return c
	}
	if c.GetServer() == nil {
		c.Server = &Server{}
	}
	if c.Server.GetGrpc() == nil {
		c.Server.Grpc = &GRPC{}
	}
	if c.Server.GetHttp() == nil {
		c.Server.Http = &HTTP{}
	}
	if c.Server.Grpc.GetAddr() == "" {
		c.Server.Grpc.Addr = DefaultGRPCAddr
	}
	if c.Server.Http.GetAddr() == "" {
		c.Server.Http.Addr = DefaultHTTPAddr
	}
	if c.Server.GetEdgeConfig() == nil {
		c.Server.EdgeConfig = &EdgeConfig{}
	}
	if c.Server.EdgeConfig.GetAddr() == "" {
		c.Server.EdgeConfig.Addr = DefaultEdgeConfigAddr
	}
	if c.GetData() == nil {
		c.Data = &Data{}
	}
	if c.Data.GetRoot() == "" {
		c.Data.Root = DefaultDataRoot
	}
	if c.GetEngine() == nil {
		c.Engine = &Engine{}
	}
	if c.GetEngine().GetScheduleOverlapPolicy() == "" {
		c.Engine.ScheduleOverlapPolicy = DefaultScheduleOverlapPolicy
	}
	return c
}

// GRPCAddr / HTTPAddr / EdgeConfigAddr / DataRoot 是带缺省的只读访问器
// （容忍 nil 链）。
func (c *AppConfig) GRPCAddr() string {
	if addr := c.GetServer().GetGrpc().GetAddr(); addr != "" {
		return addr
	}
	return DefaultGRPCAddr
}

func (c *AppConfig) HTTPAddr() string {
	if addr := c.GetServer().GetHttp().GetAddr(); addr != "" {
		return addr
	}
	return DefaultHTTPAddr
}

// EdgeConfigAddr 是带缺省的 Edge config 拉取端点监听地址访问器（容忍 nil 链）。
func (c *AppConfig) EdgeConfigAddr() string {
	if addr := c.GetServer().GetEdgeConfig().GetAddr(); addr != "" {
		return addr
	}
	return DefaultEdgeConfigAddr
}

// RegistryAddr 是受管仓库引用地址访问器（容忍 nil 链）。无缺省可回退：
// 空值 = 受管仓库停用，与未设 env 的现状一致（ADR-0036）。
func (c *AppConfig) RegistryAddr() string {
	return c.GetRegistry().GetAddr()
}

func (c *AppConfig) DataRoot() string {
	if root := c.GetData().GetRoot(); root != "" {
		return root
	}
	return DefaultDataRoot
}

// ScheduleOverlapPolicy 是带缺省的 Schedule 重叠策略访问器（容忍 nil 链；
// 值域执法在 engine.ParseScheduleOverlap）。
func (c *AppConfig) ScheduleOverlapPolicy() string {
	if p := c.GetEngine().GetScheduleOverlapPolicy(); p != "" {
		return p
	}
	return DefaultScheduleOverlapPolicy
}
