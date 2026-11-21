package capacity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	functionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/function"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMetrics 是决策环观测双面的可编程桩：按查询内容分发（traefik 请求
// 率 / 负载词汇 CPU）；onQuery 钩子承载 CAS 竞态注入（查询期改行——
// 落败方下拍重算的乐观并发断言）。
type fakeMetrics struct {
	capability.Metrics
	rates   map[string]float64 // routerName → req/s
	cpus    map[string]float64 // function → cores
	err     error
	queries []string
	onQuery func()
}

func (f *fakeMetrics) QuerySeries(_ context.Context, query string, _, _ time.Time, _ time.Duration) ([]capability.Series, error) {
	f.queries = append(f.queries, query)
	if f.onQuery != nil {
		f.onQuery()
		f.onQuery = nil
	}
	if f.err != nil {
		return nil, f.err
	}
	switch {
	case strings.Contains(query, "traefik_service_requests_total"):
		out := make([]capability.Series, 0, len(f.rates))
		for k, v := range f.rates {
			out = append(out, capability.Series{
				Metric: map[string]string{"routerName": k},
				Points: []capability.SeriesPoint{{Value: v}},
			})
		}
		return out, nil
	case strings.Contains(query, capability.MetricWorkloadCPUCores):
		out := make([]capability.Series, 0, len(f.cpus))
		for k, v := range f.cpus {
			out = append(out, capability.Series{
				Metric: map[string]string{"function": k},
				Points: []capability.SeriesPoint{{Value: v}},
			})
		}
		return out, nil
	}
	return nil, nil
}

// harness 是决策环白盒夹具：真 repo（functions + 事实落库双面）+ 可编程
// 观测桩 + 可控墙钟。
type harness struct {
	module  *Capacity
	clock   *statetest.FakeClock
	repos   transact.Stores
	metrics *fakeMetrics
	kicks   int
	events  []string // outbox 事件名序（断言面）
	quota   func(projectID string, active int, sum, delta int64) error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, clock := statetest.New(t)
	statetest.SeedProject(t, db, "01JD0PROJ00000000000000000", "default")
	run := db.Runner()
	stores := transact.Stores{
		Functions:         functionrepo.New(clock, run),
		FunctionRevisions: functionrepo.NewRevisions(clock, run),
		Projects:          projectrepo.New(clock, run),
		Outbox:            outboxrepo.New(clock, run),
		Audits:            auditrepo.New(clock, run),
	}
	h := &harness{clock: clock, repos: stores, metrics: &fakeMetrics{},
		quota: quotaLaw}
	h.module = New(Deps{
		Repos:          stores,
		Metrics:        h.metrics,
		RouteMetricKey: func(r capability.Route) string { return r.Host + "-k" },
		QuotaCheck: func(ctx context.Context, st transact.Stores, projectID string, delta int64) error {
			active, sum, err := st.Functions.StatsByProject(ctx, projectID)
			if err != nil {
				return err
			}
			return h.quota(projectID, active, sum, delta)
		},
		FactTeam: func(ctx context.Context, projectID string) (string, error) { return "team-x", nil },
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
			// 事件名序账（断言面——真实 outbox 消费方镜像）。
			for _, fact := range f.Facts {
				h.events = append(h.events, fact().Name)
			}
			return nil
		},
		KickFunctions: func() { h.kicks++ },
		Log:           slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		Clock:         clock,
	})
	return h
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// quotaLaw 是测试侧配额定律镜像（与 api.CheckFunctionQuota 同判据——
// 哨兵 contract.ErrFunctionQuota 包装，defer 语义分流据此判定）。
func quotaLaw(projectID string, active int, sum, delta int64) error {
	if sum+delta > 64 {
		return fmt.Errorf("%w: project %s replica sum %d + %d would exceed the limit", contract.ErrFunctionQuota, projectID, sum, delta)
	}
	return nil
}

