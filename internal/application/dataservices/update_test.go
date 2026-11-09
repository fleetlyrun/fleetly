package dataservices

// Update 动词双车道单测（B2d，ADR-0095 决策 4/5/6 验收锚）：
//
//   - static 车道：落行 + Kick + fake Runtime **即时**收到 Ensure 真实重发
//     且命令面含新值（ensure cache 签名第四段——不等 60s 兜底节拍）；
//   - dynamic 车道：同步热应用先行（utility 执行序 + ensure cache 记账）+ 未变
//     重放零扰动 + 失败行不动 + 强制 Forget + 移除键复位缺省；
//   - 重放门四断言：失配触发 / 先于 ensure cache 短路判定 / 崩溃窗回退（行值
//     拉回）/ pending 不重放 + 失败退避 + 恢复挂起不重放；
//   - Teardown 收口 applied ensure cache 与退避锚。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// pgUpdateURL 铸 postgres 夹具连接串（48 字符密码过渲染闸）。
func pgUpdateURL() string {
	return fmt.Sprintf("postgresql://fleetly:%s@db-%s:5432/fleetly", strings.Repeat("u", 48), strings.ToLower(tDatabaseID))
}

// settingsUtilityReqs 过滤 utility 调用流水中的 dynamic 热应用面
// （ID 前缀 dbsettings-；卷源标记门/轮换的调用被排除）。
func settingsUtilityReqs(ut *fakeUtility) []string {
	var argvs []string
	for _, r := range ut.requests() {
		if strings.HasPrefix(r.ID, "dbsettings-") {
			argvs = append(argvs, strings.Join(r.Argv, " "))
		}
	}
	return argvs
}

// TestUpdateStaticLaneRollsImmediately：static-only 变更 → 落行 + Kick +
// 下一拍 Ensure 真实重发（非 ensure cache 短路）且命令覆写面含新值（GUC 原生
// 字面）——ADR-0095 决策 4 验收锚「即时收到 Ensure 真实重发且材料含新值」
// （命令覆写面即 static 值的下发面；第四段缺失时本拍 sig 不变即被短路，
// 本断言即第四段的机械证明）。未变重放 = Kick 照发但 Ensure 短路命中。
func TestUpdateStaticLaneRollsImmediately(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ctx := context.Background()
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	require.Len(t, fx.rt.calls(), 1)
	assert.Nil(t, fx.rt.calls()[0].Spec["postgres"].Command, "no static knobs keep the image entrypoint (zero drift)")

	res, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"max_connections": "300"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"max_connections"}, res.StaticRolled)
	assert.Empty(t, res.DynamicApplied)
	assert.Equal(t, 1, fx.kicks, "static lane kicks the convergence loop")

	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	calls := fx.rt.calls()
	require.Len(t, calls, 2, "the memo fourth segment (static digest) must break the short circuit on the very next step — no 60s grace wait")
	w := calls[1].Spec["postgres"]
	assert.Equal(t, []string{"docker-entrypoint.sh", "postgres", "-c", "max_connections=300"}, w.Command,
		"static knobs ride the entrypoint as -c overrides with native GUC values")

	// 未变重放：Kick 照发（幂等），Ensure 短路命中（零扰动 no-op）。
	_, err = fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"max_connections": "300"}})
	require.NoError(t, err)
	assert.Equal(t, 2, fx.kicks)
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	assert.Len(t, fx.rt.calls(), 2, "an unchanged declaration replays as a no-op (memo hit)")

	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"max_connections": "300"}, row.Settings)
}

// TestUpdateDynamicLaneAppliesFirst：dynamic 变更 → 同步热应用先行
// （工具容器执行，ALTER SYSTEM + pg_reload_conf；GUC 原生字面）→ ensure cache
// 记账 → 落行；未变重放 = 摘要命中即零扰动（声明式幂等）。
func TestUpdateDynamicLaneAppliesFirst(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ut := utilityOf(t, fx)
	ctx := context.Background()
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)

	res, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"work_mem"}, res.DynamicApplied)
	assert.Empty(t, res.StaticRolled)

	reqs := settingsUtilityReqs(ut)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "ALTER SYSTEM SET work_mem = '64MB'")
	assert.Contains(t, reqs[0], "SELECT pg_reload_conf()")
	assert.Contains(t, reqs[0], "ON_ERROR_STOP=1")

	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"work_mem": "64mb"}, row.Settings)
	_, remembered := fx.appliedMemo[tDatabaseID]
	assert.True(t, remembered, "the applied-digest memo is written after a successful apply")

	// 未变重放：ensure cache 摘要命中 → 不再执行工具容器。
	res, err = fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
	require.NoError(t, err)
	assert.Empty(t, res.DynamicApplied, "an unchanged dynamic declaration replays as a no-op")
	assert.Len(t, settingsUtilityReqs(ut), 1)
}

