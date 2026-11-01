package logpipes

// 采集环卫星白盒（F2.4/ADR-0040；P5 wave-1 随域拆包自 engine 包迁入，
// ADR-0088 决策 2）：批汇器域盖戳/达量 flush/游标推进契约与失败自愈面。
// 跨域集成面（build 脱敏链、Step 流生命周期经引擎整环驱动）留在 engine
// 包 logging_test.go。

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
	logcursorrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/logcursor"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// fakeLogging 是 Logging 端口假底座（与 engine 包 logging_test 同款，包
// 隔离副本）：记录 Ingest 批次；可编程失败。
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
func (f *fakeLogging) snapshot() []capability.LogFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.LogFrame, len(f.ingested))
	copy(out, f.ingested)
	return out
}

// newTestSatellite 建 hermetic 卫星（真 SQLite 游标 repo + 假存储端口 +
// 假时钟；rootCtx/spawn 缺省为 Background/直跑——白盒面不依赖核装配）。
func newTestSatellite(t *testing.T) (*Logpipes, *fakeLogging, logcursor.Repo, *statetest.FakeClock) {
	t.Helper()
	db, clock := statetest.New(t)
	cursors := logcursorrepo.New(clock, db.Runner())
	fl := &fakeLogging{}
	s := New(Deps{
		Logging: fl,
		Log:     slog.New(slog.DiscardHandler),
		Cursors: cursors,
		Clock:   clock,
		RootCtx: func() context.Context { return context.Background() },
		Spawn:   func(fn func()) { go fn() }, //nolint:gosec // 白盒批汇面无排水语义（goroutine 由流收口）
	})
	return s, fl, cursors, clock
}

// TestNSBatchStampsAndFlush 钉批汇器：域盖戳（kind=runtime + team/project/
// app）、达量 flush、游标推进只在成功后落盘（P5 wave-1 自 engine 包逐字
// 迁入——断言面零漂移）。
func TestNSBatchStampsAndFlush(t *testing.T) {
	s, fl, cursors, clock := newTestSatellite(t)
	ns := capability.NamespaceRef{Team: "t1", Project: "P1", App: "A1"}
	b := &nsBatch{logging: s.logging, log: s.log, clock: s.clock, cursors: cursors, ns: ns}
	ts := clock.Now()
	for i := 0; i < logIngestBatchFrames; i++ {
		require.NoError(t, b.WriteLog(context.Background(), capability.LogFrame{
			WorkloadID: "W1", Container: "task1", Node: "n1",
			Time: ts.Add(time.Duration(i) * time.Millisecond), Line: []byte("line"),
		}))
	}
	// 达量已 flush：帧全部入库且盖戳正确。
	got := fl.snapshot()
	require.Len(t, got, logIngestBatchFrames)
	for _, f := range got {
		assert.Equal(t, capability.LogKindRuntime, f.Kind)
		assert.Equal(t, "t1", f.Team)
		assert.Equal(t, "P1", f.Project)
		assert.Equal(t, "A1", f.App)
	}
	// 游标已落盘（首拍直落）。
	cur, err := cursors.Get(context.Background(), ns.String())
	require.NoError(t, err)
	assert.WithinDuration(t, ts.Add(time.Duration(logIngestBatchFrames-1)*time.Millisecond), cur, time.Millisecond)

	// Ingest 失败：错误上抛（断流自愈触发面）+ 游标不推进。
	fl.mu.Lock()
	fl.fail = true
	fl.ingested = nil
	fl.mu.Unlock()
	b2 := &nsBatch{logging: s.logging, log: s.log, clock: s.clock, cursors: cursors, ns: ns, cursor: cur}
	for i := 0; i < logIngestBatchFrames; i++ {
		err := b2.WriteLog(context.Background(), capability.LogFrame{
			Time: cur.Add(time.Duration(i+1) * time.Minute), Line: []byte("later"),
		})
		if i == logIngestBatchFrames-1 {
			require.Error(t, err, "threshold flush must surface the ingest failure to abort the stream")
		} else {
			require.NoError(t, err)
		}
	}
	cur2, err := cursors.Get(context.Background(), ns.String())
	require.NoError(t, err)
	assert.Equal(t, cur, cur2, "cursor must freeze on ingest failure (replay anchor)")
}
