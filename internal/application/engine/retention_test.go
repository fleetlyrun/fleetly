package engine

// Revision 保留窗行为锚（ADR-0101）：冻结事务内裁剪（新行落账同事务清最旧
// 非活跃）、活跃集保护（驱动权行锚 ∪ 最近成功基线锚——本文件验基线锚；
// 驱动权行锚同集）、内容复用零新行不触发。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// freezeOnly 只冻结不部署（Submit=nil；冻结事务即保留窗裁剪的触发点）。
func freezeOnly(t *testing.T, e *Engine, ctx context.Context, appID, ref string) string {
	t.Helper()
	rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: appID, Spec: submitImageSpec(appID, ref, nil),
	})
	require.NoError(t, err)
	return rev.ID
}

// ① 冻结即裁剪：105 个不同内容 → 恰 100 行、最旧 5 行清、最新在列；
// 内容复用（同内容重冻结）零新行不触发——窗保持且复用既有行。
func TestSubmitSpecPrunesRetentionWindow(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	for i := 1; i <= 105; i++ {
		freezeOnly(t, e, ctx, tAppID, fmt.Sprintf("nginx:1.%d", i))
	}
	rows, err := e.repos.Revisions.ListByApp(ctx, tAppID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 100, "freeze transaction prunes to the retention window")
	assert.Equal(t, int64(6), rows[0].Seq, "oldest non-active rows are pruned first")
	assert.Equal(t, int64(105), rows[len(rows)-1].Seq, "newest revision survives")

	// 内容复用：零新行零裁剪，返回既有冻结体。
	reused := freezeOnly(t, e, ctx, tAppID, "nginx:1.105")
	rows2, err := e.repos.Revisions.ListByApp(ctx, tAppID, 0, 200)
	require.NoError(t, err)
	assert.Len(t, rows2, 100, "content reuse must not change the window")
	assert.Equal(t, rows[len(rows)-1].ID, reused, "identical spec reuses the frozen revision")
}

// ② 活跃集保护（软上限）：窗外行被最近成功基线锚住即存活——窗 100 +
// 保护行，绝不删在流量行；未保护行照清。
func TestSubmitSpecRetentionProtectsBaselineAnchor(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	// R1 冻结 + succeeded 基线（LatestSucceeded 的 to 锚受保护）。
	r1 := freezeOnly(t, e, ctx, tAppID, "nginx:baseline")
	d := &deployment.Deployment{AppID: tAppID, ToRevision: r1, State: deployment.StateQueued}
	require.NoError(t, e.repos.Deployments.Create(ctx, d))
	require.NoError(t, e.repos.Deployments.Transit(ctx, d.ID,
		[]deployment.State{deployment.StateQueued}, deployment.StateSucceeded,
		func(_ *deployment.Deployment) {}))
	// R2 冻结 + 终态 deployment 锚（不被保护——超窗后悬空，时间线仍可读：
	// to_revision 是不透明 ID 软引用，读径无 join 即无悬空面）。
	r2 := freezeOnly(t, e, ctx, tAppID, "nginx:dangling")
	d2 := &deployment.Deployment{AppID: tAppID, ToRevision: r2, State: deployment.StateQueued}
	require.NoError(t, e.repos.Deployments.Create(ctx, d2))
	require.NoError(t, e.repos.Deployments.Transit(ctx, d2.ID,
		[]deployment.State{deployment.StateQueued}, deployment.StateFailed,
		func(_ *deployment.Deployment) {}))

	// 超窗冻结 105：R1 在窗外（最新行 seq=106，窗 = seq 7..106）仍存活。
	for i := 1; i <= 105; i++ {
		freezeOnly(t, e, ctx, tAppID, fmt.Sprintf("nginx:p.%d", i))
	}
	rows, err := e.repos.Revisions.ListByApp(ctx, tAppID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 101, "retention window plus the protected anchor beyond it")
	assert.Equal(t, int64(1), rows[0].Seq, "protected baseline revision survives beyond the window")
	_, err = e.repos.Revisions.Get(ctx, r1)
	require.NoError(t, err)

	// 悬空时间线（ADR-0101 决策 3）：R2 已被清理，其 deployment 行仍在且
	// 可读——审计链不因 revision 清理断流；解析面（Get revision）= 404 由
	// 窗外回滚锚承载。
	_, err = e.repos.Revisions.Get(ctx, r2)
	require.ErrorIs(t, err, state.ErrNotFound, "r2 was pruned beyond the window")
	timeline, err := e.repos.Deployments.ListByApp(ctx, tAppID, 0, 50)
	require.NoError(t, err)
	found := false
	for _, row := range timeline {
		if row.ID == d2.ID && row.ToRevision == r2 {
			found = true
		}
	}
	assert.True(t, found, "deployment timeline survives with its dangling to_revision anchor")
}