// TestUpdateDynamicFailureLeavesRow：热应用被拒 = ErrSettingsApplyRejected
// （API map E_SETTINGS_APPLY_FAILED）+ 行不动 + ensure cache 强制 Forget（半应用
// 黑洞修，ADR-0095 决策 6）+ 不 Kick。
func TestUpdateDynamicFailureLeavesRow(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ut := utilityOf(t, fx)
	ut.execErr = errors.New("psql: FATAL: terminating connection due to administrator command")
	ctx := context.Background()

	_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
	require.ErrorIs(t, err, ErrSettingsApplyRejected)
	assert.Contains(t, err.Error(), "boom-detail", "the engine's own reply tail rides the error")

	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, row.Settings, "a failed apply leaves the row untouched")
	assert.NotContains(t, fx.appliedMemo, tDatabaseID, "a failed apply forces the applied-digest memo to be forgotten")
	assert.Equal(t, 0, fx.kicks)
}

// TestUpdateMixedLanes：混合变更一次受理、两车道各自执行——dynamic 键
// 走工具容器（先行）、static 键走落行 + Kick 滚动；喂入律：dynamic 键
// 结构性不进 Workload 命令面。
func TestUpdateMixedLanes(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ut := utilityOf(t, fx)
	ctx := context.Background()
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)

	res, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{
		"shared_buffers": "256mb", "work_mem": "32mb",
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"work_mem"}, res.DynamicApplied)
	assert.Equal(t, []string{"shared_buffers"}, res.StaticRolled)

	reqs := settingsUtilityReqs(ut)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], "work_mem = '32MB'")
	assert.NotContains(t, reqs[0], "shared_buffers", "static knobs never reach the hot-apply face")

	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	calls := fx.rt.calls()
	require.Len(t, calls, 2)
	cmd := strings.Join(calls[1].Spec["postgres"].Command, " ")
	assert.Contains(t, cmd, "shared_buffers=256MB")
	assert.NotContains(t, cmd, "work_mem", "dynamic knobs never enter the Workload command face (feed law)")
}

// TestUpdateDynamicRemovalResetsDefault：期望态移除 dynamic 键 = 以
// Knob.Default 复位热应用（引擎自持久面不随行删除回退——不复位即行与
// 引擎永久分叉且无 ensure cache 失配可触发收敛）；成功后期望态已无 dynamic 面
// → ensure cache Forget 归位。
func TestUpdateDynamicRemovalResetsDefault(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ut := utilityOf(t, fx)
	ctx := context.Background()

	_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
	require.NoError(t, err)
	require.Len(t, settingsUtilityReqs(ut), 1)
	require.Contains(t, fx.appliedMemo, tDatabaseID)

	// 全量替换语义：空 map = 清空全部旋钮 → work_mem 以缺省 4mb 复位。
	res, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"work_mem"}, res.DynamicApplied, "the removal renders as a default reset through the same apply face")

	reqs := settingsUtilityReqs(ut)
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[1], "work_mem = '4MB'")

	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, row.Settings)
	assert.NotContains(t, fx.appliedMemo, tDatabaseID, "an empty dynamic face forgets the memo (no stale mismatch source)")
}

