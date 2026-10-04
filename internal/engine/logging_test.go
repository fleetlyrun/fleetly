package engine

// Logging 域测试（F2.4/ADR-0040）：P11 脱敏锚（build log 中 Secret 值
// 零出现）+ 采集环（域流对账/批汇/游标自愈）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
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
func (f *fakeLogging) snapshot() []capability.LogFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.LogFrame, len(f.ingested))
	copy(out, f.ingested)
	return out
}

// logStreamRuntime 在 fakeRuntime 上补 RuntimeLogs 子面：记录查询；帧由
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

// TestBuildLogRedactionSecretZeroOccurrence 是 P11 验收锚（ADR-0040 决策 3）：
// 注入已知 Secret（推送凭证 + 材料文件值）→ build log 全量帧断言原始值
// 零出现 + secret:<fp8> 指纹形态在场；VL 持久化批同样零出现。
func TestBuildLogRedactionSecretZeroOccurrence(t *testing.T) {
	const pushSecret = "push-secret-0123456789abcdef"
	const fileSecret = "file-secret-value-9876543210"
	fb := &fakeBuilder{digest: "sha256:built", logs: []string{
		"pushing with authorization " + pushSecret,
		"material reads " + fileSecret + " ok",
		"clean line stays untouched",
		pushSecret, // 整行即密值
	}}
	db, clock := statetest.New(t)
	fl := &fakeLogging{}
	e := New(Deps{DB: db, Runtime: newFakeRuntime(),
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Registry: newFakeRegistry(),
		Logging:  fl,
		Logger:   discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000C1")
	b := &build.Build{ID: "01JD0BUILD000000000000000C1", AppID: tAppID, RevisionID: revID, State: build.StateBuilding}
	require.NoError(t, e.builds.Create(ctx, e.db.Runner(), b))
	e.build.inputsMu.Lock()
	e.build.inputs[b.ID] = capability.BuildRequest{
		Builder:  specir.BuilderDockerfile,
		PushCred: &capability.RegistryCredential{Server: "reg:5000", Username: "u", Secret: pushSecret},
		SecretFiles: map[string][]byte{
			"env": []byte(fileSecret),
		},
	}
	e.build.inputsMu.Unlock()

	e.executeBuild(b)

	got, err := e.builds.Get(ctx, e.db.Runner(), b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateSucceeded, got.State, "build must succeed with redaction in place")

	frames := e.RecentBuildLogs(b.ID)
	require.NotEmpty(t, frames)
	fpPush := fingerprint8(pushSecret)
	fpFile := fingerprint8(fileSecret)
	for _, f := range frames {
		line := string(f.Line)
		assert.NotContains(t, line, pushSecret, "P11 anchor: raw push secret must never appear in build logs")
		assert.NotContains(t, line, fileSecret, "P11 anchor: raw file secret must never appear in build logs")
	}
	joined := frameLinesJoined(frames)
	assert.Contains(t, joined, fpPush)
	assert.Contains(t, joined, fpFile)
	assert.Contains(t, joined, "clean line stays untouched")

	// VL 持久化批：同脱敏 + 域盖戳（kind=build、Source=BuildID、team/project/app）。
	ingested := fl.snapshot()
	require.NotEmpty(t, ingested)
	for _, f := range ingested {
		assert.NotContains(t, string(f.Line), pushSecret)
		assert.NotContains(t, string(f.Line), fileSecret)
		assert.Equal(t, capability.LogKindBuild, f.Kind)
		assert.Equal(t, b.ID, f.Source)
		assert.Equal(t, tProjectID, f.Project)
		assert.Equal(t, tAppID, f.App)
		assert.NotEmpty(t, f.Team)
	}
}

// TestBuildLogRedactionDisabledWithoutLogging：Logging 面停用（nil）时出口链
// 仍脱敏（P11 与持久化解耦——脱敏是出口纪律不是存储特性）。
func TestBuildLogRedactionWithoutLogging(t *testing.T) {
	const pushSecret = "push-only-secret-abcdef012345"
	fb := &fakeBuilder{digest: "sha256:built", logs: []string{"auth " + pushSecret}}
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(),
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Registry: newFakeRegistry(),
		Logger:   discardLogger()}, Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000C2")
	b := &build.Build{ID: "01JD0BUILD000000000000000C2", AppID: tAppID, RevisionID: revID, State: build.StateBuilding}
	require.NoError(t, e.builds.Create(ctx, e.db.Runner(), b))
	e.build.inputsMu.Lock()
	e.build.inputs[b.ID] = capability.BuildRequest{
		Builder:  specir.BuilderDockerfile,
		PushCred: &capability.RegistryCredential{Server: "reg:5000", Username: "u", Secret: pushSecret},
	}
	e.build.inputsMu.Unlock()

	e.executeBuild(b)
	for _, f := range e.RecentBuildLogs(b.ID) {
		assert.NotContains(t, string(f.Line), pushSecret, "redaction must hold without the persistence face")
	}
}

// TestRedactTableOrdering 钉替换序：长值优先（短值是长值子串时不抢先）；
// 短值（<8B）不进表。
func TestRedactTableOrdering(t *testing.T) {
	long := "supersecretvalue-0123456789"
	short := "secretvalue" // 是 long 的子串且 ≥8B——必须后替换（否则 long 的匹配面被破坏）
	table := newRedactTable([]string{short, long, "tiny", ""})
	out := string(table.redact([]byte("x " + long + " y " + short + " z tiny")))
	assert.NotContains(t, out, long)
	assert.NotContains(t, out, short)
	assert.Contains(t, out, "x "+fingerprint8(long))
	assert.Contains(t, out, "z tiny", "short values below the threshold stay literal")
}

// TestNSBatchStampsAndFlush 钉批汇器：域盖戳（kind=runtime + team/project/
// app）、达量 flush、游标推进只在成功后落盘。
func TestNSBatchStampsAndFlush(t *testing.T) {
	db, clock := statetest.New(t)
	fl := &fakeLogging{}
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logging: fl, Logger: discardLogger()},
		Options{DataRoot: t.TempDir()})
	ns := capability.NamespaceRef{Team: "t1", Project: "P1", App: "A1"}
	b := &nsBatch{e: e, ns: ns}
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
	cur, err := e.logpipe.cursors.Get(context.Background(), e.db.Runner(), ns.String())
	require.NoError(t, err)
	assert.WithinDuration(t, ts.Add(time.Duration(logIngestBatchFrames-1)*time.Millisecond), cur, time.Millisecond)

	// Ingest 失败：错误上抛（断流自愈触发面）+ 游标不推进。
	fl.mu.Lock()
	fl.fail = true
	fl.ingested = nil
	fl.mu.Unlock()
	b2 := &nsBatch{e: e, ns: ns, cursor: cur}
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
	cur2, err := e.logpipe.cursors.Get(context.Background(), e.db.Runner(), ns.String())
	require.NoError(t, err)
	assert.Equal(t, cur, cur2, "cursor must freeze on ingest failure (replay anchor)")
}

