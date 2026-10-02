package engine

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeRegistryAddr 是 engine 测试的受管仓库地址锚（引用形态断言共用）。
const fakeRegistryAddr = "reg.test:5000"

// fakeRegistry 是 Registry 端口假底座（地址/凭证固定；managed 开启时同时
// 实现 Managed+MaterialsSource——reconcileManaged 双 Provider 与材料透传
// 的测试面，形态对齐 zot Provider）。
type fakeRegistry struct {
	addr      string
	cred      capability.RegistryCredential
	managed   bool
	materials capability.Materials
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		addr: fakeRegistryAddr,
		cred: capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
	}
}

func (f *fakeRegistry) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-registry", Capability: capability.KindRegistry, Version: "test", Managed: f.managed}
}

func (f *fakeRegistry) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeRegistry) Endpoint(context.Context) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{Addr: f.addr, Cred: f.cred}, nil
}

// ManagedNamespace 实现 Managed 子面（fleetly/system/registry——与 zot 同域）。
func (f *fakeRegistry) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
}

// ManagedWorkloads 实现 Managed 子面（单 workload 声明）。
func (f *fakeRegistry) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{{
		ID: "fleetly-registry-fake", Process: "zot", Image: "reg.test/zot:vtest", Replicas: 1,
	}}
}

// ManagedMaterials 实现 MaterialsSource 子面。
func (f *fakeRegistry) ManagedMaterials() capability.Materials { return f.materials }

var (
	_ capability.Registry        = (*fakeRegistry)(nil)
	_ capability.Managed         = (*fakeRegistry)(nil)
	_ capability.MaterialsSource = (*fakeRegistry)(nil)
)
