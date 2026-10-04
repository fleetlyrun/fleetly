package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeBuilder 是构建端口假底座（可编程 digest/失败/日志帧；block 非空时
// 构建阻塞直至放行或 ctx 取消——在途/停机形态注入，entered 同步入口信号）。
type fakeBuilder struct {
	mu     sync.Mutex
	digest string
	fail   bool
	logs   []string
	calls  []capability.BuildRequest

	block   chan struct{}
	entered chan struct{}
}

func (f *fakeBuilder) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindBuilder, Version: "test"}
}
func (f *fakeBuilder) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeBuilder) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return capability.BuildResult{}, ctx.Err()
		}
	}
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

// snapshot 返回 Build 调用快照（并发安全读面）。
func (f *fakeBuilder) snapshot() []capability.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.BuildRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// unblock 放行阻塞中的构建（停机排空后的夹具清理/复用）。
func (f *fakeBuilder) unblock() {
	if f.block != nil {
		close(f.block)
	}
}

// newBlockingBuilder 建可挂起的构建假底座（entered 收到信号 = 构建已进入
// 并停在 block；crash-recovery/停机夹具）。
func newBlockingBuilder(digest string) *fakeBuilder {
	return &fakeBuilder{digest: digest, block: make(chan struct{}), entered: make(chan struct{}, 1)}
}

func gitBuildSpec() string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"git":{"repo":"file:///unused","ref":"main"}},` +
		`"processes":[{"name":"web","from_build":"default","replicas":1}],` +
		`"build":{"builder":"dockerfile","dockerfile":"Dockerfile"}}`
}

// newBuildEngine 建 git+build 场景引擎（fake builder + fake registry +
// temp 数据根 + project/app 夹具行）。
func newBuildEngine(t *testing.T, fb *fakeBuilder) (*Engine, *fakeRuntime) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt,
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Registry: newFakeRegistry(),
		Logger:   discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
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

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
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
	bcalls := fb.snapshot()
	require.Len(t, bcalls, 1)
	assert.Equal(t, LocalImageRef(fakeRegistryAddr, tAppID, revSeq), bcalls[0].Target)
	assert.Equal(t, contextDir, bcalls[0].ContextDir)
	require.NotNil(t, bcalls[0].PushCred, "managed registry credential must ride the build request (B.3 face 2)")
	assert.Equal(t, fakeRegistryAddr, bcalls[0].PushCred.Server)

	// 日志进最近缓冲。
	recent := e.build.logs.recent(b.ID)
	require.Len(t, recent, 1)
	assert.Equal(t, "FROM busybox", string(recent[0].Line))

	// 部署续走：building → releasing（from_build 解析为受管仓库 digest 引用）。
	e.step(ctx)
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	calls := rt.calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, LocalImageDigestRef(fakeRegistryAddr, tAppID, "sha256:built"), calls[0].Spec["web"].Image)
	// 拉取凭证面（B.3 分发面③）：managed host 平台凭证注入。
	assert.Equal(t, capability.RegistryCredential{Server: fakeRegistryAddr, Username: "fleetly", Secret: "test-secret"},
		calls[0].Materials.RegistryAuth[fakeRegistryAddr])

	assert.Equal(t, []string{"build.queued", "build.building", "build.succeeded"}, eventNames(t, e, b.ID))
}

// 构建失败 → 部署失败（精确错误）。
func TestBuildFailureFailsDeployment(t *testing.T) {
	fb := &fakeBuilder{fail: true}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B2")

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
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

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
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

// Q-9：构建命名锚 revSeq 的 Revision 读取失败 → 部署硬失败带精确错误
// （不再静默 revSeq=0 撞 r0 tag）。building 驱动步内抽走 revisions 表
// （hermetic 库；让读路径失败的唯一确定缝）。
func TestBuildRevisionReadFailureFailsDeployment(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:x"}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B7")

	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // → building（prepare 已过；revSeq 读取在 building 驱动步）
	require.Equal(t, deployment.StateBuilding, getDeployment(t, e, d.ID).State)

	_, err = e.db.Runner().ExecContext(ctx, `DROP TABLE revisions`)
	require.NoError(t, err)
	e.step(ctx)

	final := getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateFailed, final.State)
	assert.Contains(t, final.Error, "resolve revision for build")
}

// D-4 防御纵深：executeBuild 无输入登记（正常路径被拾取前置检拦截，此
// 分支不可达；到达即输入丢失）→ 一跳终态 cancelled，不回 queued 弹跳。
func TestExecuteBuildWithoutInputGoesTerminal(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:x"}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B8")

	b := &build.Build{
		ID: "01JD0BUILD000000000000000B8", AppID: tAppID,
		RevisionID: revID, State: build.StateBuilding,
	}
	require.NoError(t, e.builds.Create(ctx, e.db.Runner(), b))

	e.executeBuild(b) // 同步直调：无登记路径
	got, err := e.builds.Get(ctx, e.db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateCancelled, got.State, "input-less build must land terminal, not bounce to queued")
	assert.Contains(t, got.Error, "build input lost")
	assert.True(t, got.State.Terminal())
	assert.Empty(t, fb.snapshot(), "the builder must never run without registered input")
}
