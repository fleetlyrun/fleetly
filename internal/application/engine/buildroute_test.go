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

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// newRoutingEngine 建 dockerfile/railpack/static 三名全在册的引擎（各自
// 独立 fake，捕获分派结果）。
func newRoutingEngine(t *testing.T) (*Engine, *fakeBuilder, *fakeBuilder, *fakeBuilder) {
	t.Helper()
	fbD, fbR, fbS := &fakeBuilder{digest: "sha256:routed-d"}, &fakeBuilder{digest: "sha256:routed-r"}, &fakeBuilder{digest: "sha256:routed-s"}
	db, clock := statetest.New(t)
	e := newEngineForTest(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(),
		Builders: map[string]capability.Builder{
			specir.BuilderDockerfile: fbD,
			specir.BuilderRailpack:   fbR,
			specir.BuilderStatic:     fbS,
		},
		Registry: newFakeRegistry(), Logger: discardLogger()}, Options{Data: DataOptions{DataRoot: t.TempDir()}})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, apprepo.New(clock, db.Runner()).Create(ctx, &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))
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
	seq, err := e.repos.Revisions.NextSeq(ctx, tAppID)
	require.NoError(t, err)
	require.NoError(t, e.repos.Revisions.Create(ctx, &revision.Revision{
		ID: id, AppID: tAppID, Seq: seq, Spec: []byte(strategySpecJSON(buildJSON)),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(e.opts.Data.DataRoot, "contexts", id), 0o750))
	return id, seq
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
	contract.StepOf(e.intake, "build")(ctx)
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
