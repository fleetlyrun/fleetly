package placement

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeDocker 是放置测试的底座替身（state.DockerClient 端口最小实现：
// 自省 + 全量节点直读快照；未实现的方法永不被 placement 调用）。
type fakeDocker struct {
	selfID  string
	nodes   []state.SubstrateNode
	listErr error
}

func (f *fakeDocker) Ping(_ context.Context) error { return nil }

func (f *fakeDocker) ListNodeObservations(_ context.Context) ([]state.SubstrateNode, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.nodes, nil
}

func (f *fakeDocker) SelfNodeID(_ context.Context) (string, error) {
	return f.selfID, nil
}

func (f *fakeDocker) UpdateNodeLabel(_ context.Context, _, _ string, _ string, _ state.ObjectVersion) error {
	return nil
}

func (f *fakeDocker) ResolveObjectVersion(_ context.Context, _ state.ObjectKind, _ string) (state.ObjectVersion, error) {
	return state.ObjectVersion{}, nil
}

func (f *fakeDocker) SubscribeEvents(_ context.Context) (<-chan state.SubstrateEvent, error) {
	return nil, errors.New("not used")
}

func (f *fakeDocker) Close() error { return nil }

// harness 是每个用例的独立环境：单节点快照（ready）+ 已锚定的平台身份。
type harness struct {
	store      *state.Store
	docker     *fakeDocker
	resolver   *Resolver
	appID      string
	platformID string
	hostname   string
	swarmID    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := state.Open(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	app, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	platformID := "n_" + ulid.Make().String()
	if err := st.SetMeta(ctx, state.MetaKeyPlatformNodeID, platformID); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	h := &harness{
		store:      st,
		docker:     &fakeDocker{},
		resolver:   NewResolver(st, nil),
		appID:      app.ID,
		platformID: platformID,
		hostname:   "srv-01",
		swarmID:    "swarm-" + ulid.Make().String(),
	}
	h.setSelf("ready", "active")
	h.resolver = NewResolver(st, h.docker)
	return h
}

// setSelf 设置直读快照：本机 + 可选第二节点（多节点守卫用例）。
func (h *harness) setSelf(nodeState, availability string, extra ...state.SubstrateNode) {
	nodes := []state.SubstrateNode{{
		SwarmNodeID:  h.swarmID,
		Hostname:     h.hostname,
		State:        nodeState,
		Availability: availability,
		Labels:       map[string]string{state.LabelNodeID: h.platformID},
	}}
	nodes = append(nodes, extra...)
	h.docker.selfID = h.swarmID
	h.docker.nodes = nodes
}

// input 是默认有卷输入（T2-2 fixture my-api 的放置切面）。
func (h *harness) input() Input {
	return Input{
		AppID:   h.appID,
		AppName: "my-api",
		Volumes: []VolumeMount{{Key: "data", Target: "/var/lib/data"}},
	}
}

// TestResolveVolumesAutoBindSelf 验收 4：有卷 compose → resolve 输出本机
// 绑定 + 约束字符串；Apply 落库绑定 + 卷注册表（docker_name = 命名约定值）。
func TestResolveVolumesAutoBindSelf(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	d, err := h.resolver.Resolve(ctx, h.input())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !d.Bind || d.PlatformNodeID != h.platformID {
		t.Fatalf("decision = %+v, want bind to %s", d, h.platformID)
	}
	if d.Source != state.PlacementSourcePlatform || d.KeptExisting {
		t.Fatalf("decision = %+v", d)
	}
	wantConstraint := "node.labels." + state.LabelNodeID + " == " + h.platformID
	if d.Constraint != wantConstraint {
		t.Fatalf("constraint = %q, want %q", d.Constraint, wantConstraint)
	}

	d2, err := h.resolver.Apply(ctx, h.input())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if d2.Constraint != wantConstraint {
		t.Fatalf("applied constraint = %q", d2.Constraint)
	}
	p, err := h.store.GetPlacement(ctx, h.appID)
	if err != nil || p.PlatformNodeID != h.platformID || p.State != state.PlacementBound {
		t.Fatalf("placement = %+v err=%v", p, err)
	}
	if p.PinnedAt.IsZero() || p.Etag == "" {
		t.Fatalf("pinned_at/etag missing: %+v", p)
	}

	vols, err := h.store.ListAppVolumes(ctx, h.appID)
	if err != nil || len(vols) != 1 {
		t.Fatalf("volumes = %+v err=%v", vols, err)
	}
	wantName, err := naming.VolumeName("my-api", "data", h.appID)
	if err != nil {
		t.Fatalf("volume name: %v", err)
	}
	if vols[0].Name != wantName || vols[0].PlatformNodeID != h.platformID {
		t.Fatalf("volume = %+v, want name %s node %s", vols[0], wantName, h.platformID)
	}
	// 事件：volume.created + placement.bound（M1-12 补发——绑定与事件/审计
	// 同事务，stateful-placement §2.8）；审计：placement.bound。
	assertEvent(t, h.store, "volume.created")
	assertEvent(t, h.store, "placement.bound")
	assertAudit(t, h.store, "placement.bound")

	// 幂等：重复 Apply 不重写绑定（KeptExisting）。
	d3, err := h.resolver.Apply(ctx, h.input())
	if err != nil || !d3.KeptExisting {
		t.Fatalf("second apply = %+v err=%v, want kept", d3, err)
	}
}

// assertEvent / assertAudit 是 Outbox/审计的负面测试基座（secret 不入事件/
// 审计的全局纪律在此面上同样成立）。
func assertEvent(t *testing.T, st *state.Store, name string) {
	t.Helper()
	events, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Name == name {
			return
		}
	}
	t.Fatalf("event %s not found", name)
}

