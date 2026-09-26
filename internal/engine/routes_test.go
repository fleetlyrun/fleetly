package engine

// 路由发布挂点测试（T2.15）：发布严格晚于健康门（时序断言）、发布失败
// 不回滚部署（route.publish_failed 单独告警 + 审计）、compose 声明提取
// 与台账兜底。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// fakePublisher 是 RoutePublisher 的记录型假实现（calls 含失败调用；
// inputs 只记录成功载荷）。
type fakePublisher struct {
	calls   int
	inputs  []RoutePublishInput
	err     error
	errOnce bool
}

func (f *fakePublisher) PublishRoutes(_ context.Context, in RoutePublishInput) error {
	f.calls++
	if f.errOnce && f.err != nil {
		f.errOnce = false
		return f.err
	}
	f.inputs = append(f.inputs, in)
	return nil
}

const composeWithDomains = `name: demo
services:
  web:
    image: nginx:1.27-alpine
    expose: ["8080"]
    labels:
      fleetly.domains: "test.example.internal"
`

// routeServiceName 是 fixture 的 Swarm 服务名（naming 公式）。
// routeServiceName 已随 v0.3 三段命名公式改为 h.svc("web") 动态推导（const 字面量不可调用方法）。

// TestRoutePublishStrictlyAfterHealthGate 时序断言：releasing 阶段（健康
// 门通过前）发布器零调用；route.published 事件严格晚于 deployment.healthy。
func TestRoutePublishStrictlyAfterHealthGate(t *testing.T) {
	h := newHarness(t)
	pub := &fakePublisher{}
	h.eng = h.eng.WithRoutePublisher(pub)
	// 首更新滞留 PENDING：部署停在 releasing（健康门未过）。
	h.sub.setMode(h.svc("web"), modePending)
	path := h.writeCompose(composeWithDomains)
	rec := h.enqueue(path)
	ctx := context.Background()

	// 驱动到 releasing（切流之前）。
	for i := 0; i < 32; i++ {
		row, err := h.store.GetDeployment(ctx, rec.ID)
		if err != nil {
			t.Fatalf("get deployment: %v", err)
		}
		if row.Status == state.DeployReleasing {
			break
		}
		if row.Status.Terminal() {
			t.Fatalf("deployment reached terminal %s before releasing", row.Status)
		}
		h.clk.Advance(2 * time.Second)
		h.eng.Tick(ctx)
	}
	row, err := h.store.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if row.Status != state.DeployReleasing {
		t.Fatalf("status = %s, want releasing", row.Status)
	}
	if got := pub.calls; got != 0 {
		t.Fatalf("publisher called %d time(s) BEFORE health gate (invariant violation)", got)
	}
	eventsBeforeGate := len(h.events())

	// 健康门通过：切流 → observing → 首健康发布挂点。（假底座直接把
	// pending 服务收敛为 completed + 目标版本运行任务——ServiceUpdate
	// 语义只在新内容更新时改写行为模型，这里等价「任务已健康」。）
	svc := h.sub.services[h.svc("web")]
	svc.update = "completed"
	svc.message = ""
	svc.tasks = h.sub.runningTasks(svc, "t-new")
	h.clk.Advance(2 * time.Second)
	h.eng.Tick(ctx)
	if got := pub.calls; got != 1 {
		t.Fatalf("publisher calls after gate = %d, want 1", got)
	}
	in := pub.inputs[0]
	if in.AppName != "demo" || len(in.Declared) != 1 {
		t.Fatalf("publish input malformed: %+v", in)
	}
	if in.Declared[0].Service != "web" || in.Declared[0].Port != "8080" ||
		len(in.Declared[0].Domains) != 1 || in.Declared[0].Domains[0] != "test.example.internal" {
		t.Fatalf("route spec wrong: %+v", in.Declared[0])
	}

	// 事件时序：healthy/switched/observe_started 之后才有 route.published
	//（发布前的事件序列里没有任何 route.* 事件）。
	names := h.events()
	for _, n := range names[:eventsBeforeGate] {
		if strings.HasPrefix(n, "route.") {
			t.Fatalf("route event %q appeared before health gate", n)
		}
	}
	healthyIdx, publishedIdx := -1, -1
	for i, n := range names {
		switch n {
		case "deployment.healthy":
			if healthyIdx < 0 {
				healthyIdx = i
			}
		case "route.published":
			if publishedIdx < 0 {
				publishedIdx = i
			}
		}
	}
	if healthyIdx < 0 || publishedIdx < 0 {
		t.Fatalf("missing events: healthy=%d published=%d (%v)", healthyIdx, publishedIdx, names)
	}
	if publishedIdx < healthyIdx {
		t.Fatalf("route.published (idx %d) must be strictly after deployment.healthy (idx %d)",
			publishedIdx, healthyIdx)
	}
}

