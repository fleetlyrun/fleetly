package engine

// per-Project 凭证域隔离的 engine 面测试（ADR-0036 N2 兑现节 2）：
// 拉取/推送凭证按 Project 分发（面在场时）、reconciler 喂活跃 Project 集
// （集变即材料变即一次受管滚动）、面缺席回退平台凭证（升级零扰动序）。

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

// projectFakeRegistry 在 fakeRegistry 上加 per-Project 双子面（记录喂入集，
// 供 reconciler 断言；凭证值形态对齐 zot Provider：用户名 = projectID）。
type projectFakeRegistry struct {
	fakeRegistry

	setCalls int
	lastSet  []string
}

func (f *projectFakeRegistry) EndpointForProject(_ context.Context, projectID string) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{
		Addr: f.addr,
		Cred: capability.RegistryCredential{Server: f.addr, Username: projectID, Secret: "proj-secret"},
	}, nil
}

func (f *projectFakeRegistry) ManagedMaterialsFor(ids []string) capability.Materials {
	f.setCalls++
	f.lastSet = append([]string(nil), ids...)
	return capability.Materials{SecretFiles: map[string][]byte{
		"zot-config": []byte("for:" + strings.Join(ids, ",")),
	}}
}

var (
	_ capability.ProjectEndpoints       = (*projectFakeRegistry)(nil)
	_ capability.ProjectScopedMaterials = (*projectFakeRegistry)(nil)
)

// tProjectID2 是第二个测试 Project（活跃集多元素序断言用）。
const tProjectID2 = "01JD0PROJ00000000000000002"

// newProjectFakeRegistry 构造带 per-Project 双子面的受管仓库假底座（addr/
// 平台凭证继承 fakeRegistry 缺省锚）。
func newProjectFakeRegistry() *projectFakeRegistry {
	return &projectFakeRegistry{fakeRegistry: *newFakeRegistry()}
}

// 面在场：managed host 的拉取凭证按 Project 分发（用户名 = projectID），
// 平台凭证不再是 workload 拉取面。
func TestManagedHostCredentialPerProject(t *testing.T) {
	db, clock := statertest.New(t)
	reg := newProjectFakeRegistry()
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))

	m, err := e.materialsFor(ctx, nil, []string{fakeRegistryAddr}, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, tProjectID, m.RegistryAuth[fakeRegistryAddr].Username,
		"workload pull credential must be the project credential when the face is present")
	assert.Equal(t, "proj-secret", m.RegistryAuth[fakeRegistryAddr].Secret)
}

// 面缺席（升级零扰动序）：managed host 回退平台凭证——fakeRegistry 不实现
// per-Project 面，既有 TestManagedHostCredentialInjection 已钉平台面；此处
// 钉 projectPushCred 的 nil 回退与面在场的前纲推送凭证。
func TestProjectPushCredRidesBuildInput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reg        capability.Registry
		wantUser   string
		wantSecret string
	}{
		{name: "face present", reg: newProjectFakeRegistry(), wantUser: tProjectID, wantSecret: "proj-secret"},
		{name: "face absent", reg: newFakeRegistry(), wantUser: "fleetly", wantSecret: "test-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBuilder{digest: "sha256:built"}
			e, _ := newBuildEngine(t, fb)
			e.registry = tc.reg // 装配后覆盖（firstboot_test 同款接缝）
			ctx := context.Background()

			revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000P1")
			_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
			require.NoError(t, err)
			e.step(ctx)
			e.buildStep(ctx)
			waitFor(t, func() bool {
				e.step(ctx)
				b, ok := lastBuild(t, e, revID)
				return ok && b.State.Terminal()
			})
			b, _ := lastBuild(t, e, revID)
			require.Equal(t, build.StateSucceeded, b.State, "build error: %s", b.Error)
			assert.Equal(t, buildRepo(tProjectID, tAppID), b.Repo, "build row stamps the prefixed repo at creation")

			calls := fb.snapshot()
			require.Len(t, calls, 1)
			assert.Equal(t, buildRepo(tProjectID, tAppID)+":r1", strings.TrimPrefix(calls[0].Target, fakeRegistryAddr+"/"),
				"push target must carry the project prefix (both segments lowercased)")
			require.NotNil(t, calls[0].PushCred)
			assert.Equal(t, tc.wantUser, calls[0].PushCred.Username)
			assert.Equal(t, tc.wantSecret, calls[0].PushCred.Secret)
		})
	}
}

// reconciler 喂活跃集：Ensure 材料随活跃 Project 集再生成（排序稳定）；
// 集不变签名短路（不重复 Ensure）；Project 创建即一次受管滚动（材料变更
// → 新 Ensure）。
func TestReconcileManagedFeedsActiveProjects(t *testing.T) {
	db, clock := statertest.New(t)
	rt := newFakeRuntime()
	reg := newProjectFakeRegistry()
	e := New(Deps{DB: db, Runtime: rt, Edge: &fakeEdge{}, Registry: reg, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID2, Name: "b", TeamID: "default"}))
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "a", TeamID: "default"}))

	e.managedStep(ctx)
	assert.Equal(t, []string{tProjectID, tProjectID2}, reg.lastSet, "the active set is fed sorted")
	firstEnsure := len(rt.calls())
	require.NotZero(t, firstEnsure)

	// 集不变：签名短路（无新增 Ensure）。
	e.managedStep(ctx)
	assert.Equal(t, firstEnsure, len(rt.calls()), "unchanged set must not re-ensure")

	// Project 创建 = 集变 = 一次受管滚动。
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(),
		&project.Project{ID: "01JD0PROJ00000000000000003", Name: "c", TeamID: "default"}))
	e.managedStep(ctx)
	assert.Greater(t, len(rt.calls()), firstEnsure, "a new project must roll the managed registry once")
	assert.Equal(t, []string{tProjectID, tProjectID2, "01JD0PROJ00000000000000003"}, reg.lastSet)

	// Ensure 收到的材料就是按集铸造的产物。
	regNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
	var regCall *ensureCall
	for i := range rt.calls() {
		if rt.calls()[i].NS.String() == regNS.String() {
			regCall = &rt.calls()[i]
		}
	}
	require.NotNil(t, regCall)
	assert.Equal(t, []byte("for:"+strings.Join([]string{tProjectID, tProjectID2, "01JD0PROJ00000000000000003"}, ",")),
		regCall.Materials.SecretFiles["zot-config"], "project-scoped materials must ride the managed Ensure")
}

// 集序契约（activeProjectIDs）：多 Project 排序稳定（材料字节稳定的输入面）。
func TestActiveProjectIDsSorted(t *testing.T) {
	db, clock := statertest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Registry: newFakeRegistry(), Logger: discardLogger()}, Options{})
	ctx := context.Background()
	for _, id := range []string{tProjectID2, "01JD0PROJ00000000000000003", tProjectID} {
		require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: id, Name: "p-" + id, TeamID: "default"}))
	}
	ids, err := e.activeProjectIDs(ctx)
	require.NoError(t, err)
	assert.True(t, sort.StringsAreSorted(ids), "project ids must come out sorted")
	assert.Len(t, ids, 3)
}
