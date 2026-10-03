package node_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// ADR-0026 List 分页（after_* + limit）。nodes 是观测缓存表，无 ULID 主键
// ——游标轴 = 既有排序轴 platform_id 字典序升序（节点 ID 永不复用，轴稳
// 定）：游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestNodeListPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	nodes := node.New(clock)

	// 字典序与落序交错落三行。
	rows := []*node.Node{
		{PlatformID: "node-c", CarrierID: "carrier-c", Hostname: "c", Role: "worker"},
		{PlatformID: "node-a", CarrierID: "carrier-a", Hostname: "a", Role: "manager"},
		{PlatformID: "node-b", CarrierID: "carrier-b", Hostname: "b", Role: "worker"},
	}
	for _, n := range rows {
		require.NoError(t, nodes.Upsert(ctx, db.Runner(), n))
	}

	// 升序首页截断（platform_id 字典序）。
	page1, err := nodes.ListPage(ctx, db.Runner(), "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, "node-a", page1[0].PlatformID)
	assert.Equal(t, "node-b", page1[1].PlatformID)

	// 游标跳过首页。
	page2, err := nodes.ListPage(ctx, db.Runner(), page1[len(page1)-1].PlatformID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "node-c", page2[0].PlatformID)

	// 游标越过末条 → 空页。
	page3, err := nodes.ListPage(ctx, db.Runner(), "zzz", 2)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -1, 300} {
		got, err := nodes.ListPage(ctx, db.Runner(), "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}

	// Upsert 刷新不换行（观测缓存语义）。
	require.NoError(t, nodes.Upsert(ctx, db.Runner(), &node.Node{
		PlatformID: "node-a", CarrierID: "carrier-a", Hostname: "a2", Role: "worker",
	}))
	fresh, err := nodes.ListPage(ctx, db.Runner(), "", 50)
	require.NoError(t, err)
	assert.Len(t, fresh, 3, "upsert refreshes in place, no new row")

	got, err := nodes.Get(ctx, db.Runner(), "node-a")
	require.NoError(t, err)
	assert.Equal(t, "a2", got.Hostname)

	_, err = nodes.Get(ctx, db.Runner(), "missing")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
