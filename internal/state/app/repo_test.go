package app_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newApp(id, projectID, name string) *app.App {
	return &app.App{ID: id, ProjectID: projectID, Name: name}
}

// ADR-0026 List 分页（after_* + limit）：id 升序（既有响应序）、游标跳过、
// limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）；tombstone 不入分页面。
func TestAppListByProjectPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	apps := app.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	// 逆序落三行（ULID 时间序与落序相反，验证 ORDER BY id ASC）+ 异项目一行对照。
	rows := []*app.App{
		newApp("01JD0APPS0000000000000000003", projA, "worker"),
		newApp("01JD0APPS0000000000000000002", projA, "api"),
		newApp("01JD0APPS0000000000000000009", projB, "other"),
		newApp("01JD0APPS0000000000000000001", projA, "web"),
	}
	for _, a := range rows {
		require.NoError(t, apps.Create(ctx, db.Runner(), a))
	}

	// 升序首页截断。
	page1, err := apps.ListByProjectPage(ctx, db.Runner(), projA, "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, rows[3].ID, page1[0].ID, "ascending ULID creation order")
	assert.Equal(t, rows[1].ID, page1[1].ID)

	// 游标跳过首页。
	page2, err := apps.ListByProjectPage(ctx, db.Runner(), projA, page1[len(page1)-1].ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, rows[0].ID, page2[0].ID)

	// 游标越过末条 → 空页；tombstone 不入分页面。
	page3, err := apps.ListByProjectPage(ctx, db.Runner(), projA, "01JD0APPS000000000000000000Z", 2)
	require.NoError(t, err)
	assert.Empty(t, page3)
	require.NoError(t, apps.SoftDelete(ctx, db.Runner(), rows[0].ID))
	got, err := apps.ListByProjectPage(ctx, db.Runner(), projA, "", 0)
	require.NoError(t, err)
	assert.Len(t, got, 2, "deleted rows stay hidden")

	// limit 钳制：<=0 回落缺省 50（两行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -8, 800} {
		got, err := apps.ListByProjectPage(ctx, db.Runner(), projA, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 2, "limit %d clamps into range and returns all rows", limit)
	}
}