func assertAudit(t *testing.T, st *state.Store, action string) {
	t.Helper()
	recs, err := st.RecentAudits(context.Background(), 100)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	for _, r := range recs {
		if r.Action == action {
			return
		}
	}
	t.Fatalf("audit %s not found", action)
}

// TestResolveLabelReferences 验收 4/前哨分支：label 以名或 ID 引用本机均
// 解析成功（来源记 label）；未声明 label 的自动绑定来源记 platform。
func TestResolveLabelReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"by hostname", "srv-01"},
		{"by platform id", ""},
	} {
		if tc.ref == "" {
			continue // platform id 形态在 TestResolveUnknownLabel 覆盖
		}
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			in := h.input()
			in.LabelRef = tc.ref
			d, err := h.resolver.Apply(context.Background(), in)
			if err != nil {
				t.Fatalf("resolve by %q: %v", tc.ref, err)
			}
			if d.Source != state.PlacementSourceLabel {
				t.Fatalf("source = %s, want label", d.Source)
			}
		})
	}
	// 平台 ID 引用。
	h := newHarness(t)
	in := h.input()
	in.LabelRef = h.platformID
	d, err := h.resolver.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("resolve by platform id: %v", err)
	}
	if d.Source != state.PlacementSourceLabel || d.PlatformNodeID != h.platformID {
		t.Fatalf("decision = %+v", d)
	}
}

// TestResolveUnknownLabel label 指向未知节点 → E_PLACEMENT_NODE_NOT_FOUND
// （422 + 候选清单）；形态非法 → E_PLACEMENT_NODE_INVALID。
func TestResolveUnknownLabel(t *testing.T) {
	h := newHarness(t)
	in := h.input()
	in.LabelRef = "no-such-node"
	_, err := h.resolver.Resolve(context.Background(), in)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NODE_NOT_FOUND" {
		t.Fatalf("err = %v, want E_PLACEMENT_NODE_NOT_FOUND", err)
	}
	if !strings.Contains(err.Error(), "srv-01") {
		t.Fatalf("candidates missing from message: %s", err)
	}

	in.LabelRef = "bad node!"
	if _, err := h.resolver.Resolve(context.Background(), in); !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NODE_INVALID" {
		t.Fatalf("err = %v, want E_PLACEMENT_NODE_INVALID", err)
	}
}

// TestResolveKeepsExistingBinding 不变量：绑定优先于 label 的缺失——既有
// 绑定保持，label 缺失/变化都不触发迁移。
func TestResolveKeepsExistingBinding(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.resolver.Apply(ctx, h.input()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	in := h.input()
	in.Volumes = nil // 无卷声明变化也不丢绑定
	in.LabelRef = ""
	d, err := h.resolver.Resolve(ctx, in)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !d.KeptExisting || !d.Bind || d.PlatformNodeID != h.platformID {
		t.Fatalf("decision = %+v, want kept binding", d)
	}
	if d.Constraint != "" {
		t.Fatalf("stateless keep should not compile constraint, got %q", d.Constraint)
	}
}

// TestStatelessPinWarning 验收 4：无卷应用显式 pin → W_PLACEMENT_STATELESS_PIN，
// 且不产生绑定（无卷不钉）。
func TestStatelessPinWarning(t *testing.T) {
	h := newHarness(t)
	in := h.input()
	in.Volumes = nil
	in.LabelRef = "srv-01"
	d, err := h.resolver.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if d.Bind {
		t.Fatalf("stateless app must not pin: %+v", d)
	}
	found := false
	for _, w := range d.Warnings {
		if w.Code == "W_PLACEMENT_STATELESS_PIN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %+v, want W_PLACEMENT_STATELESS_PIN", d.Warnings)
	}
	// 无 label 无卷：静默不钉。
	in.LabelRef = ""
	d, err = h.resolver.Resolve(context.Background(), in)
	if err != nil || d.Bind || len(d.Warnings) != 0 {
		t.Fatalf("stateless no-label decision = %+v err=%v", d, err)
	}
}

