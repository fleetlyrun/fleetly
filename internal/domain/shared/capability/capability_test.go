package capability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider 是注册表行为的测试替身。
type fakeProvider struct {
	descriptor ProviderDescriptor
}

func (f *fakeProvider) Describe() ProviderDescriptor        { return f.descriptor }
func (f *fakeProvider) Health(context.Context) HealthReport { return HealthReport{Healthy: true} }

func TestFactoryRegistry(t *testing.T) {
	ResetFactories()
	t.Cleanup(ResetFactories)

	RegisterFactory(KindRuntime, "fake", func(ctx context.Context, p FactoryParams) (Provider, error) {
		return &fakeProvider{descriptor: ProviderDescriptor{Name: "fake", Capability: KindRuntime}}, nil
	})

	// 空名直取唯一在册者。
	p, err := Build[*fakeProvider](context.Background(), KindRuntime, "", FactoryParams{})
	require.NoError(t, err)
	assert.Equal(t, "fake", p.Describe().Name)

	// 指名构造。
	p, err = Build[*fakeProvider](context.Background(), KindRuntime, "fake", FactoryParams{})
	require.NoError(t, err)
	assert.Equal(t, KindRuntime, p.Describe().Capability)

	// 未知名报错并列出候选。
	_, err = Build[*fakeProvider](context.Background(), KindRuntime, "nope", FactoryParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake")

	// 重复注册 panic（装配错误启动期暴露）。
	assert.Panics(t, func() {
		RegisterFactory(KindRuntime, "fake", func(context.Context, FactoryParams) (Provider, error) { return nil, nil })
	})
}

// TestBuildPortAssertionFailsLoud（提案 2026-11-05 C2）：工厂产物未实现
// 请求端口 → 断言入库内精确失败（此前是装配侧 8 处样板）；报错带 Provider
// 自描述名与在册候选。
func TestBuildPortAssertionFailsLoud(t *testing.T) {
	ResetFactories()
	t.Cleanup(ResetFactories)

	RegisterFactory(KindRuntime, "fake", func(context.Context, FactoryParams) (Provider, error) {
		return &fakeProvider{descriptor: ProviderDescriptor{Name: "fake", Capability: KindRuntime}}, nil
	})

	_, err := Build[Registry](context.Background(), KindRuntime, "fake", FactoryParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider fake does not implement the port requested for runtime")
	assert.Contains(t, err.Error(), "fake", "the error lists registered candidates")
}

// TestMultipleRegistrationErrorPerKind（提案 2026-11-05 C4）：空名多注册的
// 报错按 kind 参数化——Runtime 有真旋钮（runtime.provider），报错指名它；
// 其余 kind 无选择旋钮，报错明说只许编译进一个（不得误指不存在的键）。
func TestMultipleRegistrationErrorPerKind(t *testing.T) {
	ResetFactories()
	t.Cleanup(ResetFactories)

	for _, name := range []string{"fake-a", "fake-b"} {
		RegisterFactory(KindRuntime, name, func(context.Context, FactoryParams) (Provider, error) {
			return &fakeProvider{descriptor: ProviderDescriptor{Name: name, Capability: KindRuntime}}, nil
		})
		RegisterFactory(KindRegistry, name, func(context.Context, FactoryParams) (Provider, error) {
			return &fakeProvider{descriptor: ProviderDescriptor{Name: name, Capability: KindRegistry}}, nil
		})
	}

	_, err := Build[*fakeProvider](context.Background(), KindRuntime, "", FactoryParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple providers registered for runtime")
	assert.Contains(t, err.Error(), "runtime.provider", "runtime has a selector knob and the error must name it")
	assert.Contains(t, err.Error(), "fake-a")
	assert.NotContains(t, err.Error(), "no name selector", "runtime has a selector knob; claiming none would lie")

	_, err = Build[*fakeProvider](context.Background(), KindRegistry, "", FactoryParams{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple providers registered for registry")
	assert.Contains(t, err.Error(), "no name selector")
	assert.Contains(t, err.Error(), "only one provider may be compiled in")
}

// TestFactoryParamsPassThrough：FactoryParams 逐字段透传到工厂闭包（载体
// 收拢后配置只此一条通道——装配写入什么工厂就看见什么）。
func TestFactoryParamsPassThrough(t *testing.T) {
	ResetFactories()
	t.Cleanup(ResetFactories)

	var got FactoryParams
	RegisterFactory(KindRegistry, "fake", func(_ context.Context, p FactoryParams) (Provider, error) {
		got = p
		return &fakeProvider{descriptor: ProviderDescriptor{Name: "fake", Capability: KindRegistry}}, nil
	})

	in := FactoryParams{
		Registry:    RegistryParams{Addr: "10.0.0.1:5000"},
		Logging:     LoggingParams{Addr: "10.0.0.1:9428", RetentionDays: 7},
		Metrics:     MetricsParams{Addr: "10.0.0.1:8428", RetentionDays: 14},
		ObjectStore: ObjectStoreParams{S3: &ObjectStoreS3Config{Endpoint: "e", Bucket: "b"}},
		Proxy:       ProxyParams{AuthToken: "t", ConfigEndpoint: "http://ce", AcmeEmail: "a@e", AcmeCAServer: "https://ca"},
		Runtime:     RuntimeParams{DockerHost: "unix:///run/d.sock", K3sKubeconfig: "/etc/rancher/k3s/k3s.yaml"},
		Platform:    PlatformParams{DataRoot: "/var/lib/fleetly"},
	}
	_, err := Build[*fakeProvider](context.Background(), KindRegistry, "fake", in)
	require.NoError(t, err)
	assert.Equal(t, in, got)
}
