package route_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newRoute(id, projectID, host string) *route.Route {
	return &route.Route{
		ID: id, ProjectID: projectID, Host: host, Path: "",
		AppID: "01JD0APP000000000000000000", Process: "web", Port: 8080,
		Protocol: "http", TLSMode: "auto",
	}
}

// ADR-0026 List 分页（after_* + limit）：id 升序（既有响应序）、游标跳过、
// limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）；project 过滤下推后与
// 游标叠加，过滤语义与全量面一致。
func TestRouteListPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	routes := route.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	// 逆序落四行（ULID 时间序与落序相反）；projA 三行 + projB 一行。
	rows := []*route.Route{
		newRoute("01JD0ROUTE00000000000000000D", projB, "d.example.test"),
		newRoute("01JD0ROUTE00000000000000000C", projA, "c.example.test"),
		newRoute("01JD0ROUTE00000000000000000B", projA, "b.example.test"),
		newRoute("01JD0ROUTE00000000000000000A", projA, "a.example.test"),
	}
	for _, rt := range rows {
		require.NoError(t, routes.Create(ctx, db.Runner(), rt))
	}

	// 全量分页（owner 面）：id 升序。
	page1, err := routes.ListPage(ctx, db.Runner(), "", "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, rows[3].ID, page1[0].ID, "ascending ULID creation order")
	assert.Equal(t, rows[2].ID, page1[1].ID)

	page2, err := routes.ListPage(ctx, db.Runner(), "", page1[len(page1)-1].ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, rows[1].ID, page2[0].ID, "cursor skips the first page")

	// project 过滤与分页叠加：只回 projA 的行，序保持。
	projPage, err := routes.ListPage(ctx, db.Runner(), projA, "", 2)
	require.NoError(t, err)
	require.Len(t, projPage, 2)
	assert.Equal(t, rows[3].ID, projPage[0].ID)
	assert.Equal(t, rows[2].ID, projPage[1].ID)
	projPage2, err := routes.ListPage(ctx, db.Runner(), projA, projPage[1].ID, 2)
	require.NoError(t, err)
	require.Len(t, projPage2, 1)
	assert.Equal(t, rows[1].ID, projPage2[0].ID)

	// tombstone 不入分页面。
	require.NoError(t, routes.SoftDelete(ctx, db.Runner(), rows[0].ID))
	got, err := routes.ListPage(ctx, db.Runner(), "", "", 0)
	require.NoError(t, err)
	assert.Len(t, got, 3, "deleted rows stay hidden")

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -9, 900} {
		got, err := routes.ListPage(ctx, db.Runner(), "", "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}

// 全量发布面（引擎 Proxy 发布消费）语义不变：含全部活跃行、id 升序。
func TestRouteListUnchanged(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	routes := route.New(clock)

	require.NoError(t, routes.Create(ctx, db.Runner(),
		newRoute("01JD0ROUTE00000000000000000B", "01JD0PROJ0000000000000000A", "b.example.test")))
	require.NoError(t, routes.Create(ctx, db.Runner(),
		newRoute("01JD0ROUTE00000000000000000A", "01JD0PROJ0000000000000000B", "a.example.test")))

	got, err := routes.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "01JD0ROUTE00000000000000000A", got[0].ID)
	assert.Equal(t, "01JD0ROUTE00000000000000000B", got[1].ID)
}
