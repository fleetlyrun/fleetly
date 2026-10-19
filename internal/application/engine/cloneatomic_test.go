package engine

// git 上下文原子落位（R03-M2，S16-c8）：「目录存在」不再是完成判据——
// clone 先落 .cloning 临时目录，成功后原子 rename 到 contexts/<rev>；中断/
// 超时/失败只留临时目录（下次尝试先清），最终路径永不出现半截上下文被后
// 续拍误判完整。测试经 gitRun 执行缝注入中断形态（免网络/真仓）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGitRun 替换 git 执行缝（t.Cleanup 还原；本包测试无 t.Parallel，替换
// 面不与并发用例交叉）。
func stubGitRun(t *testing.T, fn func(args []string) ([]byte, error)) {
	t.Helper()
	orig := gitRun
	t.Cleanup(func() { gitRun = orig })
	gitRun = func(_ context.Context, args ...string) ([]byte, error) { return fn(args) }
}

// cloneDest 是 clone 子命令的落点（argv 末位——gitCloneArgs 的 -- 之后）。
func cloneDest(args []string) string { return args[len(args)-1] }

func TestCloneSourceDiscardsInterruptedClone(t *testing.T) {
	e, _ := newBuildEngine(t, &fakeBuilder{digest: "sha256:x"})
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X1")
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "half-checkout"), []byte("partial"), 0o600))
		return []byte("fatal: the remote end hung up unexpectedly"), errors.New("exit status 128")
	})

	err := e.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil)
	require.Error(t, err)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr),
		"an interrupted clone must not be published at the final context path (a later tick would treat it as complete)")
	_, statErr = os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the temporary clone directory must be cleaned up after a failed clone")
}

func TestCloneSourcePublishesCompleteContextAtomically(t *testing.T) {
	e, _ := newBuildEngine(t, &fakeBuilder{digest: "sha256:x"})
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X2")
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		assert.NotEqual(t, dir, dest, "the clone must land in the temporary directory, not the final path")
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte("FROM busybox"), 0o600))
		return nil, nil
	})

	require.NoError(t, e.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil))
	b, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err, "a successful clone must be published at the final context path")
	assert.Equal(t, "FROM busybox", string(b))
	_, statErr := os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the temporary clone directory must be gone after publication")
}

func TestCloneSourceToleratesConcurrentWinner(t *testing.T) {
	e, _ := newBuildEngine(t, &fakeBuilder{digest: "sha256:x"})
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X3")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "winner"), []byte("complete"), 0o600))
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "loser"), []byte("dup"), 0o600))
		return nil, nil
	})

	require.NoError(t, e.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil))
	b, err := os.ReadFile(filepath.Join(dir, "winner"))
	require.NoError(t, err, "the concurrently published context must be kept intact")
	assert.Equal(t, "complete", string(b))
	_, statErr := os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the losing clone's temporary directory must be cleaned up")
}
