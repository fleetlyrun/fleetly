package capability

// FacesOf 探测面测试：全提供/零提供/部分提供三形态 + Offered 稳定序。
//（8 处运行期断言点的收口锚——duck-typing 发现单点的行为冻结。）

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeFaceProvider 实现全部子面（方法体全空——探测只看类型，不看行为）。
type fakeFaceProvider struct{}

func (p *fakeFaceProvider) Describe() ProviderDescriptor        { return ProviderDescriptor{Name: "fake"} }
func (p *fakeFaceProvider) Health(context.Context) HealthReport { return HealthReport{} }

// 全子面方法集（nil 接收者安全空实现——fake 只挂类型）。
func (p *fakeFaceProvider) StreamLogs(context.Context, LogQuery, LogWriter) error { return nil }
func (p *fakeFaceProvider) Drain(context.Context, string) error                   { return nil }
func (p *fakeFaceProvider) Uncordon(context.Context, string) error                { return nil }
func (p *fakeFaceProvider) Cordon(context.Context, string) error                  { return nil }
func (p *fakeFaceProvider) InspectWorkloads(context.Context, NamespaceRef) ([]WorkloadObservation, error) {
	return nil, nil
}
func (p *fakeFaceProvider) SweepOrphanSecrets(context.Context, int) (int, error) { return 0, nil }
func (p *fakeFaceProvider) RunUtility(context.Context, UtilityRequest, io.Writer, io.Writer) error {
	return nil
}
func (p *fakeFaceProvider) InspectNetwork(context.Context, NamespaceRef, string) (NetworkCarrierState, error) {
	return NetworkCarrierState{}, nil
}
func (p *fakeFaceProvider) DetachNetwork(context.Context, NamespaceRef, string, string) error {
	return nil
}
func (p *fakeFaceProvider) RemoveNetwork(context.Context, NamespaceRef, string) error { return nil }
func (p *fakeFaceProvider) EnsureNetwork(context.Context, NamespaceRef, string) error { return nil }
func (p *fakeFaceProvider) AttachNetwork(context.Context, NamespaceRef, string, string) error {
	return nil
}
func (p *fakeFaceProvider) ManagedWorkloads() []Workload   { return nil }
func (p *fakeFaceProvider) ManagedNamespace() NamespaceRef { return NamespaceRef{} }
func (p *fakeFaceProvider) ManagedMaterials() Materials    { return Materials{} }
func (p *fakeFaceProvider) ConfigSnapshot() []byte         { return nil }
func (p *fakeFaceProvider) EndpointForProject(context.Context, string) (RegistryEndpoint, error) {
	return RegistryEndpoint{}, nil
}
func (p *fakeFaceProvider) ManagedMaterialsFor([]string) Materials { return Materials{} }

func TestFacesOfAllOffered(t *testing.T) {
	p := &fakeFaceProvider{}
	f := FacesOf(p)
	assert.NotNil(t, f.Logs)
	assert.NotNil(t, f.Admin)
	assert.NotNil(t, f.Inspector)
	assert.NotNil(t, f.Hygiene)
	assert.NotNil(t, f.Utility)
	assert.NotNil(t, f.NetworkMaintenance)
	assert.NotNil(t, f.Managed)
	assert.NotNil(t, f.MaterialsSource)
	assert.NotNil(t, f.ConfigSource)
	assert.NotNil(t, f.ProjectEndpoints)
	assert.NotNil(t, f.ProjectScopedMaterials)
	assert.Equal(t, []string{"logs", "admin", "inspector", "hygiene", "utility", "network-maintenance", "managed", "materials", "config",
		"project-endpoints", "project-materials"},
		f.Offered(), "enumeration order is frozen")
}

// bareFaceProvider 只实现 Provider 三件套（零子面）。
type bareFaceProvider struct{}

func (p *bareFaceProvider) Describe() ProviderDescriptor        { return ProviderDescriptor{Name: "bare"} }
func (p *bareFaceProvider) Health(context.Context) HealthReport { return HealthReport{} }

func TestFacesOfNoneOffered(t *testing.T) {
	f := FacesOf(&bareFaceProvider{})
	assert.Nil(t, f.Logs)
	assert.Nil(t, f.Admin)
	assert.Nil(t, f.Inspector)
	assert.Nil(t, f.Hygiene)
	assert.Nil(t, f.Managed)
	assert.Nil(t, f.MaterialsSource)
	assert.Nil(t, f.ConfigSource)
	assert.Nil(t, f.ProjectEndpoints)
	assert.Nil(t, f.ProjectScopedMaterials)
	assert.Empty(t, f.Offered())
}

// partialFaceProvider 实现两个不面目（logs + materials——跨面目混搭；
// project-endpoints 单挂——Registry 双子面彼此独立，ADR-0036 N2 兑现）。
type partialFaceProvider struct {
	bareFaceProvider
}

func (p *partialFaceProvider) StreamLogs(context.Context, LogQuery, LogWriter) error { return nil }
func (p *partialFaceProvider) ManagedMaterials() Materials                           { return Materials{} }
func (p *partialFaceProvider) EndpointForProject(context.Context, string) (RegistryEndpoint, error) {
	return RegistryEndpoint{}, nil
}

func TestFacesOfPartial(t *testing.T) {
	f := FacesOf(&partialFaceProvider{})
	assert.NotNil(t, f.Logs)
	assert.Nil(t, f.Admin)
	assert.NotNil(t, f.MaterialsSource)
	assert.Nil(t, f.Managed, "MaterialsSource does not imply Managed (独立子面)")
	assert.NotNil(t, f.ProjectEndpoints)
	assert.Nil(t, f.ProjectScopedMaterials, "ProjectEndpoints does not imply ProjectScopedMaterials (独立子面)")
	assert.Equal(t, []string{"logs", "materials", "project-endpoints"}, f.Offered())
}
