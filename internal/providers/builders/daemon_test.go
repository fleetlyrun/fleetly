package builders

// daemon 共享机械单测（收尾批 E27）：构建上下文 mount 组装的 VCS 排除面。
// 断言面 = 组装出的 fsutil.FS 的 Walk 视图——session 上传给 buildkit 的
// 就是这份 Walk 投影，fake/记录形态由此成立（Solve 本体是 daemon 集成测
// 试的职责，见 build_test.go 的 env-gated 真机面）。

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildContextExcludesTopLevelVCSMetadata 钉 E27：上下文根的
// .git/.svn/.hg 不进构建上下文（COPY . . 不再烙入 git 历史）；嵌套 VCS
// 目录与普通文件原样保留（裁决=只排顶层 VCS 元数据，不越权用户域）。
func TestBuildContextExcludesTopLevelVCSMetadata(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	write(".git/HEAD", "ref: refs/heads/main")
	write(".git/objects/ab/cdef", "object")
	write(".svn/entries", "12")
	write(".hg/store/00manifest.i", "store")
	write("app.txt", "hello")
	write("sub/.git/nested-payload", "user domain")
	write("sub/main.go", "package main")

	f, err := newContextFS(dir)
	require.NoError(t, err)

	var paths []string
	require.NoError(t, f.Walk(context.Background(), "/", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d != nil && !d.IsDir() {
			paths = append(paths, filepath.ToSlash(path)) // fsutil Walk 产出 OS 分隔符（Windows），归一后断言
		}
		return nil
	}))
	sort.Strings(paths)

	assert.Equal(t, []string{
		"app.txt",
		"sub/.git/nested-payload", // 嵌套 VCS 目录保留（顶层限定裁决）
		"sub/main.go",
	}, paths, "top-level VCS metadata must be excluded from the build context; nested content stays")
}
