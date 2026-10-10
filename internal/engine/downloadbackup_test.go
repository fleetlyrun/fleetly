package engine

// DownloadBackup 单测（IA v3 二期⑤）：只读流与台账校验——内容字节一致、
// 归属错配/非 succeeded/未装配对象仓均精确拒绝（API 授权链在其上）。

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
)

func TestDownloadBackupStreamsObjectBytes(t *testing.T) {
	e, _, _, store, _ := newBackupFixture(t, "postgres", "postgres://u:p@db:5432/app")
	const payload = "PGDUMP-BYTES-0123456789"
	const backupID = "01JD0BKP000000000000000002"
	seedSucceededBackup(t, e, store, backupID, "postgres", payload)

	b, reader, err := e.DownloadBackup(context.Background(), tDatabaseID, backupID)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	assert.Equal(t, backupID, b.ID)

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, payload, string(data))
}

func TestDownloadBackupPreciseRejections(t *testing.T) {
	e, _, _, store, _ := newBackupFixture(t, "postgres", "postgres://u:p@db:5432/app")
	const backupID = "01JD0BKP000000000000000003"
	seedSucceededBackup(t, e, store, backupID, "postgres", "payload")

	// 归属错配按 not found（不泄漏跨库存在性）。
	_, _, err := e.DownloadBackup(context.Background(), "01JD0DBWRONG0000000000001", backupID)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 非成功台账行不可下载。
	_, _, err = e.DownloadBackup(context.Background(), tDatabaseID, "01JD0BKPMISSING00000001")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 对象仓未装配 = 精确失败。
	bare := New(Deps{DB: e.db, Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{})
	_, _, err = bare.DownloadBackup(context.Background(), tDatabaseID, backupID)
	assert.Contains(t, err.Error(), "object store is not assembled")
}
