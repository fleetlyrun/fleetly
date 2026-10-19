package node_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// countingRunner 统计写语句落笔次数（P0-7.4 断言面：空闲对账零写）。
type countingRunner struct {
	state.Runner
	writes int
}

func (c *countingRunner) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	c.writes++
	return c.Runner.ExecContext(ctx, q, args...)
}

// ADR-0026 List 分页（after_* + limit）。nodes 是观测缓存表，无 ULID 主键
// ——游标轴 = 既有排序轴 platform_id 字典序升序（节点 ID 永不复用，轴稳
// 定）：游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）。
func TestNodeListPagePagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	nodes := noderepo.New(clock, db.Runner())

	// 字典序与落序交错落三行。
	rows := []*node.Node{
		{PlatformID: "node-c", CarrierID: "carrier-c", Hostname: "c", Role: "worker"},
		{PlatformID: "node-a", CarrierID: "carrier-a", Hostname: "a", Role: "manager"},
		{PlatformID: "node-b", CarrierID: "carrier-b", Hostname: "b", Role: "worker"},
	}
	for _, n := range rows {
		require.NoError(t, nodes.Upsert(ctx, n))
	}

	// 升序首页截断（seq 锚定序 = 落序）。
	page1, err := nodes.ListPage(ctx, 0, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, "node-c", page1[0].PlatformID)
	assert.Equal(t, "node-a", page1[1].PlatformID)

	// 游标跳过首页。
	page2, err := nodes.ListPage(ctx, page1[len(page1)-1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "node-b", page2[0].PlatformID)

	// 游标越过末条 → 空页。
	page3, err := nodes.ListPage(ctx, 999999, 2)
	require.NoError(t, err)
	assert.Empty(t, page3)

	// limit 钳制：<=0 回落缺省 50（三行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -1, 300} {
		got, err := nodes.ListPage(ctx, 0, limit)
		require.NoError(t, err)
		assert.Len(t, got, 3, "limit %d clamps into range and returns all rows", limit)
	}

	// Upsert 刷新不换行（观测缓存语义）。
	require.NoError(t, nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-a", CarrierID: "carrier-a", Hostname: "a2", Role: "worker",
	}))
	fresh, err := nodes.ListPage(ctx, 0, 50)
	require.NoError(t, err)
	assert.Len(t, fresh, 3, "upsert refreshes in place, no new row")

	got, err := nodes.Get(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "a2", got.Hostname)

	_, err = nodes.Get(ctx, "missing")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestUpsertWriteGuard（P0-7.4）：观测缓存 Upsert 只在值变化或 last_seen
// 超过刷新窗才落笔——每秒一拍的对账在 30s 窗内对未变节点零写（空闲 WAL
// churn 止血：2 节点 ~17 万写/天 → 窗内 0）；首见落行、值变化即时写、
// 窗过期 last_seen 刷新三个语义都在。
func TestUpsertWriteGuard(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	run := &countingRunner{Runner: db.Runner()}
	nodes := noderepo.New(clock, run)

	first := &node.Node{PlatformID: "node-1", CarrierID: "c-1", Hostname: "h", Role: "worker", Available: true}
	require.NoError(t, nodes.Upsert(ctx, first))
	firstWrites := run.writes
	require.Positive(t, firstWrites)
	row, err := nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.Equal(t, row.FirstSeenAt, row.LastSeenAt)

	for i := 0; i < 5; i++ {
		clock.Advance(time.Second)
		require.NoError(t, nodes.Upsert(ctx, &node.Node{
			PlatformID: "node-1", CarrierID: "c-1", Hostname: "h", Role: "worker", Available: true,
		}))
	}
	assert.Equal(t, firstWrites, run.writes, "unchanged observation inside the refresh window must not write")

	clock.Advance(time.Second)
	require.NoError(t, nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-1", CarrierID: "c-1", Hostname: "h", Role: "worker", Available: false,
	}))
	assert.Greater(t, run.writes, firstWrites, "value change must write immediately")
	row, err = nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.False(t, row.Available)
	assert.Equal(t, first.FirstSeenAt, row.FirstSeenAt)

	before := run.writes
	clock.Advance(31 * time.Second)
	require.NoError(t, nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-1", CarrierID: "c-1", Hostname: "h", Role: "worker", Available: false,
	}))
	assert.Greater(t, run.writes, before, "stale last_seen must refresh")
	row, err = nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.Equal(t, state.FormatTime(clock.Now()), row.LastSeenAt)
}

