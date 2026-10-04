package engine

// Builder 家族路由测试（F1.14，ADR-0032）：三 strategy 的 BuildRequest
// 分派（路由名 + strategy 载荷）、railpack 全链路由（executeBuild 拾取
// 正确 Provider）、未知 builder 精确终态失败、存量无 strategy 行防御性
// dockerfile 路由。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// newRoutingEngine 建 dockerfile/railpack/static 三名全在册的引擎（各自
// 独立 fake，捕获分派结果）。
func newRoutingEngine(t *testing.T) (*Engine, *fakeBuilder, *fakeBuilder, *fakeBuilder) {
	t.Helper()
	fbD, fbR, fbS := &fakeBuilder{digest: "sha256:routed-d"}, &fakeBuilder{digest: "sha256:routed-r"}, &fakeBuilder{digest: "sha256:routed-s"}
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(),
		Builders: map[string]capability.Builder{
			specir.BuilderDockerfile: fbD,
			specir.BuilderRailpack:   fbR,
			specir.BuilderStatic:     fbS,
		},
		Registry: newFakeRegistry(), Logger: discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, app.New(clock).Create(ctx, db.Runner(), &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))
	return e, fbD, fbR, fbS
}

// strategySpecJSON 合成三 strategy 的 git 源 spec（git 源 + 预置上下文目录
// 跳过 clone；与 gitBuildSpec 同构，仅 build 段不同）。
func strategySpecJSON(buildJSON string) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"git":{"repo":"file:///unused","ref":"main"}},` +
		`"build":` + buildJSON + `,` +
		`"processes":[{"name":"web","from_build":"web","replicas":1}]}`
}

// freezeStrategySpec 落 Revision + 预置上下文目录，返回 (revID, revSeq)。
func freezeStrategySpec(t *testing.T, e *Engine, id, buildJSON string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	seq, err := e.revisions.NextSeq(ctx, e.db.Runner(), tAppID)
	require.NoError(t, err)
	require.NoError(t, e.revisions.Create(ctx, e.db.Runner(), &revision.Revision{
		ID: id, AppID: tAppID, Seq: seq, Spec: []byte(strategySpecJSON(buildJSON)),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(e.opts.DataRoot, "contexts", id), 0o750))
	return id, seq
}

// TestPrepareBuildInputDispatchesStrategy：三 strategy 各自携带路由名与
// strategy 载荷进 BuildRequest；目标/凭证面三 strategy 同构（共享链不变）。
func TestPrepareBuildInputDispatchesStrategy(t *testing.T) {
	e, _, _, _ := newRoutingEngine(t)
	ctx := context.Background()
	cases := []struct {
		name      string
		revID     string
		buildJSON string
		verify    func(t *testing.T, in capability.BuildRequest)
	}{
		{
			name:      "dockerfile",
			revID:     "01JD0REV0000000000000000D1",
			buildJSON: `{"builder":"dockerfile","dockerfile":"Dockerfile"}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderDockerfile, in.Builder)
				assert.Equal(t, "Dockerfile", in.Dockerfile)
				assert.Nil(t, in.Railpack)
				assert.Nil(t, in.Static)
			},
		},
		{
			name:      "railpack",
			revID:     "01JD0REV0000000000000000D2",
			buildJSON: `{"builder":"railpack","railpack":{"pinned_version":"0.39.0"}}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderRailpack, in.Builder)
				require.NotNil(t, in.Railpack)
				assert.Equal(t, "0.39.0", in.Railpack.PinnedVersion)
				assert.Empty(t, in.Dockerfile)
			},
		},
		{
			name:      "static",
			revID:     "01JD0REV0000000000000000D3",
			buildJSON: `{"builder":"static","static":{"output_dir":"dist"}}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderStatic, in.Builder)
				require.NotNil(t, in.Static)
				assert.Equal(t, "dist", in.Static.OutputDir)
				assert.Empty(t, in.Dockerfile)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			revID, revSeq := freezeStrategySpec(t, e, tc.revID, tc.buildJSON)
			spec, err := e.loadSpec(ctx, revID)
			require.NoError(t, err)
			input, err := e.prepareBuildInput(ctx, &deployment.Deployment{AppID: tAppID, ToRevision: revID}, spec, revSeq, tProjectID)
			require.NoError(t, err)
			tc.verify(t, input)
			assert.Equal(t, LocalImageRef(fakeRegistryAddr, tProjectID, tAppID, revSeq), input.Target)
			require.NotNil(t, input.PushCred, "the push target and credential are strategy-invariant")
		})
	}
}

