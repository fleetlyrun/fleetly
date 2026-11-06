package pubsub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainpubsub "github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// pubsubs 聚合 repo 冒烟（B4b，ADR-0096 决策 1：行面与 databases 同形——
// 此处钉同形性不重放全矩阵；行为等价由 database repo_test 的范式承载）。
func TestPubSubRepoSmoke(t *testing.T) {
	db, _ := statetest.New(t)
	repo := New(db.Clock(), db.Runner())
	ctx := context.Background()

	row := &domainpubsub.PubSub{
		ProjectID: "prj", Name: "events", Engine: "nats", Version: "2.11",
		CredentialsRef: "pubsub:" + "events",
		Settings:       map[string]string{"jetstream_max_file": "1gb"},
	}
	require.NoError(t, repo.Create(ctx, row))
	assert.NotEmpty(t, row.ID)
	assert.Equal(t, domainpubsub.StatusPending, row.Status)

	// 同项目同名冲突（tombstone 后可复用）。
	dup := *row
	dup.ID = ""
	err := repo.Create(ctx, &dup)
	require.ErrorIs(t, err, state.ErrAlreadyExists)

	got, err := repo.Get(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, "nats", got.Engine)
	assert.Equal(t, map[string]string{"jetstream_max_file": "1gb"}, got.Settings)

	byName, err := repo.GetByName(ctx, "prj", "events")
	require.NoError(t, err)
	assert.Equal(t, row.ID, byName.ID)

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	n, err := repo.CountByProject(ctx, "prj")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// EnsureGeneration：变化推进 +1，指纹一致幂等重放同号。
	gen1, err := repo.EnsureGeneration(ctx, row.ID, "fp-1")
	require.NoError(t, err)
	gen1b, err := repo.EnsureGeneration(ctx, row.ID, "fp-1")
	require.NoError(t, err)
	assert.Equal(t, gen1, gen1b)
	gen2, err := repo.EnsureGeneration(ctx, row.ID, "fp-2")
	require.NoError(t, err)
	assert.Equal(t, gen1+1, gen2)

	// 期望态全量替换（settings/resources）。
	cpu := int32(500)
	require.NoError(t, repo.UpdateDesired(ctx, row.ID, nil, &cpu, nil))

	// 恢复挂起三态。
	require.NoError(t, repo.SetRestorePending(ctx, row.ID, "bk-1"))
	pending, err := repo.ListRestorePending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, repo.SetRestoreError(ctx, row.ID, "boom"))
	pending, err = repo.ListRestorePending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	got, _ = repo.Get(ctx, row.ID)
	assert.Equal(t, "boom", got.RestoreError)
	require.NoError(t, repo.SetRestorePending(ctx, row.ID, "bk-2"))
	require.NoError(t, repo.ClearRestoreSucceeded(ctx, row.ID))

	// 状态推进 + 调度锚。
	require.NoError(t, repo.SetStatus(ctx, row.ID, domainpubsub.StatusRunning))
	require.NoError(t, repo.SetLastBackupAt(ctx, row.ID, "2026-11-07T00:00:00Z"))

	// tombstone：幂等口径。
	require.NoError(t, repo.SoftDelete(ctx, row.ID))
	require.ErrorIs(t, repo.SoftDelete(ctx, row.ID), state.ErrNotFound)
	gone, err := repo.Get(ctx, row.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	assert.Nil(t, gone)

	// tombstone 后同名可新建（partial unique index 同 databases 形）。
	fresh := &domainpubsub.PubSub{
		ProjectID: "prj", Name: "events", Engine: "nats", Version: "2.10",
		CredentialsRef: "pubsub:" + "events",
	}
	require.NoError(t, repo.Create(ctx, fresh))
	assert.NotEqual(t, row.ID, fresh.ID, "ID 永不复用")
	_ = persistence.MapScanErr(nil)
}
