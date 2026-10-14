package githubapp_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func newApp(projectID, login string) *githubapp.App {
	return &githubapp.App{
		ProjectID: projectID, AppID: 182345, InstallationID: 51790921,
		AccountLogin: login, APIBase: "",
		PrivateKeyCiphertext: []byte("age-envelope"), PrivateKeyFingerprint: "0123456789abcdef",
	}
}

// 双作用域唯一性（ADR-0073 决策 2）：Global 按属主唯一、Project 按域内
// 属主唯一（partial unique index），Upsert 覆盖同键活跃行（私钥轮换 =
// 重放 put）；跨态同名互不冲突。
func TestUpsertScopeUniqueness(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	apps := githubapp.New(clock)

	// Global 行落位；同属主重放 = 覆盖更新（id/seq 不变，私钥换新）。
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("", "acme")))
	first, err := apps.GetGlobalByOwner(ctx, db.Runner(), "acme")
	require.NoError(t, err)
	rotated := newApp("", "acme")
	rotated.PrivateKeyCiphertext = []byte("age-envelope-2")
	rotated.PrivateKeyFingerprint = "ffffffffffffffff"
	require.NoError(t, apps.Upsert(ctx, db.Runner(), rotated))
	again, err := apps.GetGlobalByOwner(ctx, db.Runner(), "acme")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, "ffffffffffffffff", again.PrivateKeyFingerprint)
	assert.Equal(t, first.Seq, again.Seq, "upsert keeps the original creation seq")

	// 直插绕过 Upsert：Global 唯一索引兜底。
	rawDup := func(id string, projectID string) error {
		return db.Tx(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `
				INSERT INTO github_apps (id, seq, project_id, app_id, installation_id, account_login,
					api_base, private_key_ciphertext, private_key_fingerprint, created_at, updated_at, deleted_at)
				VALUES (?, 4242, ?, 1, 2, 'acme', '', x'00', 'ff', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`,
				id, projectID)
			return e
		})
	}
	require.Error(t, rawDup("ghapp-9999", ""), "global (owner) uniqueness must hold")

	// Project 行与 Global 同属主互不冲突（异态异键）。
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("proj-a", "acme")))
	projRow, err := apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-a", "acme")
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, projRow.ID)

	// Project 域内同属主第二行插不进（partial unique 兜底）。
	require.Error(t, rawDup("ghapp-8888", "proj-a"), "project (scope, owner) uniqueness must hold")

	// 异属主同 Project 落新行。
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("proj-a", "giant-robots")))
}

// 构建期解析的遮蔽律（ADR-0073 决策 1）：Project 级命中优先于 Global；
// 域内未命中回落 Global；两态都未命中 = ErrNotFound（匿名 clone 的判定
// 由调用方做）。
func TestResolutionShadowing(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	apps := githubapp.New(clock)
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("", "acme")))
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("proj-a", "acme")))
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("", "giant-robots")))

	// Project 级命中（遮蔽键存在）；Global 行独立可查。
	projHit, err := apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-a", "acme")
	require.NoError(t, err)
	assert.Equal(t, "proj-a", projHit.ProjectID)
	globHit, err := apps.GetGlobalByOwner(ctx, db.Runner(), "acme")
	require.NoError(t, err)
	assert.Equal(t, "", globHit.ProjectID)
	fallback, err := apps.GetGlobalByOwner(ctx, db.Runner(), "giant-robots")
	require.NoError(t, err)
	assert.Equal(t, int64(51790921), fallback.InstallationID)

	// 未命中精确 ErrNotFound。
	_, err = apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-b", "acme")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-a", "nobody")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// 回显契约：List 族密文 SQL 层 NULL 投影（值永不出现），软删行不入面；
// ListAll 全量含墓碑含密文（rewrap 专用面）。
func TestListFacesAndTombstones(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	apps := githubapp.New(clock)
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("proj-a", "acme")))
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("proj-a", "giant-robots")))
	require.NoError(t, apps.Upsert(ctx, db.Runner(), newApp("", "giant-robots")))

	projRows, err := apps.ListByProject(ctx, db.Runner(), "proj-a")
	require.NoError(t, err)
	require.Len(t, projRows, 2)
	assert.Equal(t, "acme", projRows[0].AccountLogin, "ordered by login")
	assert.Nil(t, projRows[0].PrivateKeyCiphertext, "read faces never carry ciphertext")
	assert.Equal(t, "0123456789abcdef", projRows[0].PrivateKeyFingerprint)

	globRows, err := apps.ListGlobal(ctx, db.Runner())
	require.NoError(t, err)
	require.Len(t, globRows, 1)
	assert.Nil(t, globRows[0].PrivateKeyCiphertext)

	// 软删：回显面隐藏、解析面隐藏、ListAll 仍在（墓碑密文可解前提）。
	row, err := apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-a", "acme")
	require.NoError(t, err)
	require.NoError(t, apps.SoftDelete(ctx, db.Runner(), row.ID))
	_, err = apps.GetByProjectAndOwner(ctx, db.Runner(), "proj-a", "acme")
	assert.ErrorIs(t, err, state.ErrNotFound)
	projRows, err = apps.ListByProject(ctx, db.Runner(), "proj-a")
	require.NoError(t, err)
	assert.Len(t, projRows, 1)
	all, err := apps.ListAll(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, all, 3)
	assert.NotNil(t, all[0].PrivateKeyCiphertext)

	// 幂等软删 = nil；未知 id = ErrNotFound。
	assert.NoError(t, apps.SoftDelete(ctx, db.Runner(), row.ID))
	assert.ErrorIs(t, apps.SoftDelete(ctx, db.Runner(), "ghapp-404"), state.ErrNotFound)
}
