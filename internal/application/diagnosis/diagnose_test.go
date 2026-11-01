package diagnosis

// 卫星本地单测（P5 wave-6 拆包同批）：Deps 端口注入形态的装配契约——
// 观测快照端口 + 账本/告警 repo 端口 → Facts 三面；读不了 ≠ 不存在的
// None 三态；廉价先行筛命中矩阵。全链咬合（经 engine 装配）由 engine
// 白盒 diagnose_test.go 承载（Submit/step/handleObservation 驱动真实
// 观测缓存），本文件只钉卫星自身契约。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// fakeClock 是 hermetic 假时钟（架构 §11 自制夹具）。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// stubOutbox 只实现诊断消费面（ListByAggregateIDs；嵌入 nil Repo 封住
// 其余面——本卫星不消费）。
type stubOutbox struct {
	outbox.Repo
	rows map[string][]outbox.Event
	err  error
}

func (s stubOutbox) ListByAggregateIDs(context.Context, string, string, []string, int64, int) (map[string][]outbox.Event, error) {
	return s.rows, s.err
}

// stubAlertRules 只实现 ListByApp。
type stubAlertRules struct {
	alertrule.Repo
	rules []alertrule.Rule
	err   error
}

func (s stubAlertRules) ListByApp(context.Context, string) ([]alertrule.Rule, error) {
	return s.rules, s.err
}

// newSat 构造注入全端口的卫星实例。
func newSat(snap []capability.WorkloadEvent, ob stubOutbox, ar stubAlertRules, now time.Time) *Diagnosis {
	return New(Deps{
		Clock:      fakeClock{now: now},
		Outbox:     ob,
		AlertRules: ar,
		ObservationSnapshot: func(_ context.Context, _ string) []capability.WorkloadEvent {
			return snap
		},
		WorkloadProcess: func(workloadID string) (string, bool) {
			return "web", true
		},
	})
}

// TestFactsAssemblesFacesFromPorts：三面装配——观测快照端口投影 +
// RestartCount/LastExitAt 账本纯派生 + 近窗事件合并（Seq 升序）+ 告警
// 态 firing/pending/ok 投影；组件健康面 B2b 前恒 None。
func TestFactsAssemblesFacesFromPorts(t *testing.T) {
	now := time.Now().UTC()
	exit := 1
	snap := []capability.WorkloadEvent{{
		WorkloadID: "app-web", Generation: 1, State: capability.WorkloadFailed,
		ExitCode: &exit, Reason: "task: non-zero exit (1)",
	}}
	ob := stubOutbox{rows: map[string][]outbox.Event{
		"app-web": {
			{Seq: 2, Name: "workload.container_exited", AggregateID: "app-web", CreatedAt: now.Add(-time.Minute).Format(time.RFC3339), Payload: []byte(`{"workload_id":"app-web"}`)},
			{Seq: 5, Name: "workload.container_exited", AggregateID: "app-web", CreatedAt: now.Add(-30 * time.Second).Format(time.RFC3339), Payload: []byte(`{"workload_id":"app-web"}`)},
		},
	}}
	ar := stubAlertRules{rules: []alertrule.Rule{
		{ID: "r1", State: alertrule.StateFiring},
		{ID: "r2", PendingSince: now.Add(-time.Minute).Format(time.RFC3339)},
		{ID: "r3"},
	}}

	facts := newSat(snap, ob, ar, now).Facts(context.Background(), "app")

	obs, ok := facts.Observations.Value()
	require.True(t, ok)
	require.Len(t, obs, 1)
	assert.Equal(t, 2, obs[0].RestartCount, "restart count derives from the ledger rows")
	assert.False(t, obs[0].LastExitAt.IsZero())
	assert.Equal(t, "web", obs[0].Process, "process rides the workloadProcess port")

	events, ok := facts.Events.Value()
	require.True(t, ok)
	require.Len(t, events, 2)
	assert.Less(t, events[0].Seq, events[1].Seq, "events merge in seq order")

	alerts, ok := facts.Alerts.Value()
	require.True(t, ok)
	states := map[string]string{}
	for _, a := range alerts {
		states[a.Rule] = a.State
	}
	assert.Equal(t, map[string]string{"r1": "firing", "r2": "pending", "r3": "ok"}, states)

	assert.True(t, facts.Components.IsNone())
	assert.Equal(t, []string{diagnosis.FaceComponents}, facts.UnreadableFaces())
}

// TestFactsNoneFacesOnUnreadableSources：读不了 ≠ 不存在——账本/告警读
// 故障 → 对应面 None 且进 UnreadableFaces，观测面保持 Some（RestartCount
// 无从派生时不猜）。
func TestFactsNoneFacesOnUnreadableSources(t *testing.T) {
	boom := errors.New("injected source failure")
	exit := 1
	sat := newSat(
		[]capability.WorkloadEvent{{WorkloadID: "app-web", State: capability.WorkloadFailed, ExitCode: &exit}},
		stubOutbox{err: boom}, stubAlertRules{err: boom}, time.Now().UTC(),
	)

	facts := sat.Facts(context.Background(), "app")

	obs, ok := facts.Observations.Value()
	require.True(t, ok, "the observation face stays readable (port-backed)")
	assert.Equal(t, 0, obs[0].RestartCount, "restart count is underivable, not guessed")
	assert.True(t, facts.Events.IsNone())
	assert.True(t, facts.Alerts.IsNone())
	assert.ElementsMatch(t,
		[]string{diagnosis.FaceEvents, diagnosis.FaceAlerts, diagnosis.FaceComponents},
		facts.UnreadableFaces())
}

// TestCheapHitMatrix：先行筛命中矩阵——失败态全集命中、健康态与零观测
// 不命中（纯内存判定，零 repo 往返）。
func TestCheapHitMatrix(t *testing.T) {
	for _, st := range []capability.WorkloadState{
		capability.WorkloadFailed, capability.WorkloadDegraded,
		capability.WorkloadOrphaned, capability.WorkloadStopped,
	} {
		sat := newSat([]capability.WorkloadEvent{{WorkloadID: "app-web", State: st}}, stubOutbox{}, stubAlertRules{}, time.Now().UTC())
		assert.True(t, sat.CheapHit("app"), "state %s must hit the cheap pre-filter", st)
	}
	for _, st := range []capability.WorkloadState{
		capability.WorkloadRunning, capability.WorkloadPending, capability.WorkloadCompleted,
	} {
		sat := newSat([]capability.WorkloadEvent{{WorkloadID: "app-web", State: st}}, stubOutbox{}, stubAlertRules{}, time.Now().UTC())
		assert.False(t, sat.CheapHit("app"), "state %s must not hit the cheap pre-filter", st)
	}
	assert.False(t, newSat(nil, stubOutbox{}, stubAlertRules{}, time.Now().UTC()).CheapHit("app"),
		"zero observations must not hit (no deep diagnosis budget spent)")
}