// TestRelayCredentialHashAtRest（H10 §6 验收锚）：库内只落 sha256——明文
// 查不到、表行是 64 hex 且不等于明文、全行扫描不含 fltrelay_ 前缀；
// UPSERT 重铸使旧 hash 永久失效；单节点空删幂等、全删清表。
func TestRelayCredentialHashAtRest(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	nodes := noderepo.New(clock, db.Runner())

	mat, err := node.NewRelayCredential()
	require.NoError(t, err)
	require.NotEmpty(t, mat.Secret)

	require.NoError(t, nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "node-a", CarrierID: "carrier-a", SHA256: mat.SHA256,
	}))

	// 明文不是可查询键（库里没有明文，也没有对应行）。
	_, err = nodes.RelayCredentialBySHA256(ctx, mat.Secret)
	assert.ErrorIs(t, err, state.ErrNotFound, "the plaintext must never be a query key")

	// hash 查得到，身份绑定轴如实回读。
	got, err := nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(mat.Secret))
	require.NoError(t, err)
	assert.Equal(t, "node-a", got.PlatformID)
	assert.Equal(t, "carrier-a", got.CarrierID)
	assert.Equal(t, mat.SHA256, got.SHA256)
	assert.NotEmpty(t, got.CreatedAt)

	// 表行形态：secret_sha256 为 64 hex，且全行扫描不含明文前缀。
	var platformID, carrierID, secretSHA, createdAt string
	require.NoError(t, db.Runner().QueryRowContext(ctx, `
		SELECT platform_id, carrier_id, secret_sha256, created_at
		FROM node_relay_credentials WHERE platform_id = 'node-a'`).
		Scan(&platformID, &carrierID, &secretSHA, &createdAt))
	assert.Len(t, secretSHA, 64, "stored material must be a sha256 hex digest")
	assert.Regexp(t, `^[0-9a-f]{64}$`, secretSHA)
	assert.NotEqual(t, mat.Secret, secretSHA)
	assert.NotEqual(t, mat.Secret, platformID)
	assert.NotEqual(t, mat.Secret, carrierID)
	assert.NotEqual(t, mat.Secret, createdAt)
	assert.NotContains(t, platformID+carrierID+secretSHA+createdAt, node.RelayCredentialPrefix,
		"no row column may carry the plaintext prefix")

	// 重铸 = UPSERT 替换：旧 hash 永久失效，新 hash 命中同一身份行。
	rotated, err := node.NewRelayCredential()
	require.NoError(t, err)
	require.NoError(t, nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "node-a", CarrierID: "carrier-a", SHA256: rotated.SHA256,
	}))
	_, err = nodes.RelayCredentialBySHA256(ctx, mat.SHA256)
	assert.ErrorIs(t, err, state.ErrNotFound, "reissue must invalidate the old hash at once")
	got, err = nodes.RelayCredentialBySHA256(ctx, rotated.SHA256)
	require.NoError(t, err)
	assert.Equal(t, "node-a", got.PlatformID)

	// 单节点空删幂等；删后 hash 不可查。
	require.NoError(t, nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "node-b", CarrierID: "carrier-b", SHA256: mustCredentialHash(t),
	}))
	deleted, err := nodes.DeleteRelayCredential(ctx, "node-b")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = nodes.DeleteRelayCredential(ctx, "node-b")
	require.NoError(t, err)
	assert.False(t, deleted, "deleting a missing credential is an idempotent no-op")

	// 全删清表。
	require.NoError(t, nodes.DeleteAllRelayCredentials(ctx))
	var n int
	require.NoError(t, db.Runner().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_relay_credentials`).Scan(&n))
	assert.Zero(t, n)
}

// mustCredentialHash 铸一枚仅用于库内行的 hash（材质生成不触库）。
func mustCredentialHash(t *testing.T) string {
	t.Helper()
	mat, err := node.NewRelayCredential()
	require.NoError(t, err)
	return mat.SHA256
}
