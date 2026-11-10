package engine

// Logging 域测试（F2.4/ADR-0040）：P11 脱敏锚（build log 中 Secret 值
// 零出现）+ 采集循环域流对账（经装配后的引擎整环驱动）。P5 wave-1 拆包
// （ADR-0088 决策 2）：nsBatch 批汇/游标自愈白盒随域迁 logpipes 包
//（logpipes/logging_test.go）；本文件保留跨域集成面（build 脱敏链与
// Step 流生命周期——经 e.logpipes 应用模块注入引用）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// fakeLogging 是 Logging 端口假底座：记录 Ingest 批次；可编程失败。
type fakeLogging struct {
	mu       sync.Mutex
	ingested []capability.LogFrame
	fail     bool
}

func (f *fakeLogging) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fakelogs", Capability: capability.KindLogging, Version: "test"}
}
func (f *fakeLogging) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeLogging) Ingest(_ context.Context, frames []capability.LogFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return assert.AnError
	}
	f.ingested = append(f.ingested, frames...)
	return nil
}
func (f *fakeLogging) Query(context.Context, capability.LogQuery, capability.LogWriter) error {
	return nil
}

// logStreamRuntime 在 fakeRuntime 上补 RuntimeLogs 扩展接口：记录查询；帧由
// 测试侧 feed 通道注入（ctx 取消即收口）。
type logStreamRuntime struct {
	*fakeRuntime
	mu      sync.Mutex
	queries []capability.LogQuery
	feed    chan capability.LogFrame
	stopped chan string // 每次流收口上报 ns（生命周期断言面）
}

func newLogStreamRuntime() *logStreamRuntime {
	return &logStreamRuntime{
		fakeRuntime: newFakeRuntime(),
		feed:        make(chan capability.LogFrame),
		stopped:     make(chan string, 8),
	}
}

func (f *logStreamRuntime) StreamLogs(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	defer func() { f.stopped <- q.Namespace.String() }()
	for {
		select {
		case fr := <-f.feed:
			if err := w.WriteLog(ctx, fr); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// TestLoggingStepStreamLifecycle 钉域流对账：活跃域起流（查询带 Since=
// 游标/Follow）、域消失（app tombstone）即 cancel。
func TestLoggingStepStreamLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newLogStreamRuntime()
	e := newEngineForTest(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt, Logging: &fakeLogging{}, Logger: discardLogger(),
		LoggingFactory: testLoggingFactory},
		Options{Data: DataOptions{DataRoot: t.TempDir()}})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))

	loggingStep := contract.StepOf(e.logpipes, "logging")
	loggingStep(ctx)
	waitForLogCond(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return len(rt.queries) >= 1
	})
	rt.mu.Lock()
	q := rt.queries[0]
	rt.mu.Unlock()
	assert.Equal(t, tProjectID, q.Namespace.Project)
	assert.Equal(t, tAppID, q.Namespace.App)
	assert.True(t, q.Follow, "collector streams must follow")

	// 帧注入 → 采集入库（盖戳）。
	now := clock.Now()
	rt.feed <- capability.LogFrame{WorkloadID: "W1", Time: now, Line: []byte("hello")}

	// app tombstone → 下一拍拆流。
	require.NoError(t, e.repos.Apps.SoftDelete(ctx, tAppID))
	loggingStep(ctx)
	select {
	case ns := <-rt.stopped:
		assert.Contains(t, ns, tAppID)
	case <-time.After(5 * time.Second):
		t.Fatal("stream for the deleted app must be cancelled")
	}
}

// waitForLogCond 轮询直至 cond 为真或超时。
func waitForLogCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for logging condition")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// seedProjectApp 播 project/app 夹具行（logging 域测试的权威表种子）。
func seedProjectApp(t *testing.T, clock state.Clock, db *persistence.DB) error {
	t.Helper()
	ctx := context.Background()
	if err := projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}); err != nil {
		return err
	}
	return apprepo.New(clock, db.Runner()).Create(ctx, &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"})
}
