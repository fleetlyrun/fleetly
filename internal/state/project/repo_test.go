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
