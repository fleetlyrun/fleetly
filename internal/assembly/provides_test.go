package assembly

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lynx-go/lynx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// TestNewEngineOverlapPolicyFailsFast（ADR-0017 附录 A.4）：无效重叠策略
// 在触碰任何引擎依赖之前拒绝（fail-fast——配置错误不得静默回退 skip，
// 也不得拖到首个到期拍才暴露）。依赖参数保持 nil：校验先行，不物化引擎。
func TestNewEngineOverlapPolicyFailsFast(t *testing.T) {
	cfg := config.WithDefaults(&config.AppConfig{})
	cfg.Engine = &config.Engine{ScheduleOverlapPolicy: "queue"}
	_, err := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schedule_overlap_policy")
	assert.Contains(t, err.Error(), "queue")
}

// TestObjectStoreSelection（ADR-0042 决策 2 装配选择锚）：platform_backup.s3
// 五元组缺席 → local、不注入配置（升级零扰动）；在场 → s3 且五元组经装配
// ctx 完整注入（含 endpoint/bucket/凭证——config 是唯一契约源）。选择谓词
// 与告警解除谓词同源（PlatformBackupS3 访问器单真源）。
func TestObjectStoreSelection(t *testing.T) {
	name, ctx := objectStoreSelection(config.WithDefaults(&config.AppConfig{}))
	assert.Equal(t, "local", name)
	assert.Nil(t, capability.ObjectStoreS3FromContext(ctx), "absent five-tuple must not inject the s3 config")

	cfg := config.WithDefaults(&config.AppConfig{})
	cfg.PlatformBackup = &config.PlatformBackup{S3: &config.PlatformBackupS3{
		Endpoint: "minio.example.com:9000", Bucket: "fleetly-backups",
		AccessKeyId: "ak", SecretAccessKey: "sk",
	}}
	name, ctx = objectStoreSelection(cfg)
	assert.Equal(t, "s3", name)
	got := capability.ObjectStoreS3FromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "minio.example.com:9000", got.Endpoint)
	assert.Equal(t, "fleetly-backups", got.Bucket)
	assert.Equal(t, "ak", got.AccessKeyID)
	assert.Equal(t, "sk", got.SecretAccessKey)

	// 半配置（endpoint 或 bucket 空）= 访问器判缺席 = local（与 restic 仓
	// 的 nil 语义同一访问器，不出现"配了半截切 s3"形态）。
	cfg.PlatformBackup.S3.Endpoint = ""
	name, ctx = objectStoreSelection(cfg)
	assert.Equal(t, "local", name)
	assert.Nil(t, capability.ObjectStoreS3FromContext(ctx))
}

// metricsFaceApp 是 NewMetricsProvider 的最小 App 夹具：嵌入接口零值
// （未触碰的成员 panic——装配面只用 Logger，越界即测试红）+ 显式 Logger。
type metricsFaceApp struct {
	lynx.App
	log *slog.Logger
}

func (a *metricsFaceApp) Logger(_ ...any) *slog.Logger { return a.log }

// fakeMetricsProvider 是正形态对照的假 Provider（Metrics 端口最小实现）。
type fakeMetricsProvider struct{}

func (fakeMetricsProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "assembly-test-metrics", Capability: capability.KindMetrics}
}
func (fakeMetricsProvider) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (fakeMetricsProvider) Managed() bool { return false }
func (fakeMetricsProvider) ImportPrometheus(_ context.Context, _ []byte, _ map[string]string) error {
	return nil
}
func (fakeMetricsProvider) QuerySeries(_ context.Context, _ string, _, _ time.Time, _ time.Duration) (capability.Series, error) {
	return capability.Series{}, nil
}

// TestMetricsFaceDisabledWhenAddrEmpty（ADR-0041 锚 7 装配面）：metrics.addr
// 空 = Metrics 面停用——工厂在册的同一夹具下仍返回 nil Provider：装配不
// 注入受管声明（VM/cadvisor 的受管 Workload 无从进入 reconciler 集——
// "无新受管服务、零采集零告警"），addr 在场正形态对照证明 nil 结果来自
// 地址门而非工厂缺席。查询精确失败由 apitest TestMetricsQueryDisabledFace
// 承载（E_INTERNAL 信封）。
func TestMetricsFaceDisabledWhenAddrEmpty(t *testing.T) {
	capability.RegisterFactory(capability.KindMetrics, "assembly-test-metrics", func(ctx context.Context) (capability.Provider, error) {
		if capability.MetricsAddrFromContext(ctx) == "" {
			return nil, fmt.Errorf("test factory must not be built without a metrics address")
		}
		return fakeMetricsProvider{}, nil
	})
	app := &metricsFaceApp{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	cfg := config.WithDefaults(&config.AppConfig{})
	require.Empty(t, cfg.MetricsAddr(), "fixture precondition: no metrics address configured")
	m, cleanup, err := NewMetricsProvider(app, cfg)
	require.NoError(t, err)
	assert.Nil(t, m, "empty metrics.addr must not build the managed metrics provider (no managed workloads injected)")
	require.NotNil(t, cleanup)
	cleanup()

	cfg.Metrics = &config.Metrics{Addr: "10.0.0.1:8428"}
	m2, cleanup2, err2 := NewMetricsProvider(app, cfg)
	require.NoError(t, err2)
	assert.NotNil(t, m2, "a configured address must build the provider (positive control for the gate)")
	require.NotNil(t, cleanup2)
	cleanup2()
}
