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
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	functionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/function"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"

	"github.com/fleetlyrun/fleetly/internal/testsupport"
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
	events  []string // outbox 事件名序（兑现事务断言面）
	kicks   int

	ownersMu     sync.Mutex
	owners       map[string]uint64
	specs        []capability.Workload
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
		Builds:            buildrepo.New(clock, db.Runner()),
		Outbox:            outboxrepo.New(clock, db.Runner()),
		Audits:            auditrepo.New(clock, db.Runner()),
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
		Commit: func(ctx context.Context, s transact.Stores, f contract.WriteFact) error {
			for _, check := range f.Checks {
				if err := check(ctx, s); err != nil {
					return err
				}
			}
			if f.Write != nil {
				if err := f.Write(ctx, s); err != nil {
					return err
				}
			}
			if err := writes.Sequence(ctx, writes.Repos{Outbox: s.Outbox, Audits: s.Audits}, f.Facts...); err != nil {
				return err
			}
			for _, fact := range f.Facts {
				h.events = append(h.events, fact().Name)
			}
			return nil
		},
		Log:   slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Clock: clock,
		LockMaintenance: func() func() {
			return func() {}
		},
		BoundedStep: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 10*time.Second)
		},
		Ensure: testsupport.NewMemoCache(),
		ProjectTeam: func(ctx context.Context, projectID string) (string, error) {
			return "default", nil
		},
		ProjectNetworkFacts: func(ctx context.Context, projectID string) ([]string, map[string]bool) {
			return []string{"net-a"}, nil
		},
		Observations: h,
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
		Kick: kickerFunc(func() { h.kicks++ }),
		RegistryEndpoint: func(ctx context.Context) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{Addr: "10.0.0.1:5000"}, nil
		},
	})
	return h
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

// 观测域句柄桩（批 1①b）：harness 兼作 contract.Observations——记账落
// harness 既有断言表（owners/specs/expected/forgotten），断言点零改。
func (h *harness) RecordOwners(functionID string, gen uint64, ws []capability.Workload) {
	h.ownersMu.Lock()
	defer h.ownersMu.Unlock()
	h.owners[functionID] = gen
}

func (h *harness) RecordOwnersByWorkload(gen uint64, ws []capability.Workload) {} // function 域不用逐载体形态

func (h *harness) RecordProjected(functionID string, ws []capability.Workload) {
	h.ownersMu.Lock()
	defer h.ownersMu.Unlock()
	h.specs = append(h.specs, ws...)
}

func (h *harness) RecordExpected(functionID string, gen uint64) {
	h.expectedMu.Lock()
	defer h.expectedMu.Unlock()
	h.expected[functionID] = gen
}

func (h *harness) ForgetExpected(functionID string) {
	h.expectedMu.Lock()
	defer h.expectedMu.Unlock()
	h.forgotten[functionID] = true
	delete(h.expected, functionID)
}

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

// ---- P2-1b private 半边：parked 别名/声明口集 ----

// ParkedFunctionAliases/ParkedFunctionPorts：parked（0 副本活跃）行集——
// 别名 = fn-<id> DNS 形；端口 = 冻结体声明口并集（同口去重；非 parked 行
// 不入集——唤醒后别名回挂 fn、组件让出口）。
func TestParkedFunctionAliasesAndPorts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	parkedA := h.seedFunction(t, ctx, "01JDFN000000000000000005", "park-a", 0,
		imageSpec("01JDFN000000000000000005", "img", 8080))
	h.seedFunction(t, ctx, "01JDFN000000000000000006", "park-b", 0,
		imageSpec("01JDFN000000000000000006", "img", 3000))
	h.seedFunction(t, ctx, "01JDFN000000000000000007", "awake", 2,
		imageSpec("01JDFN000000000000000007", "img", 8080))

	aliases, err := h.module.ParkedFunctionAliases(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fn-01jdfn000000000000000005", "fn-01jdfn000000000000000006"}, aliases)

	ports, err := h.module.ParkedFunctionPorts(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int32{8080, 3000}, ports)

	// 唤醒方向：parkedA 回 1 副本——别名与口都让位（fn 直挂接管）。
	require.NoError(t, h.repos.Functions.SetCapacity(ctx, parkedA.ID, 1))
	aliases, err = h.module.ParkedFunctionAliases(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fn-01jdfn000000000000000006"}, aliases)
	ports, err = h.module.ParkedFunctionPorts(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int32{3000}, ports)
}

// ParkedFunctionPorts 同口去重：两 parked 函数同声明口 → 并集一枚。
func TestParkedFunctionPortsDedup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedFunction(t, ctx, "01JDFN000000000000000008", "same-a", 0,
		imageSpec("01JDFN000000000000000008", "img", 8080))
	h.seedFunction(t, ctx, "01JDFN000000000000000009", "same-b", 0,
		imageSpec("01JDFN000000000000000009", "img", 8080))
	ports, err := h.module.ParkedFunctionPorts(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int32{8080}, ports)
}