// TestLoggingStepStreamLifecycle 钉域流对账：活跃域起流（查询带 Since=
// 游标/Follow）、域消失（app tombstone）即 cancel。
func TestLoggingStepStreamLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	rt := newLogStreamRuntime()
	e := New(Deps{DB: db, Runtime: rt, Logging: &fakeLogging{}, Logger: discardLogger()},
		Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))

	e.loggingStep(ctx)
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
	require.NoError(t, e.apps.SoftDelete(ctx, e.db.Runner(), tAppID))
	e.loggingStep(ctx)
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
func seedProjectApp(t *testing.T, clock state.Clock, db *state.DB) error {
	t.Helper()
	ctx := context.Background()
	if err := project.New(clock).Create(ctx, db.Runner(), &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}); err != nil {
		return err
	}
	return app.New(clock).Create(ctx, db.Runner(), &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"})
}

// frameLinesJoined 是帧行拼接（断言辅助）。
func frameLinesJoined(frames []capability.LogFrame) string {
	var b strings.Builder
	for _, f := range frames {
		b.Write(f.Line)
		b.WriteByte('\n')
	}
	return b.String()
}

// fingerprint8 是脱敏指纹短形态的独立复算（测试侧独立实现避免恒真断言）。
func fingerprint8(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "secret:" + hex.EncodeToString(sum[:])[:8]
}