// TestResolveNotReadyNoCandidate 自动选点候选 = ready（§2.5）：本机非 ready
// 且需自动绑定 → E_PLACEMENT_NO_ELIGIBLE_NODE。
func TestResolveNotReadyNoCandidate(t *testing.T) {
	h := newHarness(t)
	h.setSelf("down", "active")
	_, err := h.resolver.Resolve(context.Background(), h.input())
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NO_ELIGIBLE_NODE" {
		t.Fatalf("err = %v, want E_PLACEMENT_NO_ELIGIBLE_NODE", err)
	}
}

// TestMultiNodeGuard 验收 4：拓扑非单节点 → E_CAPABILITY_REQUIRES_MULTI_NODE；
// MoveBinding/守卫函数同码。
func TestMultiNodeGuard(t *testing.T) {
	h := newHarness(t)
	h.setSelf("ready", "active", state.SubstrateNode{
		SwarmNodeID: "swarm-2", Hostname: "srv-02", State: "ready", Availability: "active",
	})
	_, err := h.resolver.Resolve(context.Background(), h.input())
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_CAPABILITY_REQUIRES_MULTI_NODE" {
		t.Fatalf("resolve err = %v, want E_CAPABILITY_REQUIRES_MULTI_NODE", err)
	}

	// 换点路径静态守卫。
	if err := h.resolver.MoveBinding(context.Background(), h.appID, "srv-02", "restored", true); !errors.As(err, &ae) || ae.Code() != "E_CAPABILITY_REQUIRES_MULTI_NODE" {
		t.Fatalf("move err = %v, want guard code", err)
	}
	if err := GuardMultiNode(2); !errors.As(err, &ae) || ae.Code() != "E_CAPABILITY_REQUIRES_MULTI_NODE" {
		t.Fatalf("guard err = %v", err)
	}
	if err := GuardMultiNode(1); err != nil {
		t.Fatalf("single node guarded: %v", err)
	}
}

// TestPreflightBranches 验收 4 的前哨分支：ready → nil；down/drain →
// E_PLACEMENT_NODE_UNAVAILABLE；节点消失 → E_PLACEMENT_NODE_GONE；
// 数据前哨（单机短路但代码路径保留）→ E_VOLUME_NODE_MISMATCH。
func TestPreflightBranches(t *testing.T) {
	ctx := context.Background()
	mkBound := func(t *testing.T) *harness {
		h := newHarness(t)
		if _, err := h.resolver.Apply(ctx, h.input()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if _, err := h.store.UpsertRuntimeNodeRef(ctx, h.platformID, h.swarmID, time.Now().UTC()); err != nil {
			t.Fatalf("ref: %v", err)
		}
		return h
	}

	t.Run("ready", func(t *testing.T) {
		h := mkBound(t)
		if err := h.resolver.Preflight(ctx, h.appID); err != nil {
			t.Fatalf("preflight = %v, want nil", err)
		}
	})
	t.Run("down", func(t *testing.T) {
		h := mkBound(t)
		h.setSelf("down", "active")
		err := h.resolver.Preflight(ctx, h.appID)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NODE_UNAVAILABLE" {
			t.Fatalf("err = %v, want E_PLACEMENT_NODE_UNAVAILABLE", err)
		}
	})
	t.Run("drain", func(t *testing.T) {
		h := mkBound(t)
		h.setSelf("ready", "drain")
		err := h.resolver.Preflight(ctx, h.appID)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NODE_UNAVAILABLE" {
			t.Fatalf("err = %v, want E_PLACEMENT_NODE_UNAVAILABLE", err)
		}
	})
	t.Run("node gone", func(t *testing.T) {
		h := mkBound(t)
		h.docker.nodes = nil
		err := h.resolver.Preflight(ctx, h.appID)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code() != "E_PLACEMENT_NODE_GONE" {
			t.Fatalf("err = %v, want E_PLACEMENT_NODE_GONE", err)
		}
	})
	t.Run("volume data mismatch", func(t *testing.T) {
		h := mkBound(t)
		// 卷数据钉在另一平台节点（模拟残留/手工移动）→ 数据前哨 409。
		if _, _, err := h.store.RegisterAppVolume(ctx, state.VolumeWrite{
			AppID: h.appID, Key: "other", Name: "fleetly-my-api-other-aaaaaaaa",
			PlatformNodeID: "n_otherplace",
		}); err != nil {
			t.Fatalf("register: %v", err)
		}
		err := h.resolver.Preflight(ctx, h.appID)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code() != "E_VOLUME_NODE_MISMATCH" {
			t.Fatalf("err = %v, want E_VOLUME_NODE_MISMATCH", err)
		}
	})
	t.Run("no binding passes", func(t *testing.T) {
		h := newHarness(t)
		if err := h.resolver.Preflight(ctx, h.appID); err != nil {
			t.Fatalf("stateless app preflight = %v, want nil", err)
		}
	})
}

// TestConstraintFor 约束编译逐字对照（stateful-placement §2.1 执行层）。
func TestConstraintFor(t *testing.T) {
	if got := ConstraintFor("n_01HZX"); got != "node.labels.fleetly.node-id == n_01HZX" {
		t.Fatalf("constraint = %q", got)
	}
}
