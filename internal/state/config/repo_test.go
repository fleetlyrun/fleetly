package configrepo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// ADR-0026 List 分页（after_* + limit）：name 字典序升序（既有排序轴不变）、
// after_name 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）；
// 分页只动行集，每行仍是该 name 最新版。
func TestConfigLatestByProjectPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	configs := configrepo.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	put := func(projectID, name, content string) {
		require.NoError(t, configs.Create(ctx, db.Runner(), &configrepo.Config{
			ID: "01JD0CONF0000000000000000" + content, ProjectID: projectID, Name: name,
			Content: []byte(content),
		}))
	}
	// 多版本 + 字典序与落序交错；projB 一行对照。
	put(projA, "web.conf", "0") // v1
	put(projA, "web.conf", "1") // v2（最新）
	put(projA, "app.ini", "2")  // v1
	put(projB, "other", "3")

	// 升序首页截断：app.ini < web.conf；每行是最新版。
	page1, err := configs.LatestByProjectPage(ctx, db.Runner(), projA, "", 1)
	require.NoError(t, err)
	require.Len(t, page1, 1)
	assert.Equal(t, "app.ini", page1[0].Name)
	assert.Equal(t, int64(1), page1[0].Version)

	// 游标跳过。
	page2, err := configs.LatestByProjectPage(ctx, db.Runner(), projA, page1[0].Name, 1)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "web.conf", page2[0].Name)
	assert.Equal(t, int64(2), page2[0].Version, "latest version per name, row shape unchanged")

	// 游标越过末条 → 空页。
	page3, err := configs.LatestByProjectPage(ctx, db.Runner(), projA, "zzz", 1)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（两行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -4, 400} {
		got, err := configs.LatestByProjectPage(ctx, db.Runner(), projA, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 2, "limit %d clamps into range and returns all rows", limit)
	}

	// 全量面（配额执法消费）语义不变。
	all, err := configs.LatestByProject(ctx, db.Runner(), projA)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// 版本号单调（NextVersion）与指定版本回读（分页面之外的行形态锚）。
func TestConfigVersions(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	configs := configrepo.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	for i := 0; i < 2; i++ {
		require.NoError(t, configs.Create(ctx, db.Runner(), &configrepo.Config{
			ID: "01JD0CONF0000000000000000" + string(rune('0'+i)), ProjectID: projA,
			Name: "app.ini", Content: []byte{byte('a' + i)},
		}))
	}
	latest, err := configs.Latest(ctx, db.Runner(), projA, "app.ini")
	require.NoError(t, err)
	assert.Equal(t, int64(2), latest.Version)

	v1, err := configs.GetVersion(ctx, db.Runner(), projA, "app.ini", 1)
	require.NoError(t, err)
	assert.Equal(t, []byte("a"), v1.Content)

	versions, err := configs.ListVersions(ctx, db.Runner(), projA, "app.ini")
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	_, err = configs.Latest(ctx, db.Runner(), projA, "missing")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
