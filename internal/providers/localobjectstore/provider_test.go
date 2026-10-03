package localobjectstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func newProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(t.TempDir())
	require.NoError(t, err)
	return p
}

func TestPutGetListDeleteRoundTrip(t *testing.T) {
	p := newProvider(t)
	ctx := context.Background()
	// 键时间戳不带冒号：Windows 控制面文件名不允许 ":"（F2 备份执行器
	// 铸键同口径）。
	const k1 = "backups/shop/db1/20261002T000000Z.archive"
	const k2 = "backups/shop/db2/20261002T000000Z.archive"

	require.NoError(t, p.Put(ctx, k1, strings.NewReader("payload-1")))
	require.NoError(t, p.Put(ctx, k2, strings.NewReader("payload-2")))

	got, err := p.Get(ctx, k1)
	require.NoError(t, err)
	body, err := io.ReadAll(got)
	require.NoError(t, err)
	require.NoError(t, got.Close())
	assert.Equal(t, "payload-1", string(body))

	all, err := p.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	db1, err := p.List(ctx, "backups/shop/db1/")
	require.NoError(t, err)
	assert.Equal(t, []string{k1}, db1)

	require.NoError(t, p.Delete(ctx, k1))
	// 幂等删除：不存在不报错。
	require.NoError(t, p.Delete(ctx, k1))
	all, err = p.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, 1)

	_, err = p.Get(ctx, k1)
	assert.Error(t, err)
}

// 键钳制：路径穿越/盘符/反斜杠/空段一律拒绝（端口不信任调用方）。
func TestKeySanitizationRejectsTraversal(t *testing.T) {
	p := newProvider(t)
	ctx := context.Background()
	for _, key := range []string{
		"../escape", "a/../../escape", "/absolute", "C:/win", `back\slash`,
		"a//b", "a/./b", "", ".",
	} {
		assert.Error(t, p.Put(ctx, key, strings.NewReader("x")), "key %q must be rejected", key)
	}
	// List 前缀同受钳制。
	_, err := p.List(ctx, "../escape")
	assert.Error(t, err)
}

// 健康探测：可写即健康；Describe 承载同机非灾备的诚实边界。
func TestHealthAndDescribeHonesty(t *testing.T) {
	p := newProvider(t)
	report := p.Health(context.Background())
	assert.True(t, report.Healthy)

	desc := p.Describe()
	assert.Equal(t, capability.KindObjectStore, desc.Capability)
	assert.Equal(t, "local", desc.Name)
	joined := strings.Join(desc.Notes, "\n")
	assert.Contains(t, joined, "NOT disaster recovery")
}

// TestProbeWritableConcurrent 钉 E30-1①：探测面并发进入不互删探测文件
// （唯一探测名）——修复前固定名 .probe 下并发方 Remove 撞 ErrNotExist 被
// 误判为不可写（假 unhealthy）。-race 下 32 并发全健康；不留探测残留。
func TestProbeWritableConcurrent(t *testing.T) {
	p := newProvider(t)
	var wg sync.WaitGroup
	errs := make([]error, 32)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = p.probeWritable(context.Background())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "concurrent probe %d must not spuriously fail", i)
	}
	entries, err := os.ReadDir(p.root)
	require.NoError(t, err)
	assert.Empty(t, entries, "probe must not leave residue in the backup root")
}

// TestDirMayContainPrefix 钉 E30-1②的剪枝判定（纯函数表测）：组件级比较
// ——db1 不误判进 db10 子树；祖先路径放行；无前缀全放行。
func TestDirMayContainPrefix(t *testing.T) {
	cases := []struct {
		dir, prefix string
		want        bool
	}{
		{"backups/shop", "backups/shop/db1", true},       // 祖先：必经路径
		{"backups/shop/db1", "backups/shop/db1", true},   // 前缀目录自身
		{"backups/shop/db1/x", "backups/shop/db1", true}, // 子树成员
		{"backups/shop/db10", "backups/shop/db1", false}, // 组件级分界：db10 ≠ db1/
		{"backups/other", "backups/shop/db1", false},     // 兄弟子树
		{"anything", "", true},                           // 无前缀 = 全根遍历
	}
	for _, c := range cases {
		assert.Equal(t, c.want, dirMayContain(c.dir, c.prefix), "dir %q prefix %q", c.dir, c.prefix)
	}
}

// TestListPrunedPrefixResultUnchanged 钉 E30-1②结果面：剪枝后按库列举与
// 全量列举的交集一致（含 db10 形态的字符串前缀边界——剪枝只动遍历量）。
func TestListPrunedPrefixResultUnchanged(t *testing.T) {
	p := newProvider(t)
	ctx := context.Background()
	keys := []string{
		"backups/shop/db1/20261002T000000Z.archive",
		"backups/shop/db2/20261002T000000Z.archive",
		"backups/shop/db10/20261002T000000Z.archive",
		"backups/other/db1/20261002T000000Z.archive",
	}
	for _, k := range keys {
		require.NoError(t, p.Put(ctx, k, strings.NewReader("x")))
	}
	all, err := p.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 4)

	for _, prefix := range []string{"backups/shop/db1/", "backups/shop/db1", "backups/shop/", "backups"} {
		got, err := p.List(ctx, prefix)
		require.NoError(t, err, "prefix %q", prefix)
		norm := strings.TrimSuffix(prefix, "/") // List 尾斜杠归一（结果语义按归一后前缀）
		var want []string
		for _, k := range all {
			if strings.HasPrefix(k, norm) {
				want = append(want, k)
			}
		}
		assert.Equal(t, want, got, "pruned listing must equal the full-scan filter for prefix %q", prefix)
	}
}

// TestPutSyncFailureFailsWithoutLanding 钉 E30-1③：rename 前 fsync 失败 →
// Put 报错且对象不落位（备份落位承诺含数据落盘；错误路径不留 tmp）。
func TestPutSyncFailureFailsWithoutLanding(t *testing.T) {
	p := newProvider(t)
	prev := fileSync
	fileSync = func(*os.File) error { return errors.New("injected sync failure") }
	t.Cleanup(func() { fileSync = prev })

	err := p.Put(context.Background(), "backups/shop/db1/x.archive", strings.NewReader("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected sync failure")

	_, err = p.Get(context.Background(), "backups/shop/db1/x.archive")
	assert.Error(t, err, "object must not land when the pre-rename sync fails")
	var files []string
	require.NoError(t, filepath.WalkDir(p.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}))
	assert.Empty(t, files, "failed put must not leave tmp/blob residue (empty dirs are fine)")
}
