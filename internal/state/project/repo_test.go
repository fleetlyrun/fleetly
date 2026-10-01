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

	// 同名冲突（活跃唯一；唯一约束命中 → ErrAlreadyExists）。
	err := projects.Create(ctx, db.Runner(), &project.Project{ID: "01JD0PROJ00000000000000001", Name: "shop"})
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	got, err := projects.GetByName(ctx, db.Runner(), "shop")
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)

	// tombstone：软删后同名可复用、GetByName 不再命中、Get 直读仍在。
	require.NoError(t, projects.SoftDelete(ctx, db.Runner(), p.ID))
	_, err = projects.GetByName(ctx, db.Runner(), "shop")
	assert.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, projects.Create(ctx, db.Runner(), &project.Project{ID: "01JD0PROJ00000000000000002", Name: "shop"}))
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
