package config

import (
	"testing"

	"github.com/lynx-go/lynx"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 绑面字段的缺省语义（ADR-0036）：不配置 = 原行为——proxy config 仍
// ":9082"（现状硬编码平移为缺省），registry 仍停用（未设 env 的现状）；
// 钉址是显式动作，平台不静默改绑。
func TestWithDefaultsBindSurface(t *testing.T) {
	c := WithDefaults(&AppConfig{})
	assert.Equal(t, ":9080", c.GRPCAddr())
	assert.Equal(t, ":9081", c.HTTPAddr())
	assert.Equal(t, ":9082", c.ProxyConfigAddr())
	assert.Empty(t, c.RegistryAddr(), "registry addr unset = managed registry stays disabled")
}

// 显式配置的绑址原样保留（缺省只回填空值，不覆盖）。
func TestWithDefaultsPreservesConfiguredBinds(t *testing.T) {
	c := WithDefaults(&AppConfig{
		Server: &Server{
			Grpc:        &GRPC{Addr: "10.124.0.3:9080"},
			Http:        &HTTP{Addr: "127.0.0.1:9081"},
			ProxyConfig: &ProxyConfig{Addr: "10.124.0.3:9082"},
		},
		Registry: &Registry{Addr: "10.124.0.3:5000"},
	})
	assert.Equal(t, "10.124.0.3:9080", c.GRPCAddr())
	assert.Equal(t, "127.0.0.1:9081", c.HTTPAddr())
	assert.Equal(t, "10.124.0.3:9082", c.ProxyConfigAddr())
	assert.Equal(t, "10.124.0.3:5000", c.RegistryAddr())
}

// 访问器容忍 nil 链（装配侧存在只取单字段的消费点）。
func TestBindAccessorsTolerateNil(t *testing.T) {
	c := &AppConfig{}
	assert.Equal(t, DefaultGRPCAddr, c.GRPCAddr())
	assert.Equal(t, DefaultHTTPAddr, c.HTTPAddr())
	assert.Equal(t, DefaultProxyConfigAddr, c.ProxyConfigAddr())
	assert.Empty(t, c.RegistryAddr())
	assert.Equal(t, DefaultProxyConfigAddr, (*AppConfig)(nil).ProxyConfigAddr())
	assert.Empty(t, (*AppConfig)(nil).RegistryAddr())
}

// 拉取端点共享令牌（ADR-0036 N2 兑现）：缺省空 = 无认证现状（升级零
// 扰动）；显式值原样透传；nil 链安全。
func TestProxyConfigAuthToken(t *testing.T) {
	assert.Empty(t, WithDefaults(&AppConfig{}).ProxyConfigAuthToken(),
		"unset token keeps the endpoint unauthenticated (current behavior)")
	c := WithDefaults(&AppConfig{Server: &Server{ProxyConfig: &ProxyConfig{AuthToken: "proxy-secret"}}})
	assert.Equal(t, "proxy-secret", c.ProxyConfigAuthToken())
	assert.Empty(t, (*AppConfig)(nil).ProxyConfigAuthToken())
}

// Logging 面缺省语义（ADR-0040）：addr 空 = 面停用（logs 回退实时路径，
// 升级零扰动）；retention_days 空 = 30d；显式值原样透传；nil 链安全。
func TestLoggingDefaults(t *testing.T) {
	assert.Empty(t, WithDefaults(&AppConfig{}).LoggingAddr(), "unset addr keeps the logging face disabled")
	assert.Equal(t, DefaultLoggingRetentionDays, WithDefaults(&AppConfig{}).LoggingRetentionDays())
	c := WithDefaults(&AppConfig{Logging: &Logging{Addr: "10.124.0.3:9428", RetentionDays: 90}})
	assert.Equal(t, "10.124.0.3:9428", c.LoggingAddr())
	assert.Equal(t, int64(90), c.LoggingRetentionDays())
	// 非正值回退缺省（0/负 = 未配置口径）。
	c = WithDefaults(&AppConfig{Logging: &Logging{RetentionDays: -1}})
	assert.Equal(t, DefaultLoggingRetentionDays, c.LoggingRetentionDays())
	assert.Empty(t, (*AppConfig)(nil).LoggingAddr())
	assert.Equal(t, DefaultLoggingRetentionDays, (*AppConfig)(nil).LoggingRetentionDays())
}

// TestEnvPlatformBackupS3FiveTuple 钉五元组的 env 覆盖通道（ADR-0042：装配
// 在场选择与 restic 外置仓共用此块；install.sh 的 env 文件形态依赖嵌套键
// 逐叶生效——lynx 结构体通道，viper 裸 Unmarshal 不吃 env-only 嵌套键的
// 已知坑在此钉死）。
func TestEnvPlatformBackupS3FiveTuple(t *testing.T) {
	t.Setenv("FLEETLY_PLATFORM_BACKUP_S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("FLEETLY_PLATFORM_BACKUP_S3_BUCKET", "fleetly-backups")
	t.Setenv("FLEETLY_PLATFORM_BACKUP_S3_PREFIX", "platform-repo")
	t.Setenv("FLEETLY_PLATFORM_BACKUP_S3_ACCESS_KEY_ID", "ak")
	t.Setenv("FLEETLY_PLATFORM_BACKUP_S3_SECRET_ACCESS_KEY", "sk")

	v := viper.New()
	cs := lynx.NewViperConfig(v)
	require.NoError(t, ConfigureConfigSource(pflag.NewFlagSet("t", pflag.ContinueOnError), cs))

	var c AppConfig
	require.NoError(t, UnmarshalConfig(cs, &c))
	s3 := c.PlatformBackupS3()
	require.NotNil(t, s3, "the env-only five-tuple must reach the config struct")
	assert.Equal(t, "http://127.0.0.1:9000", s3.GetEndpoint())
	assert.Equal(t, "fleetly-backups", s3.GetBucket())
	assert.Equal(t, "platform-repo", s3.GetPrefix())
	assert.Equal(t, "ak", s3.GetAccessKeyId())
	assert.Equal(t, "sk", s3.GetSecretAccessKey())
}