// TestReplayDynamicSettingsGate：重放门四断言（B2d 验收锚，ADR-0095
// 决策 6）+ 退避 + 恢复挂起。
func TestReplayDynamicSettingsGate(t *testing.T) {
	ctx := context.Background()

	t.Run("mismatch replays before the memo short circuit", func(t *testing.T) {
		fx := newFixture(t, "postgres", pgUpdateURL(), true)
		ut := utilityOf(t, fx)
		require.NoError(t, fx.stores.Databases.SetStatus(ctx, tDatabaseID, database.StatusRunning))
		_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
		require.NoError(t, err)
		require.Len(t, settingsUtilityReqs(ut), 1)
		fx.d.Step(ctx, dataservicetemplate.KindDatabase)
		require.Len(t, fx.rt.calls(), 1, "the static signature is unchanged, so the Ensure memo stays fresh")

		// 进程重启形态：applied ensure cache 丢失（表本体随进程丢失——in-memory）。
		delete(fx.appliedMemo, tDatabaseID)
		fx.d.Step(ctx, dataservicetemplate.KindDatabase)
		assert.Len(t, settingsUtilityReqs(ut), 2,
			"the digest mismatch triggers a replay even while the Ensure memo is fresh — the replay gate runs before the short-circuit check")
		assert.Len(t, fx.rt.calls(), 1, "the idempotent replay must not re-Ensure (zero disturbance)")
		require.Contains(t, fx.appliedMemo, tDatabaseID, "a successful replay re-records the digest")
	})

	t.Run("pending rows never replay", func(t *testing.T) {
		fx := newFixture(t, "postgres", pgUpdateURL(), true)
		ut := utilityOf(t, fx)
		_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
		require.NoError(t, err)
		require.Len(t, settingsUtilityReqs(ut), 1)
		// 行仍 pending（观测未到 running）：失配在场也不重放。
		delete(fx.appliedMemo, tDatabaseID)
		fx.d.Step(ctx, dataservicetemplate.KindDatabase)
		assert.Empty(t, settingsUtilityReqs(ut)[1:], "a pending row does not replay (first-beat/StartPeriod churn guard, red team A7)")
	})

	t.Run("restore-holding rows never replay", func(t *testing.T) {
		fx := newFixture(t, "postgres", pgUpdateURL(), true)
		ut := utilityOf(t, fx)
		_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
		require.NoError(t, err)
		delete(fx.appliedMemo, tDatabaseID)
		require.NoError(t, fx.stores.Databases.SetStatus(ctx, tDatabaseID, database.StatusRunning))
		require.NoError(t, fx.stores.Databases.SetRestorePending(ctx, tDatabaseID, "bk-01JD0BK00000000000000000"))
		fx.d.Step(ctx, dataservicetemplate.KindDatabase)
		assert.Empty(t, settingsUtilityReqs(ut)[1:], "a restore-holding row does not replay (restorePass in flight)")
	})

	t.Run("crash window rolls back to the row value", func(t *testing.T) {
		fx := newFixture(t, "postgres", pgUpdateURL(), true)
		ut := utilityOf(t, fx)
		// 崩溃窗形态：apply 64mb 成功（ensure cache 已记）后行未落库——行仍是 32mb
		//（恢复后以行值为期望态幂等重放拉回，ADR-0095 决策 6 崩溃窗语义）。
		_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "32mb"}})
		require.NoError(t, err)
		require.NoError(t, fx.stores.Databases.SetStatus(ctx, tDatabaseID, database.StatusRunning))
		fx.appliedMemo[tDatabaseID] = settingsDigest(map[string]string{"work_mem": "64mb"})
		fx.d.Step(ctx, dataservicetemplate.KindDatabase)
		reqs := settingsUtilityReqs(ut)
		require.Len(t, reqs, 2)
		assert.Contains(t, reqs[1], "work_mem = '32MB'",
			"the replay pulls the engine back to the row's declared value (the row is the single source of truth)")
		assert.Equal(t, settingsDigest(map[string]string{"work_mem": "32mb"}), fx.appliedMemo[tDatabaseID])
	})

	t.Run("failed replay backs off one window", func(t *testing.T) {
		fx := newFixture(t, "postgres", pgUpdateURL(), true)
		ut := utilityOf(t, fx)
		fakeClock, ok := fx.clock.(*statetest.FakeClock)
		require.True(t, ok, "the fixture must carry the advanceable fake clock")
		_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
		require.NoError(t, err)
		require.NoError(t, fx.stores.Databases.SetStatus(ctx, tDatabaseID, database.StatusRunning))
		delete(fx.appliedMemo, tDatabaseID)
		ut.execErr = errors.New("psql: FATAL: the database system is shutting down")

		fx.d.Step(ctx, dataservicetemplate.KindDatabase) // 失败：退避锚落时刻。
		require.Len(t, settingsUtilityReqs(ut), 2)
		assert.True(t, fx.d.replayInBackoff(tDatabaseID), "a failed replay parks the row in the backoff window")

		ut.execErr = nil
		fx.d.Step(ctx, dataservicetemplate.KindDatabase) // 窗内：不重试。
		assert.Len(t, settingsUtilityReqs(ut), 2, "no retry inside the backoff window (churn guard)")

		fakeClock.Advance(61 * time.Second)
		fx.d.Step(ctx, dataservicetemplate.KindDatabase) // 出窗：重试成功。
		assert.Len(t, settingsUtilityReqs(ut), 3, "past the window the replay retries and converges")
		assert.False(t, fx.d.replayInBackoff(tDatabaseID), "a successful replay clears the backoff anchor")
		require.Contains(t, fx.appliedMemo, tDatabaseID)
	})
}

// TestTeardownClearsAppliedMemo：域收口连清 applied ensure cache 与重放退避锚
// （同 ID 永不复用——防御性清理，与 ensure ensure cache 同轴）。
func TestTeardownClearsAppliedMemo(t *testing.T) {
	fx := newFixture(t, "postgres", pgUpdateURL(), true)
	ctx := context.Background()
	_, err := fx.d.UpdateDatabase(ctx, tDatabaseID, UpdateRequest{Settings: map[string]string{"work_mem": "64mb"}})
	require.NoError(t, err)
	fx.d.markReplayFailure(tDatabaseID)
	require.Contains(t, fx.appliedMemo, tDatabaseID)

	require.NoError(t, fx.d.TeardownDatabase(ctx, tDatabaseID))
	assert.NotContains(t, fx.appliedMemo, tDatabaseID, "teardown forgets the applied-digest memo along the database axis")
	assert.False(t, fx.d.replayInBackoff(tDatabaseID), "teardown clears the replay backoff anchor")
}
