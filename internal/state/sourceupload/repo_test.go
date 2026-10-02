package sourceupload_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func newUpload(id, projectID, digest string, size int64) *sourceupload.Upload {
	return &sourceupload.Upload{ID: id, ProjectID: projectID, Digest: digest, SizeBytes: size}
}

func TestUploadLifecycle(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	repo := sourceupload.New(clock)

	u := newUpload("01JDUPLD00000000000000000A", "01JD0PROJ00000000000000000", "aa11", 100)
	require.NoError(t, repo.Create(ctx, db.Runner(), u))
	assert.NotEmpty(t, u.CreatedAt)

	// 同 (project, digest) 二次落行 → 唯一索引拒；跨项目同 digest 合法
	//（共享 blob 的 refcount 形态）。
	err := repo.Create(ctx, db.Runner(), newUpload("01JDUPLD00000000000000000B", u.ProjectID, u.Digest, 100))
	assert.ErrorIs(t, err, state.ErrAlreadyExists)
	require.NoError(t, repo.Create(ctx, db.Runner(), newUpload("01JDUPLD00000000000000000C", "01JD0PROJ00000000000000009", u.Digest, 100)))

	got, err := repo.Get(ctx, db.Runner(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, u.Digest, got.Digest)

	byDigest, err := repo.FindByDigest(ctx, db.Runner(), u.ProjectID, u.Digest)
	require.NoError(t, err)
	assert.Equal(t, u.ID, byDigest.ID)
	_, err = repo.FindByDigest(ctx, db.Runner(), u.ProjectID, "bb22")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 存量求和与 refcount。
	sum, err := repo.BytesByProject(ctx, db.Runner(), u.ProjectID)
	require.NoError(t, err)
	assert.EqualValues(t, 100, sum)
	n, err := repo.CountByDigest(ctx, db.Runner(), u.Digest)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	// List：新→旧 + after 游标。
	clock.Advance(time.Minute)
	require.NoError(t, repo.Create(ctx, db.Runner(), newUpload("01JDUPLD00000000000000000D", u.ProjectID, "cc33", 50)))
	list, err := repo.ListByProject(ctx, db.Runner(), u.ProjectID, "", 50)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "01JDUPLD00000000000000000D", list[0].ID)
	list, err = repo.ListByProject(ctx, db.Runner(), u.ProjectID, list[0].ID, 50)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, u.ID, list[0].ID)
}

func TestSweepUnreferenced(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	repo := sourceupload.New(clock)

	proj := "01JD0PROJ00000000000000000"
	// 未引用行（过期）与引用行（Revision spec 内含 id 字面量——即便行龄
	// 过窗也不清扫）。
	stale := newUpload("01JDUPLD00000000000000000A", proj, "aa11", 100)
	pinned := newUpload("01JDUPLD00000000000000000B", proj, "bb22", 200)
	require.NoError(t, repo.Create(ctx, db.Runner(), stale))
	require.NoError(t, repo.Create(ctx, db.Runner(), pinned))
	_, err := db.Runner().ExecContext(ctx,
		"INSERT INTO revisions (id, app_id, seq, digest, spec, created_at) VALUES (?, ?, 1, 'dd44', ?, '2026-01-01T00:00:00Z')",
		"01JDREVS00000000000000000A", "01JD0APP000000000000000000", `{"source":{"upload":{"id":"`+pinned.ID+`"}}}`)
	require.NoError(t, err)

	clock.Advance(8 * 24 * time.Hour) // 过 7d 保留窗
	got, err := repo.SweepUnreferenced(ctx, db.Runner(), "2026-01-05T00:00:00Z")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, stale.ID, got[0].ID)
	require.NoError(t, repo.Delete(ctx, db.Runner(), stale.ID))
	_, err = repo.Get(ctx, db.Runner(), stale.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = repo.Get(ctx, db.Runner(), pinned.ID)
	require.NoError(t, err)
}
