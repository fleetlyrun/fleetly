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
	// DefaultPlatformBackupIntervalSecs 是 Platform Backup 快照节拍缺省
	//（24h，ADR-0029 决策 7 口径；ADR-0039）。
	DefaultPlatformBackupIntervalSecs = int64(86400)
	// DefaultPlatformBackupRetentionSecs 是 Platform Backup 保留窗缺省
	//（7d，restic forget --keep-within 同口径）。
	DefaultPlatformBackupRetentionSecs = int64(604800)
	// DefaultLoggingRetentionDays 是受管日志保留窗缺省（30d，VL
	// -retentionPeriod 同口径；ADR-0040）。
	DefaultLoggingRetentionDays = int64(30)
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
	if c.GetPlatformBackup() == nil {
		c.PlatformBackup = &PlatformBackup{}
	}
	if c.GetPlatformBackup().GetIntervalSecs() <= 0 {
		c.PlatformBackup.IntervalSecs = DefaultPlatformBackupIntervalSecs
	}
	if c.GetPlatformBackup().GetRetentionSecs() <= 0 {
		c.PlatformBackup.RetentionSecs = DefaultPlatformBackupRetentionSecs
	}
	if c.GetLogging() == nil {
		c.Logging = &Logging{}
	}
	if c.GetLogging().GetRetentionDays() <= 0 {
		c.Logging.RetentionDays = DefaultLoggingRetentionDays
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

// EdgeConfigAuthToken 是拉取端点共享令牌访问器（容忍 nil 链）。空 = 无
// 认证现状（ADR-0036 N2 兑现：多租户启用前必须置值）。
func (c *AppConfig) EdgeConfigAuthToken() string {
	return c.GetServer().GetEdgeConfig().GetAuthToken()
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

// PlatformBackupInterval 是快照节拍访问器（容忍 nil 链；ADR-0039）。
func (c *AppConfig) PlatformBackupInterval() int64 {
	if v := c.GetPlatformBackup().GetIntervalSecs(); v > 0 {
		return v
	}
	return DefaultPlatformBackupIntervalSecs
}

// PlatformBackupRetention 是保留窗访问器（容忍 nil 链；ADR-0039）。
func (c *AppConfig) PlatformBackupRetention() int64 {
	if v := c.GetPlatformBackup().GetRetentionSecs(); v > 0 {
		return v
	}
	return DefaultPlatformBackupRetentionSecs
}

// PlatformBackupS3 返回外置仓配置（nil = 未配置——仅本地仓的诚实边界）。
func (c *AppConfig) PlatformBackupS3() *PlatformBackupS3 {
	s3 := c.GetPlatformBackup().GetS3()
	if s3 == nil || s3.GetEndpoint() == "" || s3.GetBucket() == "" {
		return nil
	}
	return s3
}

// LoggingAddr 是受管日志存储端点访问器（容忍 nil 链）。无缺省可回退：
// 空值 = Logging 面停用（ADR-0040——logs 回退 Runtime 实时路径）。
func (c *AppConfig) LoggingAddr() string {
	return c.GetLogging().GetAddr()
}

// LoggingRetentionDays 是日志保留窗天数访问器（容忍 nil 链；ADR-0040）。
func (c *AppConfig) LoggingRetentionDays() int64 {
	if v := c.GetLogging().GetRetentionDays(); v > 0 {
		return v
	}
	return DefaultLoggingRetentionDays
}
