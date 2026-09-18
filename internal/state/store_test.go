package state

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestStore 在临时目录打开已迁移的空库（每个测试独立文件，互不干扰）。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
