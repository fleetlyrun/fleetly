package project_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func TestProjectCRUD(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	projects := project.New(clock)

	p := &project.Project{ID: "01JD0PROJ00000000000000000", Name: "shop", TeamID: "default"}
	require.NoError(t, projects.Create(ctx, db.Runner(), p))
	assert.Equal(t, "2026-01-01T00:00:00Z", p.CreatedAt, "clock-injected timestamp")

	// 同 Team 同名冲突（ADR-0028 口径 (team_id, name)；唯一约束命中 →
	// ErrAlreadyExists）；跨 Team 同名并存合法。
	err := projects.Create(ctx, db.Runner(), &project.Project{ID: "01JD0PROJ00000000000000001", Name: "shop", TeamID: "default"})
	assert.ErrorIs(t, err, state.ErrAlreadyExists)
	require.NoError(t, projects.Create(ctx, db.Runner(), &project.Project{
		ID: "01JD0PROJ00000000000000002", Name: "shop", TeamID: "01JTEAM0000000000000000000",
	}))

	got, err := projects.GetByNameInTeam(ctx, db.Runner(), "default", "shop")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	_, err = projects.GetByNameInTeam(ctx, db.Runner(), "01JTEAM0000000000000000000", "shop")
	require.NoError(t, err, "同名项目跨 Team 并存，team 域内各自命中")

	// tombstone：软删后同名可复用、team 域查询不再命中、Get 直读仍在。
	require.NoError(t, projects.SoftDelete(ctx, db.Runner(), p.ID))
	_, err = projects.GetByNameInTeam(ctx, db.Runner(), "default", "shop")
	assert.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, projects.Create(ctx, db.Runner(), &project.Project{ID: "01JD0PROJ00000000000000003", Name: "shop", TeamID: "default"}))
	got, err = projects.Get(ctx, db.Runner(), p.ID)
	require.NoError(t, err)
	assert.True(t, got.Deleted())
}

func TestAppCRUD(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	apps := app.New(clock)

	const projectID = "01JD0PROJ00000000000000000"
	a := &app.App{ID: "01JD0APP000000000000000000", ProjectID: projectID, Name: "web"}
	require.NoError(t, apps.Create(ctx, db.Runner(), a))

	// 同 Project 同名冲突（唯一约束命中 → ErrAlreadyExists）；跨 Project
	// 同名合法（资源名只在 Project 内唯一）。
	err := apps.Create(ctx, db.Runner(), &app.App{ID: "01JD0APP000000000000000001", ProjectID: projectID, Name: "web"})
	assert.ErrorIs(t, err, state.ErrAlreadyExists)
	require.NoError(t, apps.Create(ctx, db.Runner(), &app.App{
		ID: "01JD0APP000000000000000002", ProjectID: "01JD0PROJ00000000000000009", Name: "web",
	}))

	list, err := apps.ListByProject(ctx, db.Runner(), projectID)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	require.NoError(t, apps.SoftDelete(ctx, db.Runner(), a.ID))
	list, err = apps.ListByProject(ctx, db.Runner(), projectID)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// ADR-0026 List 分页（after_* + limit）：全局（owner）与 Team 过滤两种
// 形态、游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestProjectListPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	projects := project.New(clock)

	// 逆序落三行（ULID 时间序与落序相反，验证 ORDER BY id ASC 既有序不变）。
	teamA, teamB := "01JTEAM000000000000000000A", "01JTEAM000000000000000000B"
	rows := []*project.Project{
		{ID: "01JD0PROJ00000000000000000C", Name: "gamma", TeamID: teamA},
		{ID: "01JD0PROJ00000000000000000A", Name: "alpha", TeamID: teamA},
		{ID: "01JD0PROJ00000000000000000B", Name: "beta", TeamID: teamB},
	}
	for _, p := range rows {
		require.NoError(t, projects.Create(ctx, db.Runner(), p))
	}

	// owner 全局面：id 升序（创建序）。
	page1, err := projects.ListPage(ctx, db.Runner(), "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, rows[1].ID, page1[0].ID, "ascending ULID creation order")
	assert.Equal(t, rows[2].ID, page1[1].ID)

	page2, err := projects.ListPage(ctx, db.Runner(), page1[len(page1)-1].ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, rows[0].ID, page2[0].ID, "cursor skips the first page")

	// Team 过滤与分页叠加（ADR-0035 List 面过滤不回退）。
	teamPage, err := projects.ListByTeamPage(ctx, db.Runner(), teamA, "", 1)
	require.NoError(t, err)
	require.Len(t, teamPage, 1)
	assert.Equal(t, rows[1].ID, teamPage[0].ID)
	teamPage2, err := projects.ListByTeamPage(ctx, db.Runner(), teamA, teamPage[0].ID, 1)
	require.NoError(t, err)
	require.Len(t, teamPage2, 1)
	assert.Equal(t, rows[0].ID, teamPage2[0].ID)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -3, 1000} {
		got, err := projects.ListPage(ctx, db.Runner(), "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}
}
