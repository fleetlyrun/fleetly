package browse_test

// Browse 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：回收台账四
// 方法——Create 落行铸造面（ID 由 database+seq 派生、read_only 布尔落整
// 形、created/expires 由调用方给定）、List seq 稳定序全量、CountByProject
// 项目隔离计数（quota 判定输入）、Delete 回收收口（缺行也是幂等成功——
// 会话非资源行无 NotFound 面）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	browserepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/browse"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	projA = "01JD0PROJ0000000000000000A"
	projB = "01JD0PROJ0000000000000000B"
)

// TestBrowseSessionLedgerLifecycle：落行 → 全量列表 → 项目计数 → 回收删行。
func TestBrowseSessionLedgerLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	sessions := browserepo.New(clock)

	// 首见落行：ID 由 database+seq 派生；行不承载凭证，时间由受理方给定。
	s1 := &browse.Session{
		ProjectID: projA, DatabaseID: "db-9", Engine: "postgres",
		ReadOnly: true, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
	}
	require.NoError(t, sessions.Create(ctx, db.Runner(), s1))
	assert.Equal(t, "db-9-s1", s1.ID, "default id derives from database id and seq")
	assert.Equal(t, int64(1), s1.Seq)

	// 第二行（异项目、读写形态）+ 第三行（同项目第二库）。
	s2 := &browse.Session{
		ProjectID: projB, DatabaseID: "db-8", Engine: "mysql",
		ReadOnly: false, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
	}
	require.NoError(t, sessions.Create(ctx, db.Runner(), s2))
	s3 := &browse.Session{
		ProjectID: projA, DatabaseID: "db-7", Engine: "postgres",
		ReadOnly: true, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
	}
	require.NoError(t, sessions.Create(ctx, db.Runner(), s3))

	// List：seq 稳定序全量行，布尔面读回。
	list, err := sessions.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, s1.ID, list[0].ID)
	assert.Equal(t, s2.ID, list[1].ID)
	assert.Equal(t, s3.ID, list[2].ID)
	assert.True(t, list[0].ReadOnly)
	assert.False(t, list[1].ReadOnly)
	assert.Equal(t, "mysql", list[1].Engine)
	assert.Equal(t, "2026-01-01T01:00:00Z", list[0].ExpiresAt)

	// CountByProject：项目隔离计数。
	n, err := sessions.CountByProject(ctx, db.Runner(), projA)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	n, err = sessions.CountByProject(ctx, db.Runner(), projB)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = sessions.CountByProject(ctx, db.Runner(), "01JD0PROJ0000000000000000C")
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Delete：回收收口即删行；再删/删缺行都是幂等成功（无 NotFound 面）。
	require.NoError(t, sessions.Delete(ctx, db.Runner(), s1.ID))
	list, err = sessions.List(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, list, 2)
	n, err = sessions.CountByProject(ctx, db.Runner(), projA)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "deleted sessions leave the quota count")
	require.NoError(t, sessions.Delete(ctx, db.Runner(), s1.ID),
		"deleting an already-recycled session is an idempotent success")
	require.NoError(t, sessions.Delete(ctx, db.Runner(), "missing-id"))
}