// ---- P2-3 构建就绪门：环内兑现 ----

// pending 行 × Build 终态：succeeded → 一事务 SwapCurrent + 清位 +
// revision_changed；queued → D-4 重拾（intake face 被调）+ 等待。
func TestPromotePendingLandsOnBuildSuccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000010", "gate-fn", 1,
		imageSpec("01JDFN000000000000000010", "img", 8080))
	orig := row.CurrentRevisionID

	// 受理面同构：冻结新 revision（不同内容）+ 落 pending + 铸 queued Build。
	spec := imageSpec("01JDFN000000000000000010", "img-v2", 8080)
	body, err := protojson.Marshal(spec)
	require.NoError(t, err)
	seq, err := h.repos.FunctionRevisions.NextSeq(ctx, row.ID)
	require.NoError(t, err)
	pending := &function.Revision{FunctionID: row.ID, Seq: seq, Spec: body}
	require.NoError(t, h.repos.FunctionRevisions.Create(ctx, pending))
	require.NoError(t, h.repos.Functions.SetPending(ctx, row.ID, pending.ID))
	queued := &build.Build{FunctionID: row.ID, OwnerKind: build.OwnerFunction,
		RevisionID: pending.ID, State: build.StateQueued}
	require.NoError(t, h.repos.Builds.Create(ctx, queued))

	// queued → 推进步兑现不了（Build 未终态）；行面仍 pending。
	h.module.stepFunction(ctx)
	fresh, err := h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	require.NotEqual(t, pending.ID, fresh.CurrentRevisionID, "no swap before build success")
	require.NotEqual(t, "", fresh.PendingRevisionID, "pending stays anchored while building")

	// Build → succeeded → 兑现：SwapCurrent + 清位 + 事件一拍。
	require.NoError(t, h.repos.Builds.Transit(ctx, queued.ID,
		[]build.State{build.StateQueued}, build.StateSucceeded,
		func(m *build.Build) { m.Digest = "sha256:deadbeef" }))
	h.module.stepFunction(ctx)
	fresh, err = h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, pending.ID, fresh.CurrentRevisionID, "build success swaps the current revision")
	assert.Equal(t, "", fresh.PendingRevisionID, "pending anchor cleared on landing")
	require.Len(t, h.events, 1)
	assert.Equal(t, "function.revision_changed", h.events[0])
	_ = orig
}

// latest-wins：pending 被新 deploy 覆写后，旧 Build 兑现被放弃（锚不等）。
func TestPromotePendingIgnoresSupersededAnchor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000011", "supersede-fn", 1,
		imageSpec("01JDFN000000000000000011", "img", 8080))

	// 两个 pending 候选（v2/v3 连发——latest-wins）。
	mk := func(ref string) *function.Revision {
		body, err := protojson.Marshal(imageSpec(row.ID, ref, 8080))
		require.NoError(t, err)
		seq, err := h.repos.FunctionRevisions.NextSeq(ctx, row.ID)
		require.NoError(t, err)
		rev := &function.Revision{FunctionID: row.ID, Seq: seq, Spec: body}
		require.NoError(t, h.repos.FunctionRevisions.Create(ctx, rev))
		return rev
	}
	v2, v3 := mk("img-v2"), mk("img-v3")
	require.NoError(t, h.repos.Functions.SetPending(ctx, row.ID, v2.ID))
	queued := &build.Build{FunctionID: row.ID, OwnerKind: build.OwnerFunction,
		RevisionID: v2.ID, State: build.StateQueued}
	require.NoError(t, h.repos.Builds.Create(ctx, queued))
	// 用户连发 v3：pending 被覆写。
	require.NoError(t, h.repos.Functions.SetPending(ctx, row.ID, v3.ID))

	// v2 Build 成功 → 兑现被放弃（锚指向 v3）。
	require.NoError(t, h.repos.Builds.Transit(ctx, queued.ID,
		[]build.State{build.StateQueued}, build.StateSucceeded,
		func(m *build.Build) { m.Digest = "sha256:cafebabe" }))
	h.module.stepFunction(ctx)
	fresh, err := h.repos.Functions.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, v3.ID, fresh.PendingRevisionID, "latest-wins: pending stays on the newer anchor")
	assert.NotEqual(t, v2.ID, fresh.CurrentRevisionID, "superseded build does not swap")
}

// ---- P2-3 投影半边：from_build 改写（staging 实证 fb3 succeeded 后载体
// 停旧镜像——ProjectFunction image-only 假设缺 from_build 半边）----

