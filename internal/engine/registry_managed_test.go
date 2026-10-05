package engine

// 受管 zot 面测试（F1.11，ADR-0019 附录 B）：无 Registry 的诚实拒绝、
// managed host 平台凭证注入（项目 Secret 不参与）、reconcileManaged 双
// Provider（zot 域无项目网 + 材料透传）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// 附录 B.5①：无 Registry Provider 时 build 源部署在 prepare 精确失败
// （不做本机导入退化形态）。
func TestBuildSourceWithoutRegistryFailsAtPrepare(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built"}
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt,
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Logger:   discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, app.New(clock).Create(ctx, db.Runner(), &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))

	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000C1")
	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)

	e.step(ctx) // preparing → 前置门精确失败（不进 building）
	d = getDeployment(t, e, d.ID)
	assert.Equal(t, deployment.StateFailed, d.State)
	assert.Contains(t, d.Error, "no registry provider wired")
	assert.Empty(t, fb.snapshot(), "the builder must never be engaged without a push target")
}

// 附录 B.3：managed host 平台凭证直注；同 host 的项目级 Secret
// registry:<host> 不参与（平台凭证是唯一真源）；非 managed host 走 Secret
// 通道不变。
func TestManagedHostCredentialInjection(t *testing.T) {
	db, clock := statetest.New(t)
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	reg := newFakeRegistry()
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Cipher: cipher, Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))

	// 项目级同名 registry Secret（应被平台凭证覆盖，不被读取）。
	userCred, _ := json.Marshal(map[string]string{"username": "intruder", "secret": "wrong", "server": fakeRegistryAddr}) //nolint:gosec // 测试夹具样本值
	putSecret(t, e, tProjectID, "registry:"+fakeRegistryAddr, userCred)
	// 非 managed host 的既有通道照旧。
	ghcrCred, _ := json.Marshal(registryCredentialJSON{Server: "ghcr.io", Username: "ci", Secret: "pat-x"}) //nolint:gosec // 测试夹具样本值
	putSecret(t, e, tProjectID, "registry:ghcr.io", ghcrCred)

	hosts := []string{fakeRegistryAddr, "ghcr.io"}
	m, err := e.materialsFor(ctx, nil, hosts, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
		m.RegistryAuth[fakeRegistryAddr], "the platform credential must win on the managed host")
	assert.Equal(t, "ci", m.RegistryAuth["ghcr.io"].Username, "non-managed hosts keep the project secret channel")
}

// 附录 B.1：reconcileManaged 双 Provider——Proxy 挂活跃项目网、zot 域不挂；
// MaterialsSource 材料透传到受管 Ensure。
func TestReconcileManagedDualProviders(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	proxy := &fakeProxy{}
	reg := newFakeRegistry()
	reg.managed = true
	reg.materials = capability.Materials{SecretFiles: map[string][]byte{"zot-config": []byte(`{"http":{}}`)}}
	e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, e.networks.Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET00000000000000000A", ProjectID: tProjectID, Name: "default",
	}))

	e.managedStep(ctx)

	proxyNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "proxy"}
	regNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
	var proxyCall, regCall *ensureCall
	for i := range rt.calls() {
		c := rt.calls()[i]
		switch c.NS.String() {
		case proxyNS.String():
			proxyCall = &rt.calls()[i]
		case regNS.String():
			regCall = &rt.calls()[i]
		}
	}
	require.NotNil(t, proxyCall, "proxy domain must be ensured")
	require.NotNil(t, regCall, "registry domain must be ensured")

	require.Len(t, proxyCall.Spec["proxy"].NetworkRefs, 1, "proxy must attach the active project network")
	assert.Empty(t, regCall.Spec["zot"].NetworkRefs, "registry must not attach project networks (B.1)")
	assert.Empty(t, regCall.Spec["zot"].Networks)

	assert.Empty(t, proxyCall.Materials.SecretFiles, "proxy declares no materials")
	assert.Equal(t, []byte(`{"http":{}}`), regCall.Materials.SecretFiles["zot-config"],
		"MaterialsSource payload must ride the managed Ensure")
	assert.Equal(t, proxyCall.Gen, regCall.Gen, "managed generation is shared across providers")
}
