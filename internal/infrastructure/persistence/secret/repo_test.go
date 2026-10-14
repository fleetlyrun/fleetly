package secret_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func newSecret(id, projectID, name string) *secret.Secret {
	return &secret.Secret{
		ID: id, ProjectID: projectID, Name: name,
		Ciphertext: []byte("age-envelope"), Fingerprint: "0123456789abcdef",
	}
}

// ADR-0026 List 分页（after_* + limit）：name 字典序升序（既有排序轴不变）、
// after_name 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）；
// 分页只动行集，每行仍是最新指纹面。
func TestSecretListFingerprintsPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	secrets := secret.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	// 字典序与落序交错落三行（projA）+ 异项目一行。
	names := []string{"api-token", "db-url", "api-token-json"}
	for i, n := range names {
		require.NoError(t, secrets.Upsert(ctx, db.Runner(), newSecret(
			"01JD0SECR0000000000000000"+string(rune('0'+i)), projA, n)))
	}
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), newSecret(
		"01JD0SECR00000000000000009", projB, "other")))

	// 升序首页截断：api-token < api-token-json < db-url。
	page1, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, "api-token", page1[0].Name)
	assert.Equal(t, "api-token-json", page1[1].Name)

	// 游标跳过首页。
	page2, err := secrets.ListFingerprints(ctx, db.Runner(), projA, page1[len(page1)-1].Name, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "db-url", page2[0].Name)

	// 游标越过末条 → 空页；tombstone 不入指纹面。
	page3, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "zzz", 2)
	require.NoError(t, err)
	assert.Empty(t, page3)
	require.NoError(t, secrets.SoftDelete(ctx, db.Runner(), projA, "db-url"))
	got, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", 0)
	require.NoError(t, err)
	assert.Len(t, got, 2, "deleted rows stay hidden")

	// limit 钳制：<=0 回落缺省 50（两行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -2, 250} {
		got, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 2, "limit %d clamps into range and returns all rows", limit)
	}
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

// TestSecretUpsertConcurrentSameNameConflict：并发同名 PUT 的交错窗口
// （GetByName 未命中 ↔ 对端已落活跃行）撞部分唯一索引时必须归一为
// ErrAlreadyExists（409 链在册），不得把裸 "constraint failed: UNIQUE"
// 上抛成 E_INTERNAL。写者 A 在未提交事务内先落同名活跃行（B 的读面
// 不可见），B 停在 INSERT 前等待——A 提交后放行，B 必然撞索引。
func TestSecretUpsertConcurrentSameNameConflict(t *testing.T) {
	ctx := context.Background()
	clock := statetest.NewFakeClock()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	dbA, err := state.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbA.Close()) })
	dbB, err := state.Open(ctx, path, clock)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbB.Close()) })

	const projID = "01JD0PROJ0000000000000000A"
	rowReady := make(chan struct{})
	releaseA := make(chan struct{})
	txErr := make(chan error, 1)
	go func() {
		txErr <- dbA.Tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO secrets (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
				VALUES ('01JD0SECR0000000000000000R', 99, ?, 'raced', x'00', 'fp', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`, projID)
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
		upsertErr <- secret.New(clock).Upsert(ctx, gate, &secret.Secret{
			ProjectID: projID, Name: "raced",
			Ciphertext: []byte("ct"), Fingerprint: "0123456789abcdef",
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
	got, err := secret.New(clock).GetByName(ctx, dbB.Runner(), projID, "raced")
	require.NoError(t, err)
	assert.Equal(t, "01JD0SECR0000000000000000R", got.ID)
}
