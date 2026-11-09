package diagnosis

// 应用模块本地单测（P5 wave-6 拆包同批）：Deps 端口注入形态的装配契约——
// 观测快照端口 + 账本/告警 repo 端口 → Facts 三面；读不了 ≠ 不存在的
// None 三态；廉价先行筛命中矩阵。全链咬合（经 engine 装配）由 engine
// 白盒 diagnose_test.go 承载（Submit/step/handleObservation 驱动真实
// 观测缓存），本文件只钉应用模块自身契约。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// fakeClock 是 hermetic 假时钟（架构 §11 自制夹具）。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// stubOutbox 只实现诊断消费面（ListByAggregateIDs；嵌入 nil Repo 封住
// 其余面——本应用模块不消费）。
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

// stubDeployments 只实现 ListByApp（部署行读面，新→旧序）。
type stubDeployments struct {
	deployment.Repo
	rows []deployment.Deployment
	err  error
}

func (s stubDeployments) ListByApp(context.Context, string, int64, int) ([]deployment.Deployment, error) {
	return s.rows, s.err
}

// newSat 构造注入全端口的应用模块实例。
func newSat(snap []capability.WorkloadEvent, ob stubOutbox, ar stubAlertRules, dp stubDeployments, now time.Time) *Diagnosis {
	return New(Deps{
		Clock:       fakeClock{now: now},
		Outbox:      ob,
		AlertRules:  ar,
		Deployments: dp,
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

	facts := newSat(snap, ob, ar, stubDeployments{}, now).Facts(context.Background(), "app")

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
		stubOutbox{err: boom}, stubAlertRules{err: boom}, stubDeployments{}, time.Now().UTC(),
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
		sat := newSat([]capability.WorkloadEvent{{WorkloadID: "app-web", State: st}}, stubOutbox{}, stubAlertRules{}, stubDeployments{}, time.Now().UTC())
		assert.True(t, sat.CheapHit("app"), "state %s must hit the cheap pre-filter", st)
	}
	for _, st := range []capability.WorkloadState{
		capability.WorkloadRunning, capability.WorkloadPending, capability.WorkloadCompleted,
	} {
		sat := newSat([]capability.WorkloadEvent{{WorkloadID: "app-web", State: st}}, stubOutbox{}, stubAlertRules{}, stubDeployments{}, time.Now().UTC())
		assert.False(t, sat.CheapHit("app"), "state %s must not hit the cheap pre-filter", st)
	}
	assert.False(t, newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{}, time.Now().UTC()).CheapHit("app"),
		"zero observations must not hit (no deep diagnosis budget spent)")
}

// legacyVolumeErrText 复刻 torchwood 走查实录的部署失败错误正文（engine
// volumes.go ErrLegacyVolumeCarrier 面——d1 failed 时错误在 deployments 行）。
const legacyVolumeErrText = `legacy name-keyed volume carrier present: volume "legacy-volume" still lives in its pre-ADR-0097 name-keyed carrier (docker volumes cannot be renamed); move the data to a volume created after the upgrade, or remove the runtime-side carrier and redeploy (fresh volume)`