// TestRailpackFullChainRoutesToRailpackProvider：railpack spec 全链
// （Submit → building → 构建循环）只命中 railpack 名在册的 Provider——
// 路由是 executeBuild 的分派事实，不只是 input 组装事实。
func TestRailpackFullChainRoutesToRailpackProvider(t *testing.T) {
	e, fbD, fbR, fbS := newRoutingEngine(t)
	ctx := context.Background()
	revID, _ := freezeStrategySpec(t, e, "01JD0REV0000000000000000D4",
		`{"builder":"railpack","railpack":{"pinned_version":"0.39.0"}}`)

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	got := getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateBuilding, got.State, "deployment error: %s", got.Error)
	e.buildStep(ctx)
	waitFor(t, func() bool {
		e.step(ctx)
		b, ok := lastBuild(t, e, revID)
		return ok && b.State.Terminal()
	})
	b, _ := lastBuild(t, e, revID)
	require.Equal(t, build.StateSucceeded, b.State, "build error: %s", b.Error)
	assert.Equal(t, "sha256:routed-r", b.Digest)

	calls := fbR.snapshot()
	require.Len(t, calls, 1, "the railpack provider receives exactly one call")
	assert.Equal(t, specir.BuilderRailpack, calls[0].Builder)
	assert.Empty(t, fbD.snapshot(), "the dockerfile provider must not be engaged")
	assert.Empty(t, fbS.snapshot(), "the static provider must not be engaged")
}

// TestPrepareBuildInputLegacySpecRoutesDockerfile：存量行 strategy 缺席
// （F1.14 前的冻结体）防御性走 dockerfile 轨。
func TestPrepareBuildInputLegacySpecRoutesDockerfile(t *testing.T) {
	e, _, _, _ := newRoutingEngine(t)
	ctx := context.Background()
	revID, revSeq := freezeStrategySpec(t, e, "01JD0REV0000000000000000E1",
		`{"builder":"dockerfile","dockerfile":"Dockerfile"}`)
	spec, err := e.loadSpec(ctx, revID)
	require.NoError(t, err)
	spec.Build.Strategy = nil // 剥掉 strategy：存量防御形态（oneof 字段一并清空）
	input, err := e.prepareBuildInput(ctx, &deployment.Deployment{AppID: tAppID, ToRevision: revID}, spec, revSeq, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, specir.BuilderDockerfile, input.Builder)
	assert.Empty(t, input.Dockerfile, "empty dockerfile rides the port default (\"Dockerfile\"), same semantics")
}

// TestExecuteBuildUnwiredBuilderFailsPrecisely：input 路由名不在册（漂移
// 名或未装配 Provider）→ 终态 failed 精确文本（不 panic、不回 queued 弹跳）。
func TestExecuteBuildUnwiredBuilderFailsPrecisely(t *testing.T) {
	e, fbD, _, _ := newRoutingEngine(t)
	ctx := context.Background()
	b := &build.Build{ID: "01JD0B00000000000000000UN", AppID: tAppID,
		RevisionID: "01JD0REV0000000000000000F1", State: build.StateBuilding}
	require.NoError(t, e.builds.Create(ctx, e.db.Runner(), b))
	e.build.inputs[b.ID] = capability.BuildRequest{BuildID: b.ID, Builder: "nope", ContextDir: "."}
	e.executeBuild(b)
	got, err := e.builds.Get(ctx, e.db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateFailed, got.State)
	assert.Contains(t, got.Error, `builder "nope" is not wired`)
	assert.Contains(t, got.Error, specir.BuilderDockerfile, "the error lists the wired builders")
	assert.Empty(t, fbD.snapshot(), "the wired builders must not be engaged")
}
