package config

// 缺省值集中在此（proto 无法携带默认）：WithDefaults 是唯一落点，装配
// （internal/assembly）经 UnmarshalConfig 间接触达。
const (
	// DefaultGRPCAddr 控制面 gRPC 监听地址（CLI 与 gateway 的上游）。
	DefaultGRPCAddr = ":9080"
	// DefaultHTTPAddr REST gateway 监听地址（与 gRPC 端口配对，避开常见
	// 开发机占用段 8080）。
	DefaultHTTPAddr = ":9081"
	// DefaultDataRoot 平台私有状态（SQLite/密封密钥）落盘目录；容器形态
	// 经 FLEETLY_DATA_ROOT 覆盖为 bind 卷（F0.1 安装链）。
	DefaultDataRoot = "./data"
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
	if c.GetData() == nil {
		c.Data = &Data{}
	}
	if c.Data.GetRoot() == "" {
		c.Data.Root = DefaultDataRoot
	}
	return c
}

// GRPCAddr / HTTPAddr / DataRoot 是带缺省的只读访问器（容忍 nil 链）。
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

func (c *AppConfig) DataRoot() string {
	if root := c.GetData().GetRoot(); root != "" {
		return root
	}
	return DefaultDataRoot
}
