package appvariable_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	appvariablerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/appvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// seqGateRunner 把 seq_counters 的首次 QueryRowContext（Upsert 的 seq
// 铸造）闸在 channel 后：B 已过读面（GetByName 必然未命中——A 的行未
// 提交）但尚未取写锁，测试据此在 A 提交后才放行铸造+INSERT，确定性复现
// 并发同名 PUT 的交错窗口（seq_counters 是写语句，不能像旧 MAX 读那样
// 在 A 持写锁时穿过）。
type seqGateRunner struct {
	state.Runner
	seqWaiting chan struct{}
	proceed    chan struct{}
}

func (g *seqGateRunner) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if strings.Contains(q, "seq_counters") {
		select {
		case <-g.seqWaiting:
		default:
			close(g.seqWaiting)
		}
		<-g.proceed
	}
	return g.Runner.QueryRowContext(ctx, q, args...)
}

// TestAppVariableUpsertConcurrentSameNameConflict：并发同名 PUT 的交错
// 窗口（GetByName 未命中 ↔ 对端已落活跃行）撞部分唯一索引时必须归一为
// ErrAlreadyExists（409 链在册），不得把裸 "constraint failed: UNIQUE"
// 上抛成 E_INTERNAL。写者 A 在未提交事务内先落同名活跃行（B 的读面
// 不可见），B 停在 INSERT 前等待——A 提交后放行，B 必然撞索引。
func TestAppVariableUpsertConcurrentSameNameConflict(t *testing.T) {
	ctx := context.Background()
	clock := statetest.NewFakeClock()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	dbA, err := persistence.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbA.Close()) })
	dbB, err := persistence.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbB.Close()) })

	const appID = "01JD0APP00000000000000000A"
	statetest.SeedApp(t, dbA, appID, "project-appvariable")
	rowReady := make(chan struct{})
	releaseA := make(chan struct{})
	txErr := make(chan error, 1)
	go func() {
		txErr <- dbA.Tx(ctx, func(run state.Runner) error {
			_, err := run.ExecContext(ctx, `
				INSERT INTO app_variables (id, seq, app_id, name, value, created_at, updated_at, deleted_at)
				VALUES ('01JD0APVR000000000000000R', 99, ?, 'raced', 'a-value', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`, appID)
			close(rowReady) // 落行即放行（失败亦放行——主流程在 txErr 处暴露）
			if err != nil {
				return err
			}
			<-releaseA
			return nil
		})
	}()
	<-rowReady

	gate := &seqGateRunner{
		Runner: dbB.Runner(), seqWaiting: make(chan struct{}), proceed: make(chan struct{}),
	}
	upsertErr := make(chan error, 1)
	go func() {
		upsertErr <- appvariablerepo.New(clock, gate).Upsert(ctx, &appvariable.AppVariable{
			AppID: appID, Name: "raced", Value: "b-value",
		})
	}()
	select {
	case <-gate.seqWaiting: // B 已过读面、停在 seq 铸造前
	case err := <-upsertErr:
		t.Fatalf("B returned before minting seq: %v", err)
	}
	close(releaseA) // A 提交 → 同名活跃行对 B 可见
	require.NoError(t, <-txErr)
	close(gate.proceed) // 放行 B 的铸造+INSERT → 必撞部分唯一索引

	err = <-upsertErr
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"concurrent same-name PUT must normalize the unique violation, got: %v", err)

	// 落库行仍是 A 的那颗（B 的 INSERT 失败即整体无效果）。
	got, err := appvariablerepo.New(clock, dbA.Runner()).GetByName(ctx, appID, "raced")
	require.NoError(t, err)
	assert.Equal(t, "a-value", got.Value)
}
