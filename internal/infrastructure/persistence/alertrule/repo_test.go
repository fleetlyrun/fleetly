package alertrule_test

// AlertRule 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 落
// 行铸造面（ID = app+seq 派生、初始态 ok、时间戳取假时钟、last 观测列缺
// 省）、Get 双态读径、ListEnabled/ListByApp/ListAll 三列表口径（启用过
// 滤、App 隔离、序稳定）、Observe 观测落值不迁态、Transition 迁移评估态
// （fired/resolved 沿锚）、Delete 删行（缺行 ErrNotFound——幂等性归调用
// 面）。alert_rules 无 (app, metric) 唯一轴（重复规则是调用侧裁决，不入
// repo 契约），同名冲突归一面不存在，不硬造。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	alertrulerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/alertrule"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// TestAlertRuleCreateGetListFaces：Create 铸造面 + Get 读径 + 三列表口径。
func TestAlertRuleCreateGetListFaces(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	rules := alertrulerepo.New(clock, db.Runner())

	// 首见落行：ID 由 app+seq 铸、初始态 ok、last 观测列缺省。
	r1 := &alertrule.Rule{AppID: "web", Metric: alertrule.MetricCPUPercent, Threshold: 80, ForSecs: 60, Enabled: true}
	require.NoError(t, rules.Create(ctx, r1))
	assert.Equal(t, "web-1", r1.ID, "default id joins app id and seq")
	assert.Equal(t, int64(1), r1.Seq)
	assert.Equal(t, alertrule.StateOK, r1.State, "fresh rule starts in ok state")
	assert.Equal(t, state.FormatTime(clock.Now()), r1.StateSince)
	assert.Equal(t, r1.CreatedAt, r1.StateSince, "state_since anchors at creation")
	assert.Equal(t, state.FormatTime(clock.Now()), r1.CreatedAt)
	assert.Equal(t, r1.CreatedAt, r1.UpdatedAt)
	assert.Zero(t, r1.LastValue)
	assert.Empty(t, r1.LastObservedAt)

	// 同 App 第二条规则 + 禁用行 + 异 App 行（alert_rules 无业务唯一轴，
	// 同 App 同指标双行合法）。
	r2 := &alertrule.Rule{AppID: "web", Metric: alertrule.MetricMemoryWorkingSet, Threshold: 1 << 28, Enabled: false}
	require.NoError(t, rules.Create(ctx, r2))
	r3 := &alertrule.Rule{AppID: "api", Metric: alertrule.MetricCPUPercent, Threshold: 90, Enabled: true}
	require.NoError(t, rules.Create(ctx, r3))

	// Get：命中与缺行 NotFound。
	got, err := rules.Get(ctx, r1.ID)
	require.NoError(t, err)
	assert.Equal(t, "web", got.AppID)
	assert.Equal(t, alertrule.MetricCPUPercent, got.Metric)
	assert.Equal(t, float64(80), got.Threshold)
	assert.Equal(t, int64(60), got.ForSecs)
	assert.True(t, got.Enabled)
	_, err = rules.Get(ctx, "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// ListEnabled：只含启用行，app 升序（派发评估面的口径）。
	enabled, err := rules.ListEnabled(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 2)
	assert.Equal(t, "api-3", enabled[0].ID)
	assert.Equal(t, "web-1", enabled[1].ID)

	// ListByApp：App 隔离 + 含禁用行 + seq 序。
	byApp, err := rules.ListByApp(ctx, "web")
	require.NoError(t, err)
	require.Len(t, byApp, 2)
	assert.Equal(t, r1.ID, byApp[0].ID)
	assert.Equal(t, r2.ID, byApp[1].ID)
	assert.False(t, byApp[1].Enabled, "ListByApp carries disabled rows too")

	// ListAll：全量行，app 升序（alerts list 面口径）。
	all, err := rules.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "api", all[0].AppID)
	assert.Equal(t, "web", all[2].AppID)
}

// TestAlertRuleObserveTransitionDelete：评估写面（观测落值、迁移锚）与
// 配置删面。
func TestAlertRuleObserveTransitionDelete(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	rules := alertrulerepo.New(clock, db.Runner())

	r := &alertrule.Rule{AppID: "web", Metric: alertrule.MetricCPUPercent, Threshold: 80, Enabled: true}
	require.NoError(t, rules.Create(ctx, r))
	createdAt := r.CreatedAt

	// Observe 落最近评估观测：值 + 时刻入行，updated_at 推进，态不动。
	clock.Advance(10e9)
	observedAt := "2026-01-01T00:00:10Z"
	require.NoError(t, rules.Observe(ctx, r.ID, 95.5, observedAt))
	got, err := rules.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, 95.5, got.LastValue)
	assert.Equal(t, observedAt, got.LastObservedAt)
	assert.Equal(t, state.FormatTime(clock.Now()), got.UpdatedAt, "observe refreshes updated_at")
	assert.Equal(t, alertrule.StateOK, got.State, "observe never moves the evaluation state")
	assert.Equal(t, createdAt, got.StateSince)

	// Transition 迁移评估态：firing 沿持久锚（state_since 取通知时刻）。
	clock.Advance(5e9)
	firedAt := "2026-01-01T00:00:15Z"
	require.NoError(t, rules.Transition(ctx, r.ID, alertrule.StateFiring, firedAt))
	got, err = rules.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, alertrule.StateFiring, got.State)
	assert.Equal(t, firedAt, got.StateSince)
	assert.Equal(t, state.FormatTime(clock.Now()), got.UpdatedAt)
	assert.Equal(t, 95.5, got.LastValue, "transition leaves the observation columns alone")

	// resolved 沿回 ok。
	clock.Advance(5e9)
	require.NoError(t, rules.Transition(ctx, r.ID, alertrule.StateOK, "2026-01-01T00:00:20Z"))
	got, err = rules.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, alertrule.StateOK, got.State)

	// Delete 删行即净；缺行 ErrNotFound（幂等归调用面映射 404）。
	require.NoError(t, rules.Delete(ctx, r.ID))
	_, err = rules.Get(ctx, r.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	err = rules.Delete(ctx, r.ID)
	assert.ErrorIs(t, err, state.ErrNotFound, "deleting a missing row reports ErrNotFound")
}
