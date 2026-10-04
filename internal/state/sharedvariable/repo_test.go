package sharedvariable_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func newVar(id, projectID, name, value string) *sharedvariable.SharedVariable {
	return &sharedvariable.SharedVariable{ID: id, ProjectID: projectID, Name: name, Value: value}
}

const (
	projA = "01JD0PROJ0000000000000000A"
	projB = "01JD0PROJ0000000000000000B"
)

// upsert 语义（secrets 同款）：同名覆盖值、tombstone 行复活、时间戳推进。
func TestSharedVariableUpsertLifecycle(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	vars := sharedvariable.New(clock)

	row := newVar("01JD0SVAR00000000000000001", projA, "DATABASE_URL", "v1")
	require.NoError(t, vars.Upsert(ctx, db.Runner(), row))
	assert.Equal(t, "v1", row.Value)
	firstUpdated := row.UpdatedAt

	// 同名覆盖：ID 与 created_at 不变，值换新。
	clock.Advance(1e9)
	row2 := newVar("01JD0SVAR00000000000000002", projA, "DATABASE_URL", "v2")
	require.NoError(t, vars.Upsert(ctx, db.Runner(), row2))
	assert.Equal(t, row.ID, row2.ID, "upsert must reuse the existing row id")
	assert.Equal(t, "v2", row2.Value)
	assert.Greater(t, row2.UpdatedAt, firstUpdated)

	// 软删后 GetByName 404；同名复活清 tombstone（upsert 语义）。
	require.NoError(t, vars.SoftDelete(ctx, db.Runner(), projA, "DATABASE_URL"))
	_, err := vars.GetByName(ctx, db.Runner(), projA, "DATABASE_URL")
	require.ErrorIs(t, err, state.ErrNotFound)
	row3 := newVar("01JD0SVAR00000000000000003", projA, "DATABASE_URL", "v3")
	require.NoError(t, vars.Upsert(ctx, db.Runner(), row3))
	got, err := vars.GetByName(ctx, db.Runner(), projA, "DATABASE_URL")
	require.NoError(t, err)
	assert.Equal(t, "v3", got.Value)
	assert.False(t, got.Deleted())

	// 重复删除按 404 诚实上抛（幂等 tombstone 语义与 secrets 同款）。
	require.NoError(t, vars.SoftDelete(ctx, db.Runner(), projA, "DATABASE_URL"))
	require.ErrorIs(t, vars.SoftDelete(ctx, db.Runner(), projA, "DATABASE_URL"), state.ErrNotFound)
}

// ADR-0026 List 分页：name 字典序升序、after_name 游标、limit 钳制；项目
// 隔离（异项目行不可见）。
func TestSharedVariableListPage(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	vars := sharedvariable.New(clock)

	for i, n := range []string{"b-key", "a-key", "c-key"} {
		require.NoError(t, vars.Upsert(ctx, db.Runner(), newVar(
			"01JD0SVAR0000000000000000"+string(rune('1'+i)), projA, n, "v")))
	}
	require.NoError(t, vars.Upsert(ctx, db.Runner(), newVar("01JD0SVAR00000000000000009", projB, "z-key", "v")))

	all, err := vars.ListPage(ctx, db.Runner(), projA, "", 0)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []string{"a-key", "b-key", "c-key"}, []string{all[0].Name, all[1].Name, all[2].Name})

	page, err := vars.ListPage(ctx, db.Runner(), projA, "a-key", 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "b-key", page[0].Name)

	// limit 钳制：>200 钳 50（3 行全回）。
	big, err := vars.ListPage(ctx, db.Runner(), projA, "", 500)
	require.NoError(t, err)
	assert.Len(t, big, 3)

	// 合成装载面：ListActive 全量活跃行（tombstone 不见）。
	require.NoError(t, vars.SoftDelete(ctx, db.Runner(), projA, "b-key"))
	active, err := vars.ListActive(ctx, db.Runner(), projA)
	require.NoError(t, err)
	assert.Len(t, active, 2)
}
