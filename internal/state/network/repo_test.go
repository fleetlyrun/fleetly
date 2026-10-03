package networkrepo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newNetwork(id, projectID, name string) *networkrepo.Network {
	return &networkrepo.Network{ID: id, ProjectID: projectID, Name: name}
}

// ADR-0026 List 分页（after_* + limit）：name 字典序升序（既有排序轴不变）、
// after_name 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestNetworkListByProjectPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	networks := networkrepo.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	// 字典序与落序交错落三行（projA）+ 异项目一行对照。
	rows := []*networkrepo.Network{
		newNetwork("01JD0NETR000000000000000003", projA, "workers"),
		newNetwork("01JD0NETR000000000000000002", projA, "default"),
		newNetwork("01JD0NETR000000000000000001", projB, "other"),
		newNetwork("01JD0NETR000000000000000000", projA, "default-json"),
	}
	for _, n := range rows {
		require.NoError(t, networks.Create(ctx, db.Runner(), n))
	}

	// 升序首页截断：default < default-json < workers。
	page1, err := networks.ListByProjectPage(ctx, db.Runner(), projA, "", 1)
	require.NoError(t, err)
	require.Len(t, page1, 1)
	assert.Equal(t, "default", page1[0].Name)

	// 游标跳过首页。
	page2, err := networks.ListByProjectPage(ctx, db.Runner(), projA, page1[0].Name, 1)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "default-json", page2[0].Name)

	// 游标越过末条 → 空页。
	page3, err := networks.ListByProjectPage(ctx, db.Runner(), projA, "zzz", 1)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -6, 600} {
		got, err := networks.ListByProjectPage(ctx, db.Runner(), projA, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}

// 全量面（配额检查与引擎挂网装配消费）语义不变。
func TestNetworkListUnchanged(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	networks := networkrepo.New(clock)

	require.NoError(t, networks.Create(ctx, db.Runner(),
		newNetwork("01JD0NETR000000000000000001", "01JD0PROJ0000000000000000A", "default")))
	require.NoError(t, networks.Create(ctx, db.Runner(),
		newNetwork("01JD0NETR000000000000000000", "01JD0PROJ0000000000000000B", "bus")))

	got, err := networks.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, got, 2)

	byProj, err := networks.ListByProject(ctx, db.Runner(), "01JD0PROJ0000000000000000A")
	require.NoError(t, err)
	require.Len(t, byProj, 1)
	assert.Equal(t, "default", byProj[0].Name)

	_, err = networks.GetByName(ctx, db.Runner(), "01JD0PROJ0000000000000000A", "missing")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
