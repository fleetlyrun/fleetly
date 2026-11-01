package engine

// 受管 zot 面测试（F1.11，ADR-0019 附录 B）：无 Registry 的诚实拒绝、
// managed host 平台凭证注入（项目 Secret 不参与）。reconcileManaged 双
// Provider 面（zot 域无项目网 + 材料透传）已随 P5 wave-4 拆包迁入
// manageds 卫星包白盒（通则⑥——断言面零漂移）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 附录 B.5①：无 Registry Provider 时 build 源部署在 prepare 精确失败
// （不做本机导入退化形态）。
func TestBuildSourceWithoutRegistryFailsAtPrepare(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built"}
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt,
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Logger:   discardLogger()}, Options{Data: DataOptions{DataRoot: t.TempDir()}})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, apprepo.New(clock, db.Runner()).Create(ctx, &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))

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
	cipher, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	reg := newFakeRegistry()
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Cipher: cipher, Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))

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