// seedFunction 落函数行（容量初值直给；冻结体锚空 = 决策环不消费 spec）。
func (h *harness) seedFunction(t *testing.T, id, name string, replicas, minR, maxR int64, exposure, host string, idleSecs int64) *function.Function {
	t.Helper()
	row := &function.Function{ID: id, ProjectID: "01JD0PROJ00000000000000000", Name: name,
		Replicas: replicas, MinReplicas: minR, MaxReplicas: maxR,
		IdleTimeoutSecs: idleSecs, Exposure: exposure, PublicHost: host, Status: "running"}
	require.NoError(t, h.repos.Functions.Create(ctxOf(), row))
	return row
}

func ctxOf() context.Context { return context.Background() }

// stepOnce 推一拍（决策环粗节拍锚外的直驱——测试不经节拍锚，时间推进
// 显式表达）。
func (h *harness) stepOnce(t *testing.T) {
	t.Helper()
	h.module.lastScrapeAt = time.Time{} // 粗锚旁路：测试显式控拍
	h.module.Step(ctxOf())
}

func (h *harness) replicas(t *testing.T, id string) int64 {
	t.Helper()
	row, err := h.repos.Functions.Get(ctxOf(), id)
	require.NoError(t, err)
	return row.Replicas
}

// ---- 缩零（park）分支 ----

// min=0 函数静默满窗 → 行 CAS 归 0 + function.scaled(cause=park) 事件 +
// Kick；活动时钟重置（唤醒即活动——Knative scale-down-delay 同款）。
func TestParkOnSilence(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000A", "parkme", 2, 0, 2, "private", "", 30)

	h.stepOnce(t) // 首见：活动时钟 = now（保守）
	require.Equal(t, int64(2), h.replicas(t, row.ID), "no park before the idle window")

	h.clock.Advance(31 * time.Second)
	h.stepOnce(t) // 静默满窗：park
	assert.Equal(t, int64(0), h.replicas(t, row.ID))
	require.Len(t, h.events, 1)
	assert.Equal(t, "function.scaled", h.events[0])
	assert.Equal(t, 1, h.kicks)
}

// 行旋钮生效：idle_timeout_secs=60 的窗在 31s 静默下不 park。
func TestParkRespectsRowKnob(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000B", "longwin", 1, 0, 2, "private", "", 60)

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "silence shorter than the row knob window")

	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(0), h.replicas(t, row.ID), "silence past the row knob window parks")
}

// 唤醒转移（0→N）是活动信号：观测面缺席时 busy 函数在窗后 park（退化
// 语义诚实——观测面健康是产品承诺前提）；唤醒后窗口重新计时。
func TestWakeTransitionRefreshesActivity(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000C", "woken", 1, 0, 2, "private", "", 30)

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	require.Equal(t, int64(0), h.replicas(t, row.ID))

	// 唤醒（RequestWake CAS——activator 同源写径）。
	_, err := h.repos.Functions.RequestWake(ctxOf(), row.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), h.replicas(t, row.ID))

	h.clock.Advance(20 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "wake resets the idle window (20s < 30s)")

	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(0), h.replicas(t, row.ID), "silence past the window parks again")
}

// 活动信号在场（载体 CPU 高于地板）不 park——private 函数的结构性观测
// 补位（负载词汇 function 轴）。
func TestCPUActivityPreventsPark(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000D", "busy", 1, 0, 2, "private", "", 30)
	h.metrics.cpus = map[string]float64{"01jdfn00000000000000000d": 0.2}

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "cpu activity keeps the function awake")
}

// 请求率在场（public 函数经分账键）不 park；观测查询失败 = 无信号面
// （诚实断口）——静默判定继续，窗后 park（退化语义）。
func TestRequestRateActivityAndQueryFailure(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000E", "pubfn", 1, 0, 2, "public", "pub.example.com", 30)
	h.metrics.rates = map[string]float64{"pub.example.com-k": 3}

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "request activity keeps the function awake")

	h.metrics.err = errors.New("vm down")
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(0), h.replicas(t, row.ID), "query failure degrades to wake-timer park (documented)")
}