// git 源 revision 兑现后 reconcile：handler.FromBuild → succeeded Build 的
// digest 引用改写——Ensure 收到的 Workload.Image = LocalImageDigestRef。
func TestReconcileFromBuildUsesBuildDigest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000012", "git-fn", 1,
		imageSpec("01JDFN000000000000000012", "img", 8080))
	h.module.stepFunction(ctx)

	// 冻结 git 形态 revision（from_build handler + Source_Git）+ 换轨 +
	// 落 succeeded Build（digest 定型——受理面 deployPending 同构）。
	spec := imageSpec(row.ID, "unused", 8080)
	spec.Source = &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: "https://x", Ref: "main"}}}
	spec.Build = &specv1.BuildSpec{Builder: "railpack"}
	spec.Handler.ImageOrigin = &specv1.ProcessSpec_FromBuild{FromBuild: "handler"}
	spec.Handler.Healthcheck = &specv1.HealthcheckSpec{Probe: &specv1.HealthcheckSpec_Exec{Exec: &specv1.ExecProbe{
		Command: []string{"/bin/sh", "-c", "grep -q :1F90 /proc/net/tcp"}}}}
	body, err := protojson.Marshal(spec)
	require.NoError(t, err)
	seq, err := h.repos.FunctionRevisions.NextSeq(ctx, row.ID)
	require.NoError(t, err)
	rev := &function.Revision{FunctionID: row.ID, Seq: seq, Spec: body}
	require.NoError(t, h.repos.FunctionRevisions.Create(ctx, rev))
	require.NoError(t, h.repos.Functions.SwapCurrent(ctx, row.ID, rev.ID, rev.Digest))
	require.NoError(t, h.repos.Builds.Create(ctx, &build.Build{
		FunctionID: row.ID, OwnerKind: build.OwnerFunction,
		RevisionID: rev.ID, State: build.StateSucceeded,
		Repo: "proj/git-fn", Digest: "sha256:abc123",
	}))

	h.module.stepFunction(ctx)
	require.Greater(t, h.runtime.ensureCount(), 0)
	// 最近一次 Ensure 的 Workload.Image = digest 引用（registryEndpoint
	// 夹具端口绑定 10.0.0.1:5000 形）。
	last := h.runtime.ensures[len(h.runtime.ensures)-1]
	assert.Contains(t, last.Workload.Image, "sha256:abc123")
	assert.Contains(t, last.Workload.Image, "10.0.0.1:5000")
	// healthcheck exec 声明随投影下发（探针声明面 P2-4 兑现闭环）。
	require.NotNil(t, last.Workload.Healthcheck)
	assert.Equal(t, []string{"/bin/sh", "-c", "grep -q :1F90 /proc/net/tcp"}, last.Workload.Healthcheck.Exec)
}

// ---- 引用面（防误删面） ----

// 编译期断言：repo 构造器与域接口的消费面存在（persistence/function 同批
// 落地；harness 经 statetest.Bind 拿全套 Stores）。
var (
	_ function.Repo         = functionrepo.New(nil, nil)
	_ function.RevisionRepo = functionrepo.NewRevisions(nil, nil)
)

// P2-0 前置批（设计 §4.3 落码）：per-function 互斥——Teardown 持锁期间
// reconcile 的 Ensure 不得插入（Remove 后零重建竞态窗）。行为断言：锁被
// 外部持有时 reconcile 阻塞（锁释放后完成且 Ensure 恰一次）。
func TestReconcileTeardownPerFunctionMutex(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seedFunction(t, ctx, "01JDFN000000000000000010", "mutex-fn", 1,
		imageSpec("01JDFN000000000000000010", "img", 8080))

	// 外持函数锁 → reconcile 应阻塞（带超时探测：短窗内不得产出 Ensure）。
	fnMu := h.module.lockFunction(row.ID)
	fnMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.module.reconcile(ctx, row)
	}()
	select {
	case <-done:
		t.Fatal("reconcile completed while the function mutex was held — the per-function lock is not enforced")
	case <-time.After(150 * time.Millisecond):
		// 阻塞如预期。
	}
	fnMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile never completed after the lock was released")
	}
	// 锁串行后 Ensure 恰一次。
	h.runtime.mu.Lock()
	ensures := len(h.runtime.ensures)
	h.runtime.mu.Unlock()
	if ensures != 1 {
		t.Fatalf("expected exactly one ensure after serialized reconcile, got %d", ensures)
	}
}

// kickerFunc 是 contract.Kicker 的本地桩（批 2 生长点实化：kick 端口自
// func() 改窄句柄——测试装配面同形，计数桩保留断言面）。
type kickerFunc func()

func (f kickerFunc) Kick() { f() }