// TestRoutePublishFailureDoesNotFailDeployment 发布失败语义：deployment
// 照常 succeeded；route.publish_failed 事件 + 审计 error 行；观察窗终态
// 二次挂点补发成功（幂等收敛）。
func TestRoutePublishFailureDoesNotFailDeployment(t *testing.T) {
	h := newHarness(t)
	pub := &fakePublisher{err: errors.New("traefik converge: swarm not ready"), errOnce: true}
	h.eng = h.eng.WithRoutePublisher(pub)
	path := h.writeCompose(composeWithDomains)
	rec := h.enqueue(path)
	final := h.runToTerminal(rec)

	if final.Status != state.DeploySucceeded {
		t.Fatalf("deployment status = %s, want succeeded (publish failure must not fail deployment)",
			final.Status)
	}
	if got := pub.calls; got != 2 {
		t.Fatalf("publisher calls = %d, want 2 (gate fail + terminal retry)", got)
	}
	if len(pub.inputs) != 1 {
		t.Fatalf("successful publish payloads = %d, want 1 (terminal retry)", len(pub.inputs))
	}
	names := h.events()
	if !hasEvent(names, "route.publish_failed") {
		t.Fatalf("route.publish_failed event missing: %v", names)
	}
	if !hasEvent(names, "route.published") {
		t.Fatalf("route.published event missing after retry: %v", names)
	}
	// 审计：一条 error（E_ROUTE_PUBLISH_FAILED）+ 一条 ok。
	audits, err := h.store.RecentAudits(context.Background(), 50)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	publishAudits, errAudits := 0, 0
	for _, a := range audits {
		if a.Action == "route.publish" {
			publishAudits++
			if a.Result == "error" {
				errAudits++
				if a.ErrorCode != "E_ROUTE_PUBLISH_FAILED" {
					t.Fatalf("audit error code = %s", a.ErrorCode)
				}
			}
		}
	}
	if publishAudits != 2 || errAudits != 1 {
		t.Fatalf("route.publish audits = %d (error %d), want 2 (error 1)", publishAudits, errAudits)
	}
}

// TestRouteInputDeclaredSeedExtraction 发布输入 = label 种子候选提取
// （声明真值 = state 域名行，由 ingress 发布点现读；本包只提取种子）：
// compose 可重载且 hash 匹配 → Declared 非空；文件丢失/hash 漂移 → 不提供
// 种子（没有可靠声明源时不猜——不误删/误建路由）。
func TestRouteInputDeclaredSeedExtraction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, h.store, "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	// 文件丢失（声明源缺失）→ 无种子；state 行照常由 ingress 发布（state
	// 真值回归覆盖在 internal/ingress 的对应用例）。
	rec := state.DeployRecord{AppID: app.ID, AppName: "demo", SpecHash: "deadbeef"}
	in := h.eng.routePublishInput(ctx, recWithComposePath(rec, filepath.Join(h.t.TempDir(), "gone.yaml")))
	if in.AppName != "demo" || len(in.Declared) != 0 {
		t.Fatalf("missing compose file must yield no seed candidates: %+v", in)
	}
	// compose 可重载且 hash 匹配 → 种子候选 = expose 首端口 + 归一化域名。
	path := h.writeCompose(composeWithDomains)
	rec2, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID: app.ID, AppName: "demo", Kind: "deploy",
		SpecHash: specHashOf(path), ComposePath: path,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	in2 := h.eng.routePublishInput(ctx, rec2)
	if len(in2.Declared) != 1 || in2.Declared[0].Port != "8080" ||
		len(in2.Declared[0].Domains) != 1 || in2.Declared[0].Domains[0] != "test.example.internal" {
		t.Fatalf("compose-sourced seed candidates wrong: %+v", in2.Declared)
	}
	if strings.Contains(composeWithDomains, "ports") {
		t.Fatal("fixture must not use ports (rejected by the v0.1 controlled subset)")
	}
}

// recWithComposePath 是部署行的路径改写（文件丢失场景构造）。
func recWithComposePath(rec state.DeployRecord, path string) state.DeployRecord {
	rec.ComposePath = path
	return rec
}

// blockingPublisher 是慢于预算的协作型发布器（尊重 ctx 取消——ACME 签发
// 卡顿的形态抽象：block 远超预算，靠预算 ctx 兜底返回）。
type blockingPublisher struct {
	calls int
	block time.Duration
}

func (b *blockingPublisher) PublishRoutes(ctx context.Context, _ RoutePublishInput) error {
	b.calls++
	select {
	case <-time.After(b.block):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestRoutePublishBudgetBoundsTick H11 回归（B4）：发布器慢于预算（网络面
// 卡顿形态）——publishRoutes 在预算内返回（tick 不被拖住、全平台部署推进
// 不停摆）、超预算按既有 route.publish_failed 语义告警（部署照常 succeeded）。
func TestRoutePublishBudgetBoundsTick(t *testing.T) {
	h := newHarness(t)
	pub := &blockingPublisher{block: 30 * time.Second}
	h.eng.routeBudget = 200 * time.Millisecond // 预算注入（生产缺省 30s）
	h.eng = h.eng.WithRoutePublisher(pub)
	path := h.writeCompose(composeWithDomains)
	rec := h.enqueue(path)

	start := time.Now()
	final := h.runToTerminal(rec)
	elapsed := time.Since(start)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("deployment status = %s, want succeeded (over-budget publishing must not fail the deployment)", final.Status)
	}
	// 两个挂点（首健康 + 终态）各耗尽 200ms 预算：tick 未被 30s 阻塞拖住。
	if elapsed >= 5*time.Second {
		t.Fatalf("tick was held up by the slow publisher for %s (budget not effective)", elapsed)
	}
	if pub.calls != 2 {
		t.Fatalf("publisher calls = %d, want 2（gate + terminal）", pub.calls)
	}
	if n := countEvents(t, h, "route.publish_failed"); n != 2 {
		t.Fatalf("route.publish_failed count = %d, want 2 (one per over-budget hook)", n)
	}
}
