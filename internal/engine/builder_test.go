package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeBuilder 是构建端口假底座（可编程 digest/失败/日志帧）。
type fakeBuilder struct {
	digest string
	fail   bool
	logs   []string
	calls  []capability.BuildRequest
}

func (f *fakeBuilder) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindBuilder, Version: "test"}
}
func (f *fakeBuilder) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeBuilder) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	f.calls = append(f.calls, req)
	for _, line := range f.logs {
		if err := w.WriteLog(ctx, capability.LogFrame{WorkloadID: req.BuildID, Line: []byte(line)}); err != nil {
			return capability.BuildResult{}, err
		}
	}
	if f.fail {
		return capability.BuildResult{}, assert.AnError
	}
	return capability.BuildResult{Digest: f.digest}, nil
}

func gitBuildSpec() string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"git":{"repo":"file:///unused","ref":"main"}},` +
		`"processes":[{"name":"web","from_build":"default","replicas":1}],` +
		`"build":{"builder":"dockerfile","dockerfile":"Dockerfile"}}`
}

// newBuildEngine 建 git+build 场景引擎（fake builder + temp 数据根 +
// project/app 夹具行）。
func newBuildEngine(t *testing.T, fb *fakeBuilder) (*Engine, *fakeRuntime) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt, Builder: fb, Logger: discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop"}))
	require.NoError(t, app.New(clock).Create(ctx, db.Runner(), &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))
	return e, rt
}

// freezeGitBuildSpec 落 git+build Revision（预置上下文目录跳过 clone）并
// 返回 (revID, revSeq, contextDir)。
func freezeGitBuildSpec(t *testing.T, e *Engine, id string) (string, int64, string) {
	t.Helper()
	ctx := context.Background()
	seq, err := e.revisions.NextSeq(ctx, e.db.Runner(), tAppID)
	require.NoError(t, err)
	require.NoError(t, e.revisions.Create(ctx, e.db.Runner(), &revision.Revision{
		ID: id, AppID: tAppID, Seq: seq, Spec: []byte(gitBuildSpec()),
	}))
	dir := filepath.Join(e.opts.DataRoot, "contexts", id)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	return id, seq, dir
}

// 构建链：building → Build 受理 → 构建 succeeded → digest 回填投影 →
// releasing；构建日志进最近缓冲；事件三连。
func TestDeployBuildChain(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built", logs: []string{"FROM busybox"}}
	e, rt := newBuildEngine(t, fb)
	ctx := context.Background()

	revID, revSeq, contextDir := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B1")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // preparing → building → driveBuilding（受理 Build 行）
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateBuilding, d.State, "deployment error: %s", d.Error)

	// 构建循环拾取执行（goroutine 异步；轮询同时推进部署循环模拟 tick）。
	e.buildStep(ctx)
	waitFor(t, func() bool {
		e.step(ctx)
		b, ok := lastBuild(t, e, revID)
		return ok && b.State.Terminal()
	})
	b, _ := lastBuild(t, e, revID)
	require.Equal(t, build.StateSucceeded, b.State)
	assert.Equal(t, "sha256:built", b.Digest)
	require.Len(t, fb.calls, 1)
	assert.Equal(t, LocalImageRef(tAppID, revSeq), fb.calls[0].Target)
	assert.Equal(t, contextDir, fb.calls[0].ContextDir)

	// 日志进最近缓冲。
	recent := e.buildLogs.recent(b.ID)
	require.Len(t, recent, 1)
	assert.Equal(t, "FROM busybox", string(recent[0].Line))

	// 部署续走：building → releasing（from_build 解析为 digest）。
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	calls := rt.calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "sha256:built", calls[0].Spec["web"].Image)

	assert.Equal(t, []string{"build.queued", "build.building", "build.succeeded"}, eventNames(t, e, b.ID))
}

// 构建失败 → 部署失败（精确错误）。
func TestBuildFailureFailsDeployment(t *testing.T) {
	fb := &fakeBuilder{fail: true}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B2")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	e.buildStep(ctx)
	waitFor(t, func() bool {
		e.step(ctx)
		return getDeployment(t, e, d.ID).State == deployment.StateFailed
	})
	final := getDeployment(t, e, d.ID)
	assert.Equal(t, deployment.StateFailed, final.State)
	assert.Contains(t, final.Error, "build")
}

// 不可解析源（仓库不存在且目录未预置）→ 诚实失败、精确原因。
func TestUnresolvableSourceFailsPrecisely(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:x"}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	seq, err := e.revisions.NextSeq(ctx, e.db.Runner(), tAppID)
	require.NoError(t, err)
	revID := "01JD0REV0000000000000000B3"
	require.NoError(t, e.revisions.Create(ctx, e.db.Runner(), &revision.Revision{
		ID: revID, AppID: tAppID, Seq: seq, Spec: []byte(gitBuildSpec()),
	}))

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx)
	final := getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, final.State)
	assert.Contains(t, final.Error, "prepare build input")
}

// waitFor 轮询直到 cond 或 2s 超时（构建 goroutine 异步完成）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func lastBuild(t *testing.T, e *Engine, revID string) (*build.Build, bool) {
	t.Helper()
	list, err := e.builds.ListByRevision(context.Background(), e.db.Runner(), revID)
	if err != nil || len(list) == 0 {
		return nil, false
	}
	return &list[0], true
}
