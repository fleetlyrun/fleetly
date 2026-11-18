package functions

// Functions 应用模块白盒（ADR-0102 任务③B；dataservices 同族形态）：投影
// 单源公式（env 注入/缺省 tcp 探针/寻址/常驻语义）、收敛步（Ensure 全链 +
// 签名短路 + 容量变更重下发的 scale 语义 + secret 材料解密）、聚合收口
//（Remove + 缓存清理）。夹具：真 SQLite 台账 repo + Runtime 三动词假面 +
// ensure cache 协议端口本地实现 + 观测窄接口捕获盒。

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	functionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/function"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// ---- 夹具 ----

type ensureCall struct {
	NS        capability.NamespaceRef
	Gen       capability.Generation
	Workload  capability.Workload
	Materials capability.Materials
}

type fakeRuntime struct {
	capability.Runtime
	mu       sync.Mutex
	ensures  []ensureCall
	removed  []capability.NamespaceRef
	failNext bool
}

func (f *fakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range ws {
		f.ensures = append(f.ensures, ensureCall{NS: ns, Gen: gen, Workload: w, Materials: m})
	}
	if f.failNext {
		f.failNext = false
		return capability.EnsureReport{}, fmt.Errorf("injected ensure failure")
	}
	return capability.EnsureReport{}, nil
}

func (f *fakeRuntime) Remove(_ context.Context, ns capability.NamespaceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

func (f *fakeRuntime) ensureCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ensures)
}

// harness 是模块白盒夹具：真 repo + 假面 + 端口本地实现。
type harness struct {
	module  *Functions
	runtime *fakeRuntime
	repos   transact.Stores
	db      state.DB

	ownersMu     sync.Mutex
	owners       map[string]uint64
	expectedMu   sync.Mutex
	expected     map[string]uint64
	forgotten    map[string]bool
	observations map[string]capability.WorkloadEvent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, clock := statetest.New(t)
	stores := transact.Stores{
		Functions:         functionrepo.New(clock, db.Runner()),
		FunctionRevisions: functionrepo.NewRevisions(clock, db.Runner()),
	}
	rt := &fakeRuntime{}
	h := &harness{
		runtime:      rt,
		repos:        stores,
		db:           db,
		owners:       map[string]uint64{},
		expected:     map[string]uint64{},
		forgotten:    map[string]bool{},
		observations: map[string]capability.WorkloadEvent{},
	}
	h.module = New(Deps{
		Runtime: rt,
		Repos:   stores,
		Log:     slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Clock:   clock,
		LockMaintenance: func() func() {
			return func() {}
		},
		BoundedStep: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 10*time.Second)
		},
		EnsureFresh: func(m map[string]contract.Memo, id, sig string, now time.Time) (contract.Memo, bool) {
			memo, ok := m[id]
			return memo, ok && memo.Sig == sig
		},
		EnsureRemember: func(m map[string]contract.Memo, id string, memo contract.Memo) { m[id] = memo },
		EnsureForget:   func(m map[string]contract.Memo, id string) { delete(m, id) },
		ProjectTeam: func(ctx context.Context, projectID string) (string, error) {
			return "default", nil
		},
		ProjectNetworkFacts: func(ctx context.Context, projectID string) ([]string, map[string]bool) {
			return []string{"net-a"}, nil
		},
		RecordOwners: func(functionID string, gen uint64, ws []capability.Workload) {
			h.ownersMu.Lock()
			defer h.ownersMu.Unlock()
			h.owners[functionID] = gen
		},
		RecordExpected: func(functionID string, gen uint64) {
			h.expectedMu.Lock()
			defer h.expectedMu.Unlock()
			h.expected[functionID] = gen
		},
		ForgetExpected: func(functionID string) {
			h.expectedMu.Lock()
			defer h.expectedMu.Unlock()
			h.forgotten[functionID] = true
			delete(h.expected, functionID)
		},
		// 核 e.obs.forget 按 workloadID 连带清归属路由（observdomain.go
		// forgetWorkloads）——桩镜像同语义。
		ForgetObservation: func(functionID string) {
			delete(h.observations, functionID)
			h.ownersMu.Lock()
			delete(h.owners, functionID)
			h.ownersMu.Unlock()
		},
		ObservationOf: func(functionID string) (capability.WorkloadEvent, bool) {
			ev, ok := h.observations[functionID]
			return ev, ok
		},
		EnsureMemo: map[string]contract.Memo{},
	})
	return h
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