// TestFactsAssemblesLatestDeployment：部署面行选——最新非终态优先/否则最新
// 终态（ListByApp 新→旧窗）；无行 = Some 零值（可读且确无）；读故障 = None
// + UnreadableFaces 透出。
func TestFactsAssemblesLatestDeployment(t *testing.T) {
	now := time.Now().UTC()
	failed := deployment.Deployment{
		ID: "app1-d1", AppID: "app1", State: deployment.StateFailed,
		Error: legacyVolumeErrText, Generation: 2, FinishedAt: now.Add(-time.Minute).Format(time.RFC3339),
	}

	t.Run("latest terminal row rides the face whole", func(t *testing.T) {
		facts := newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{rows: []deployment.Deployment{failed}}, now).
			Facts(context.Background(), "app1")
		d, ok := facts.LatestDeployment.Value()
		require.True(t, ok)
		assert.Equal(t, "app1-d1", d.ID)
		assert.Equal(t, deployment.StateFailed, d.State)
		assert.Equal(t, legacyVolumeErrText, d.Error)
		assert.EqualValues(t, 2, d.Generation)
		assert.NotEmpty(t, d.FinishedAt)
	})

	t.Run("newest non-terminal row wins over an older terminal one", func(t *testing.T) {
		newerQueued := deployment.Deployment{ID: "app1-d2", AppID: "app1", State: deployment.StateQueued, Generation: 3}
		facts := newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{rows: []deployment.Deployment{newerQueued, failed}}, now).
			Facts(context.Background(), "app1")
		d, ok := facts.LatestDeployment.Value()
		require.True(t, ok)
		assert.Equal(t, "app1-d2", d.ID, "the in-flight deployment is the latest story; the stale failure must not shadow it")
		assert.Equal(t, deployment.StateQueued, d.State)
	})

	t.Run("a non-terminal row buried under terminal rows still wins", func(t *testing.T) {
		olderFailed := failed
		olderFailed.ID, olderFailed.Generation = "app1-d0", 1
		buriedActive := deployment.Deployment{ID: "app1-d2", AppID: "app1", State: deployment.StateRollingBack, Generation: 3}
		facts := newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{rows: []deployment.Deployment{failed, olderFailed, buriedActive}}, now).
			Facts(context.Background(), "app1")
		d, _ := facts.LatestDeployment.Value()
		assert.Equal(t, "app1-d2", d.ID, "preference order: newest non-terminal first, else newest terminal")
	})

	t.Run("no rows is readable-empty, not none", func(t *testing.T) {
		facts := newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{}, now).Facts(context.Background(), "app1")
		d, ok := facts.LatestDeployment.Value()
		require.True(t, ok)
		assert.Empty(t, d.ID, "never deployed: readable and none (zero-value row)")
		assert.Equal(t, []string{diagnosis.FaceComponents}, facts.UnreadableFaces(), "the deployment face is readable and stays off the unreadable list")
	})

	t.Run("read failure surfaces as none", func(t *testing.T) {
		facts := newSat(nil, stubOutbox{}, stubAlertRules{}, stubDeployments{err: errors.New("deployments read failure")}, now).
			Facts(context.Background(), "app1")
		assert.True(t, facts.LatestDeployment.IsNone(), "unreadable does not mean absent")
		assert.ElementsMatch(t, []string{diagnosis.FaceComponents, diagnosis.FaceLatestDeployment}, facts.UnreadableFaces())
	})
}

// TestFactsLatestDeploymentDrivesFinding：fake 驱动全链——d1 failed 形态
// （错误正文在部署行、观测/事件/告警面可读且空）→ Evaluate 恰一条部署面
// 条目（立案③残留缺口的验收形态：此前此面恒报 no findings）。
func TestFactsLatestDeploymentDrivesFinding(t *testing.T) {
	now := time.Now().UTC()
	dp := stubDeployments{rows: []deployment.Deployment{{
		ID: "torchwood-d1", AppID: "torchwood", State: deployment.StateFailed,
		Error: legacyVolumeErrText, Generation: 1, FinishedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
	}}}
	facts := newSat(nil, stubOutbox{}, stubAlertRules{}, dp, now).Facts(context.Background(), "torchwood")

	got := diagnosis.Evaluate(facts)
	require.Len(t, got, 1, "a failed latest deployment with no observations yields exactly one finding")
	assert.Equal(t, "F_DEPLOYMENT_LATEST_FAILED", got[0].ID())
	assert.Equal(t, "declaration", string(got[0].FaultClass))
	assert.Equal(t, "torchwood-d1", got[0].Params["deployment"])
	assert.Equal(t, legacyVolumeErrText, got[0].Params["error"])
	assert.Equal(t, "torchwood-d1", got[0].Fix.Params["deployment"])
}
