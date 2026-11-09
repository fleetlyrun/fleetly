package intake

// materialsFor 面白盒（P5 wave-5 拆包随域迁入，通则⑥——断言面零漂移；
// 原 engine registry_managed_test/registry_project_test 的材料解析域行为面，
// managed host 凭证注入经端口闭包驱动）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
)

// fakeRegistryAddr 是受管仓库地址锚（engine 侧同源隔离副本）。
const fakeRegistryAddr = "reg.test:5000"

// fakeRegistry 是 Registry 端口假底座（engine 侧同源隔离副本；材料面测试
// 只消费非 nil 判定与 Describe/Health/Endpoint 面）。
type fakeRegistry struct {
	addr string
	cred capability.RegistryCredential
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		addr: fakeRegistryAddr,
		cred: capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
	}
}

func (f *fakeRegistry) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-registry", Capability: capability.KindRegistry, Version: "test"}
}
func (f *fakeRegistry) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeRegistry) Endpoint(context.Context) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{Addr: f.addr, Cred: f.cred}, nil
}

var _ capability.Registry = (*fakeRegistry)(nil)

// projectFakeRegistry 在 fakeRegistry 上加 per-Project 扩展接口（engine 侧
// projectFakeRegistry 的隔离副本；凭证值形态对齐 zot Provider：用户名 =
// projectID）。
type projectFakeRegistry struct {
	fakeRegistry
}

func (f *projectFakeRegistry) EndpointForProject(_ context.Context, projectID string) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{
		Addr: f.addr,
		Cred: capability.RegistryCredential{Server: f.addr, Username: projectID, Secret: "proj-secret"},
	}, nil
}

var _ capability.ProjectEndpoints = (*projectFakeRegistry)(nil)

// newProjectFakeRegistry 构造带 per-Project 扩展接口的受管仓库假底座。
func newProjectFakeRegistry() *projectFakeRegistry {
	return &projectFakeRegistry{fakeRegistry: *newFakeRegistry()}
}

// newMaterialsFixture 建材料解析夹具（registry 面 + managedEndpoint 端口
// 绑假端点；projectEP 可注入 per-Project 面）。
func newMaterialsFixture(t *testing.T, reg capability.Registry, projectEP capability.ProjectEndpoints) *fixture {
	t.Helper()
	cipher, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	fx := newFixture(t, func(d *Deps) {
		d.Cipher = cipher
		d.Registry = reg
		d.ManagedEndpoint = func(context.Context) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{Addr: fakeRegistryAddr, Cred: capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"}}, nil
		}
		d.RegistryProjectEndpoints = func() capability.ProjectEndpoints { return projectEP }
	})
	return fx
}

// 附录 B.3：managed host 平台凭证直注；同 host 的项目级 Secret
// registry:<host> 不参与（平台凭证是唯一真源）；非 managed host 走 Secret
// 通道不变。
func TestManagedHostCredentialInjection(t *testing.T) {
	fx := newMaterialsFixture(t, newFakeRegistry(), nil)
	ctx := context.Background()

	// 项目级同名 registry Secret（应被平台凭证覆盖，不被读取）。
	userCred, _ := json.Marshal(map[string]string{"username": "intruder", "secret": "wrong", "server": fakeRegistryAddr}) //nolint:gosec // 测试夹具样本值
	fx.putSecret(t, tProjectID, "registry:"+fakeRegistryAddr, userCred)
	// 非 managed host 的既有通道照旧。
	ghcrCred, _ := json.Marshal(registryCredentialJSON{Server: "ghcr.io", Username: "ci", Secret: "pat-x"}) //nolint:gosec // 测试夹具样本值
	fx.putSecret(t, tProjectID, "registry:ghcr.io", ghcrCred)

	hosts := []string{fakeRegistryAddr, "ghcr.io"}
	m, err := fx.in.materialsFor(ctx, nil, hosts, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
		m.RegistryAuth[fakeRegistryAddr], "the platform credential must win on the managed host")
	assert.Equal(t, "ci", m.RegistryAuth["ghcr.io"].Username, "non-managed hosts keep the project secret channel")
}

// 面在场：managed host 的拉取凭证按 Project 分发（用户名 = projectID），
// 平台凭证不再是 workload 拉取面。
func TestManagedHostCredentialPerProject(t *testing.T) {
	fx := newMaterialsFixture(t, newProjectFakeRegistry(), newProjectFakeRegistry())
	ctx := context.Background()

	m, err := fx.in.materialsFor(ctx, nil, []string{fakeRegistryAddr}, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, tProjectID, m.RegistryAuth[fakeRegistryAddr].Username,
		"workload pull credential must be the project credential when the face is present")
	assert.Equal(t, "proj-secret", m.RegistryAuth[fakeRegistryAddr].Secret)
}

// 前纲推送凭证断言锚（BuildRepo 单源）：双段小写。
func TestBuildRepoPrefix(t *testing.T) {
	assert.Equal(t, strings.ToLower(tProjectID)+"/"+strings.ToLower(tAppID), BuildRepo(tProjectID, tAppID))
}