// seedFunction 落函数行 + 冻结体 + 换轨（受理面的最小同构：行持 current
// 指针与指纹）。
func (h *harness) seedFunction(t *testing.T, ctx context.Context, id, name string, replicas int64, spec *specv1.FunctionSpec) *function.Function {
	t.Helper()
	statetest.SeedProject(t, h.db, "01JD0PROJ00000000000000000", "default")
	row := &function.Function{ID: id, ProjectID: "01JD0PROJ00000000000000000", Name: name, Replicas: replicas, MinReplicas: 1, MaxReplicas: 4, Status: "pending"}
	require.NoError(t, h.repos.Functions.Create(ctx, row))
	body, err := protojson.Marshal(spec)
	require.NoError(t, err)
	seq, err := h.repos.FunctionRevisions.NextSeq(ctx, id)
	require.NoError(t, err)
	rev := &function.Revision{FunctionID: id, Seq: seq, Spec: body}
	require.NoError(t, h.repos.FunctionRevisions.Create(ctx, rev))
	require.NoError(t, h.repos.Functions.SwapCurrent(ctx, id, rev.ID, "seed-fp"))
	row.CurrentRevisionID = rev.ID
	return row
}

func imageSpec(fnID, ref string, port int32) *specv1.FunctionSpec {
	return &specv1.FunctionSpec{
		SchemaVersion: 1,
		Function:      &specv1.FunctionRef{Id: fnID, Project: "01JD0PROJ00000000000000000"},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: ref}}},
		Handler: &specv1.ProcessSpec{
			Name: "handler", ImageOrigin: &specv1.ProcessSpec_Image{Image: ref},
			Ports: []*specv1.PortSpec{{Port: port}},
		},
	}
}

// ---- 投影单源公式 ----

func TestProjectFunctionShape(t *testing.T) {
	row := &function.Function{ID: "01JDFN000000000000000000", ProjectID: "01JD0PROJ00000000000000000", Name: "resize-img", Replicas: 2}
	w, ns, err := ProjectFunction(imageSpec(row.ID, "ghcr.io/acme/resize:1", 8080), row, "acme", []string{"net-a", "net-b"}, nil, "abcdef1234567890")
	require.NoError(t, err)

	assert.Equal(t, capability.NamespaceRef{Team: "acme", Project: row.ProjectID, Function: row.ID, FunctionName: "resize-img"}, ns)
	assert.Equal(t, row.ID, w.ID, "workload id = row id (stable across revisions)")
	assert.Equal(t, "handler", w.Process)
	assert.Equal(t, "ghcr.io/acme/resize:1", w.Image)
	assert.Equal(t, int64(2), w.Replicas, "capacity folds from the row plane")
	assert.Equal(t, capability.RestartAlways, w.Restart)
	assert.Equal(t, fnStopGrace, w.StopGrace)
	assert.Equal(t, []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolHTTP}}, w.Ports)
	assert.Equal(t, []capability.Address{{Name: contract.FunctionDNSName(row.ID)}}, w.Addressing, "fn-<id> is the exposure-independent permanent name")
	assert.Equal(t, "01JDFN000000000000000000", w.Env["FLEETLY_FUNCTION_ID"])
	assert.Equal(t, "resize-img", w.Env["FLEETLY_FUNCTION_NAME"])
	assert.Equal(t, "abcdef123456", w.Env["FLEETLY_FUNCTION_REVISION"], "revision digest first 12")
	assert.Equal(t, "8080", w.Env["FLEETLY_FUNCTION_PORT"])
	require.NotNil(t, w.Healthcheck)
	assert.Equal(t, int32(8080), w.Healthcheck.TCPPort, "default probe is tcp on the declared port")
	assert.Empty(t, w.Healthcheck.HTTPPath)
}