// min>0 函数静默缩容到地板 min 且永不归 0（window floor 是用户承诺——
// private 函数的载体 CPU 静默即活动信号静默，缩容语义一致）。
func TestScaleDownToMinOnSilence(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000F", "resident", 2, 1, 4, "private", "", 30)

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "silence scales down to the min floor (2 -> 1)")

	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "min floor holds forever (never parked to 0)")
}

// ---- 扩容分支 ----

// per-副本请求压力超阈值 → +1（CAS + function.scaled(cause=autoscale)）。
func TestScaleUpOnPressure(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000G", "hot", 1, 1, 4, "public", "hot.example.com", 300)
	h.metrics.rates = map[string]float64{"hot.example.com-k": 25} // 25/1 > 10

	h.stepOnce(t)
	assert.Equal(t, int64(2), h.replicas(t, row.ID))
	require.Len(t, h.events, 1)
	assert.Equal(t, "function.scaled", h.events[0])
	assert.Equal(t, 1, h.kicks)
}

// defer 语义：配额超限 → 本拍放弃 + function.scale_deferred 事件沿上
// 单发；行面外生变化（用户 scale）清沿，新沿再落一枚 defer 事件。
func TestScaleUpDeferredOnQuota(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000H", "capped", 1, 1, 16, "public", "cap.example.com", 300)
	h.metrics.rates = map[string]float64{"cap.example.com-k": 999}
	h.quota = func(projectID string, active int, sum, delta int64) error {
		return fmt.Errorf("%w: injected quota ceiling", contract.ErrFunctionQuota)
	}

	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "quota defer abandons this tick")
	require.Len(t, h.events, 1)
	assert.Equal(t, "function.scale_deferred", h.events[0])

	h.clock.Advance(10 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "defer edge holds: no repeat event")
	require.Len(t, h.events, 1, "defer edge suppresses repeat events")

	// 行面变化（用户手动 scale）清 defer 沿——决策基线已被外生动改写。
	require.NoError(t, h.repos.Functions.SetCapacity(ctxOf(), row.ID, 2))
	h.clock.Advance(10 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(2), h.replicas(t, row.ID), "external change cleared the defer edge; quota still defers the scale-up")
	require.Len(t, h.events, 2, "new defer edge after external change emits one fresh event (per-edge single-shot)")
	assert.Equal(t, "function.scale_deferred", h.events[1])
}

// ---- 缩容分支 ----

// 请求静默持续满窗 → 单拍至多减半、地板 min。
func TestScaleDownHalvingFloor(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000I", "cool", 4, 1, 4, "public", "cool.example.com", 30)

	h.stepOnce(t) // 首见（保守：活动时钟 = now）
	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(2), h.replicas(t, row.ID), "halving: 4 -> 2")

	h.clock.Advance(31 * time.Second)
	h.stepOnce(t)
	assert.Equal(t, int64(1), h.replicas(t, row.ID), "halving with floor: 2 -> 1 (= min)")
}

// ---- 竞态：决策环与唤醒/用户 scale 的乐观并发 ----

// 观测期行被他写变（唤醒/用户 scale）→ CAS 落败，决策放弃——无撕列
// （唤醒后函数不被同一拍的 park 压回 0）。
func TestParkCASLosesToExternalWrite(t *testing.T) {
	h := newHarness(t)
	row := h.seedFunction(t, "01JDFN00000000000000000J", "race", 1, 0, 2, "private", "", 30)

	h.stepOnce(t)
	h.clock.Advance(31 * time.Second)
	// 查询期注入外生写（用户 scale 同形——replicas 与 updated_at 双变，
	// 发生在行列举之后、CAS 之前的拍内窗口）。
	h.metrics.onQuery = func() {
		require.NoError(t, h.repos.Functions.SetCapacity(ctxOf(), row.ID, 2))
	}
	h.stepOnce(t)
	assert.Equal(t, int64(2), h.replicas(t, row.ID), "external write wins the optimistic anchor; park retries next tick")
}
