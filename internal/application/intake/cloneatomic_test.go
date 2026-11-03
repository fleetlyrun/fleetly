package intake

// git 上下文原子落位（R03-M2，S16-c8）：「目录存在」不再是完成判据——
// clone 先落 .cloning 临时目录，成功后原子 rename 到 contexts/<rev>；中断/
// 超时/失败只留临时目录（下次尝试先清），最终路径永不出现半截上下文被
// 后续拍误判完整。测试经 gitRun 执行缝注入中断形态（免网络/真仓）。
// P5 wave-5 拆包随域迁入（通则⑥，断言面零漂移）。

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
	fx := newFixture(t, nil)
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X1")
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "half-checkout"), []byte("partial"), 0o600))
		return []byte("fatal: the remote end hung up unexpectedly"), errors.New("exit status 128")
	})

	err := fx.in.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil)
	require.Error(t, err)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr),
		"an interrupted clone must not be published at the final context path (a later tick would treat it as complete)")
	_, statErr = os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the temporary clone directory must be cleaned up after a failed clone")
}

func TestCloneSourcePublishesCompleteContextAtomically(t *testing.T) {
	fx := newFixture(t, nil)
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X2")
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		assert.NotEqual(t, dir, dest, "the clone must land in the temporary directory, not the final path")
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte("FROM busybox"), 0o600))
		return nil, nil
	})

	require.NoError(t, fx.in.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil))
	b, err := os.ReadFile(filepath.Join(dir, "Dockerfile")) //nolint:gosec // 测试夹具读自身 TempDir 产物，路径非用户输入
	require.NoError(t, err, "a successful clone must be published at the final context path")
	assert.Equal(t, "FROM busybox", string(b))
	_, statErr := os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the temporary clone directory must be gone after publication")
}

func TestCloneSourceToleratesConcurrentWinner(t *testing.T) {
	fx := newFixture(t, nil)
	dir := filepath.Join(t.TempDir(), "contexts", "01JD0REV0000000000000000X3")
	// 胜者形态 = 完整落位的 clone（.git 在场）——容忍分支的判据（真实并发
	// 双方都经本函数 rename 发布，胜者目录必然带 .git）。
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "winner"), []byte("complete"), 0o600))
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(dest, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "loser"), []byte("dup"), 0o600))
		return nil, nil
	})

	require.NoError(t, fx.in.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil))
	b, err := os.ReadFile(filepath.Join(dir, "winner")) //nolint:gosec // 测试夹具读自身 TempDir 产物，路径非用户输入
	require.NoError(t, err, "the concurrently published context must be kept intact")
	assert.Equal(t, "complete", string(b))
	_, statErr := os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the losing clone's temporary directory must be cleaned up")
}

// TestCloneSourceRejectsPreexistingEmptyTarget 钉死容忍分支的诚实性：预创建
// 空目标（MkdirTemp 形态）不是已发布 clone——fresh clone 不得被吞，失败
// 必须浮出（hookpull 假成功实咬的负例：Go os.Rename 对目录→已存在目录不发
// syscall 直接 EEXIST，空壳曾骗过 os.Stat(dir) 容忍判据）。
func TestCloneSourceRejectsPreexistingEmptyTarget(t *testing.T) {
	fx := newFixture(t, nil)
	dir := filepath.Join(t.TempDir(), "hookpull-shell")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(filepath.Join(dest, "docker", "fleetly"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "docker", "fleetly", "docker-compose.yml"), []byte("services: {}"), 0o600))
		return nil, nil
	})

	err := fx.in.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil)
	require.Error(t, err, "an empty pre-created target is not a published clone — the failure must surface instead of swallowing the fresh clone")
	_, statErr := os.Stat(filepath.Join(dir, "docker"))
	assert.True(t, os.IsNotExist(statErr), "the clone must not be received into the empty shell (the publish failed honestly)")
	_, statErr = os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the fresh clone's temporary directory must be cleaned up on rejection")
}

// TestCloneSourcePublishesToYieldedStagingPath 钉死调用方配方（hookpull 形
// 态）：MkdirTemp 取唯一名后让位空壳，cloneSource 经 rename 落位成功、内容
// 完整可读。
func TestCloneSourcePublishesToYieldedStagingPath(t *testing.T) {
	fx := newFixture(t, nil)
	dir, err := os.MkdirTemp("", "hookpull-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Remove(dir), "the unique name must be yielded before cloneSource publishes (Go os.Rename refuses any existing directory target)")
	stubGitRun(t, func(args []string) ([]byte, error) {
		dest := cloneDest(args)
		require.NoError(t, os.MkdirAll(filepath.Join(dest, "docker", "fleetly"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "docker", "fleetly", "docker-compose.yml"), []byte("services: {}"), 0o600))
		return nil, nil
	})

	require.NoError(t, fx.in.cloneSource(context.Background(), "https://github.test/acme/shop.git", "main", dir, nil))
	b, rerr := os.ReadFile(filepath.Join(dir, "docker", "fleetly", "docker-compose.yml")) //nolint:gosec // 测试夹具读自身 TempDir 产物，路径非用户输入
	require.NoError(t, rerr, "the yielded staging path must receive the full clone")
	assert.Equal(t, "services: {}", string(b))
	_, statErr := os.Stat(dir + ".cloning")
	assert.True(t, os.IsNotExist(statErr), "the temporary clone directory must be gone after publication")
}