func TestProjectFunctionRejects(t *testing.T) {
	row := &function.Function{ID: "01JDFN000000000000000001", ProjectID: "p", Name: "fn"}
	// 无 image 源（v1 image-only）。
	noImage := imageSpec(row.ID, "", 8080)
	_, _, err := ProjectFunction(noImage, row, "t", nil, nil, "d")
	require.ErrorContains(t, err, "image source")
	// tcp 端口协议（禁面）。
	tcp := imageSpec(row.ID, "img", 8080)
	tcp.Handler.Ports[0].Protocol = specv1.Protocol_PROTOCOL_TCP
	_, _, err = ProjectFunction(tcp, row, "t", nil, nil, "d")
	require.ErrorContains(t, err, "http or h2c")
	// 端口数 ≠ 1。
	two := imageSpec(row.ID, "img", 8080)
	two.Handler.Ports = append(two.Handler.Ports, &specv1.PortSpec{Port: 9090})
	_, _, err = ProjectFunction(two, row, "t", nil, nil, "d")
	require.ErrorContains(t, err, "exactly one port")
}

// ---- 收敛步 ----

func TestReconcileEnsureAndShortCircuit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec := imageSpec("01JDFN000000000000000002", "fn-img:1", 8080)
	row := h.seedFunction(t, ctx, "01JDFN000000000000000002", "webhook-relay", 1, spec)

	h.module.stepFunction(ctx)
	require.Equal(t, 1, h.runtime.ensureCount(), "first pass ensures")
	call := h.runtime.ensures[0] // fakeRuntime 已按 len 断言单调用
	assert.Equal(t, row.ID, call.Workload.ID)
	assert.Equal(t, row.ID, call.NS.Function)
	// 归属与看门狗期望登记（replicas>0）。
	assert.Contains(t, h.owners, row.ID)
	assert.Contains(t, h.expected, row.ID)

	// 未变重拍：签名短路零 Ensure。
	h.module.stepFunction(ctx)
	assert.Equal(t, 1, h.runtime.ensureCount(), "unchanged signature short-circuits")

	// 容量变更（行面旋钮）：指纹含副本数 → 重新 Ensure（scale 语义）。
	require.NoError(t, h.repos.Functions.SetCapacity(ctx, row.ID, 3))
	h.module.stepFunction(ctx)
	assert.Equal(t, 2, h.runtime.ensureCount(), "replica change re-ensures")
	last := h.runtime.ensures[len(h.runtime.ensures)-1]
	assert.Equal(t, int64(3), last.Workload.Replicas)
}

func TestReconcileObservationAdvancesStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000003", "status-fn", 1,
		imageSpec("01JDFN000000000000000003", "img", 8080))

	h.module.stepFunction(ctx)
	fresh, err := h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", fresh.Status, "no observation keeps the default")

	// 观测到位（wire-gen 匹配）：running 采信。
	h.observations[row.ID] = capability.WorkloadEvent{WorkloadID: row.ID, Generation: 1, State: capability.WorkloadRunning}
	h.module.stepFunction(ctx)
	fresh, err = h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", fresh.Status, "matching wire-gen observation advances the cache")

	// 代次错位：不采信（wire-gen 匹配才推进）。
	h.observations[row.ID] = capability.WorkloadEvent{WorkloadID: row.ID, Generation: 9, State: capability.WorkloadStopped}
	h.module.stepFunction(ctx)
	fresh, err = h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", fresh.Status, "stale-generation observation is ignored")
}

// ---- 聚合收口 ----

func TestTeardownFunction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000004", "gone-fn", 1,
		imageSpec("01JDFN000000000000000004", "img", 8080))
	h.module.stepFunction(ctx)
	require.Equal(t, 1, h.runtime.ensureCount())

	require.NoError(t, h.module.TeardownFunction(ctx, row.ID))
	require.Len(t, h.runtime.removed, 1)
	assert.Equal(t, row.ID, h.runtime.removed[0].Function)
	assert.True(t, h.forgotten[row.ID], "watchdog expectation is cleared")
	assert.NotContains(t, h.owners, row.ID)

	// 不存在的函数：ErrNotFound（API 404 口径）。
	err := h.module.TeardownFunction(ctx, "01JDFN000000000000009")
	require.ErrorIs(t, err, state.ErrNotFound)
}

// ---- 引用面（防误删面） ----

// 编译期断言：repo 构造器与域接口的消费面存在（persistence/function 同批
// 落地；harness 经 statetest.Bind 拿全套 Stores）。
var (
	_ function.Repo         = functionrepo.New(nil, nil)
	_ function.RevisionRepo = functionrepo.NewRevisions(nil, nil)
)
