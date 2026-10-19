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
func (p *fakeFaceProvider) SweepOrphanVolumes(context.Context, int) (int, error) { return 0, nil }

func (p *fakeFaceProvider) SweepOrphanPeerGrants(context.Context, int) (int, error) { return 0, nil }
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
func (p *fakeFaceProvider) Restart(context.Context, NamespaceRef, string) error { return nil }
func (p *fakeFaceProvider) ExecInstances(context.Context, string) ([]ExecTargetInstance, error) {
	return nil, nil
}
func (p *fakeFaceProvider) ExecWorkload(context.Context, ExecWorkloadRequest) (int, error) {
	return 0, nil
}
func (p *fakeFaceProvider) RunNodeRelay(context.Context, NodeRelayOptions) error { return nil }
func (p *fakeFaceProvider) NodeContainers(context.Context, string) ([]NodeContainerObservation, error) {
	return nil, nil
}
func (p *fakeFaceProvider) Catalog(context.Context, RegistryEndpoint) ([]string, error) {
	return nil, nil
}
func (p *fakeFaceProvider) Tags(context.Context, RegistryEndpoint, string) ([]RegistryTag, error) {
	return nil, nil
}
func (p *fakeFaceProvider) ManagedWorkloads() []Workload   { return nil }
func (p *fakeFaceProvider) ManagedNamespace() NamespaceRef { return NamespaceRef{} }
func (p *fakeFaceProvider) ManagedMaterials() Materials    { return Materials{} }
func (p *fakeFaceProvider) ConfigSnapshot() []byte         { return nil }
func (p *fakeFaceProvider) EndpointForProject(context.Context, string) (RegistryEndpoint, error) {
	return RegistryEndpoint{}, nil
}
func (p *fakeFaceProvider) ManagedMaterialsFor([]string) Materials { return Materials{} }
func (p *fakeFaceProvider) NodeRelayCommand(context.Context, string, EnrollmentOptions) (string, error) {
	return "", nil
}

// TestFacesOfAllOffered：ProviderFaces 的**全字段**断言（R01-L1/R06-L1）。
// 历史漂移：报告基线曾 14 面而测试只断言 11 面；当前基线 15 个声明面 +
// R06-L1 补入 Restart = 16。本测试逐字段 + Offered 冻结序 + Len 三重钉死，
// 新增字段不改测试即红（不会再出现"断言清单落后于结构体"）。
func TestFacesOfAllOffered(t *testing.T) {
	p := &fakeFaceProvider{}
	f := FacesOf(p)
	assert.NotNil(t, f.Logs)
	assert.NotNil(t, f.Admin)
	assert.NotNil(t, f.Inspector)
	assert.NotNil(t, f.Hygiene)
	assert.NotNil(t, f.Utility)
	assert.NotNil(t, f.NetworkMaintenance)
	assert.NotNil(t, f.Exec, "Exec face must be enumerated (F3.2/ADR-0049)")
	assert.NotNil(t, f.Restart, "Restart face must be enumerated (ADR-0069; R06-L1)")
	assert.NotNil(t, f.RelayMaterial)
	assert.NotNil(t, f.NodeInspect, "NodeInspect face must be enumerated (H10)")
	assert.NotNil(t, f.Managed)
	assert.NotNil(t, f.MaterialsSource)
	assert.NotNil(t, f.ConfigSource)
	assert.NotNil(t, f.ProjectEndpoints)
	assert.NotNil(t, f.ProjectScopedMaterials)
	assert.NotNil(t, f.Content, "Content face must be enumerated (IA v3)")
	assert.Equal(t, []string{"logs", "admin", "inspector", "hygiene", "utility", "network-maintenance", "exec", "restart", "relay_material",
		"node-inspect", "managed", "materials", "config", "project-endpoints", "project-materials", "content"},
		f.Offered(), "enumeration order is frozen")
	assert.Len(t, f.Offered(), 16, "every declared ProviderFaces field must be enumerated exactly once (R01-L1/R06-L1)")
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
	assert.Nil(t, f.Utility)
	assert.Nil(t, f.NetworkMaintenance)
	assert.Nil(t, f.Exec)
	assert.Nil(t, f.Restart)
	assert.Nil(t, f.RelayMaterial)
	assert.Nil(t, f.NodeInspect)
	assert.Nil(t, f.Managed)
	assert.Nil(t, f.MaterialsSource)
	assert.Nil(t, f.ConfigSource)
	assert.Nil(t, f.ProjectEndpoints)
	assert.Nil(t, f.ProjectScopedMaterials)
	assert.Nil(t, f.Content)
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
	assert.Nil(t, f.Exec)
	assert.Nil(t, f.Restart)
	assert.Nil(t, f.NodeInspect)
	assert.Nil(t, f.Content)
	assert.Equal(t, []string{"logs", "materials", "project-endpoints"}, f.Offered())
}
