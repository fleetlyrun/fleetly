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

	RegisterFactory(KindRuntime, "fake", func(ctx context.Context) (Provider, error) {
		return &fakeProvider{descriptor: ProviderDescriptor{Name: "fake", Capability: KindRuntime}}, nil
	})

	// 空名直取唯一在册者。
	p, err := Build(context.Background(), KindRuntime, "")
	require.NoError(t, err)
	assert.Equal(t, "fake", p.Describe().Name)

	// 指名构造。
	p, err = Build(context.Background(), KindRuntime, "fake")
	require.NoError(t, err)
	assert.Equal(t, KindRuntime, p.Describe().Capability)

	// 未知名报错并列出候选。
	_, err = Build(context.Background(), KindRuntime, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake")

	// 重复注册 panic（装配错误启动期暴露）。
	assert.Panics(t, func() {
		RegisterFactory(KindRuntime, "fake", func(context.Context) (Provider, error) { return nil, nil })
	})
}
