package sharedvariable_test

import (
	"context"
	"database/sql"
	"path/filepath"
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

// insertGateRunner 把首个 ExecContext（Upsert 的 INSERT 臂）闸在 channel
// 后：测试在写者 B 已过读面（GetByName 必然未命中——A 的行未提交）后
// 才放行，确定性复现并发同名 PUT 的交错窗口，不依赖调度时序。
type insertGateRunner struct {
	state.Runner
	insertWaiting chan struct{}
	proceed       chan struct{}
}

func (g *insertGateRunner) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	select {
	case <-g.insertWaiting:
	default:
		close(g.insertWaiting)
	}
	<-g.proceed
	return g.Runner.ExecContext(ctx, q, args...)
}

// TestSharedVariableUpsertConcurrentSameNameConflict：并发同名 PUT 的
// 交错窗口（GetByName 未命中 ↔ 对端已落活跃行）撞部分唯一索引时必须
// 归一为 ErrAlreadyExists（409 链在册），不得把裸 "constraint failed:
// UNIQUE" 上抛成 E_INTERNAL。写者 A 在未提交事务内先落同名活跃行（B 的
// 读面不可见），B 停在 INSERT 前等待——A 提交后放行，B 必然撞索引。
func TestSharedVariableUpsertConcurrentSameNameConflict(t *testing.T) {
	ctx := context.Background()
	clock := statertest.NewFakeClock()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	dbA, err := state.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbA.Close()) })
	dbB, err := state.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbB.Close()) })

	rowReady := make(chan struct{})
	releaseA := make(chan struct{})
	txErr := make(chan error, 1)
	go func() {
		txErr <- dbA.Tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO shared_variables (id, seq, project_id, name, value, created_at, updated_at, deleted_at)
				VALUES ('01JD0SHVR000000000000000R', 99, ?, 'raced', 'a-value', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`, projA)
			close(rowReady) // 落行即放行（失败亦放行——主流程在 txErr 处暴露）
			if err != nil {
				return err
			}
			<-releaseA
			return nil
		})
	}()
	<-rowReady

	gate := &insertGateRunner{
		Runner: dbB.Runner(), insertWaiting: make(chan struct{}), proceed: make(chan struct{}),
	}
	upsertErr := make(chan error, 1)
	go func() {
		upsertErr <- sharedvariable.New(clock).Upsert(ctx, gate, &sharedvariable.SharedVariable{
			ProjectID: projA, Name: "raced", Value: "b-value",
		})
	}()
	select {
	case <-gate.insertWaiting: // B 已过读面、停在 INSERT 前
	case err := <-upsertErr:
		t.Fatalf("B returned before reaching the INSERT arm: %v", err)
	}
	close(releaseA) // A 提交 → 同名活跃行对 B 可见
	require.NoError(t, <-txErr)
	close(gate.proceed) // 放行 B 的 INSERT → 必撞部分唯一索引

	err = <-upsertErr
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"concurrent same-name PUT must normalize the unique violation, got: %v", err)

	// 落库行仍是 A 的那颗（B 的 INSERT 失败即整体无效果）。
	got, err := sharedvariable.New(clock).GetByName(ctx, dbB.Runner(), projA, "raced")
	require.NoError(t, err)
	assert.Equal(t, "a-value", got.Value)
}
