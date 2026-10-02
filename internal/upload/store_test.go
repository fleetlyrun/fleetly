package upload_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/upload"
)

func TestStoreCommitDedupAndDigest(t *testing.T) {
	root := t.TempDir()
	s := upload.NewStore(root, 0, 0)

	recv, err := s.Begin()
	require.NoError(t, err)
	content := []byte("hello tar bytes")
	require.NoError(t, recv.Write(content))
	digest, size := recv.Digest()
	sum := sha256.Sum256(content)
	assert.Equal(t, hex.EncodeToString(sum[:]), digest)
	assert.EqualValues(t, len(content), size)

	dedup, err := s.Commit(recv)
	require.NoError(t, err)
	assert.False(t, dedup)
	assert.True(t, s.BlobExists(digest))

	// 同内容二次接收 → dedup=true，tmp 不残留。
	recv2, err := s.Begin()
	require.NoError(t, err)
	require.NoError(t, recv2.Write(content))
	dedup, err = s.Commit(recv2)
	require.NoError(t, err)
	assert.True(t, dedup)

	entries, err := os.ReadDir(filepath.Join(root, "uploads"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "content-addressed store keeps exactly one blob per digest")
	assert.Equal(t, digest, entries[0].Name())
}

func TestStoreSizeLimit(t *testing.T) {
	root := t.TempDir()
	s := upload.NewStore(root, 8, 0)
	recv, err := s.Begin()
	require.NoError(t, err)
	require.NoError(t, recv.Write([]byte("12345678")))
	err = recv.Write([]byte("9"))
	var tooLarge *upload.ErrTooLarge
	require.ErrorAs(t, err, &tooLarge)
	assert.EqualValues(t, 8, tooLarge.Limit)
	assert.EqualValues(t, 9, tooLarge.Received)
	recv.Abort()
	assert.NoFileExists(t, recv.FilePath())
}

func TestSweepOrphanTmp(t *testing.T) {
	root := t.TempDir()
	s := upload.NewStore(root, 0, 0)
	dir := filepath.Join(root, "uploads")
	require.NoError(t, os.MkdirAll(dir, 0o750))

	old, err := os.Create(filepath.Join(dir, "tmp-stale")) //nolint:gosec // 测试自有夹具目录
	require.NoError(t, err)
	require.NoError(t, old.Close())
	past := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "tmp-stale"), past, past))

	fresh, err := os.Create(filepath.Join(dir, "tmp-live")) //nolint:gosec // 测试自有夹具目录
	require.NoError(t, err)
	require.NoError(t, fresh.Close())

	blob, err := os.Create(filepath.Join(dir, "aa11bb22")) //nolint:gosec // 测试自有夹具目录
	require.NoError(t, err)
	require.NoError(t, blob.Close())
	require.NoError(t, os.Chtimes(filepath.Join(dir, "aa11bb22"), past, past))

	n, err := s.SweepOrphanTmp(1 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.NoFileExists(t, filepath.Join(dir, "tmp-stale"))
	assert.FileExists(t, filepath.Join(dir, "tmp-live"), "in-flight staging files are not touched")
	assert.FileExists(t, filepath.Join(dir, "aa11bb22"), "blobs are never touched by the tmp sweep")
}
