package localobjectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	info1, err := p.Put(ctx, k1, strings.NewReader("payload-1"))
	require.NoError(t, err)
	assert.Equal(t, capability.ObjectInfo{Key: k1, Size: 9, Digest: sha256Hex("payload-1")}, info1)
	_, err = p.Put(ctx, k2, strings.NewReader("payload-2"))
	require.NoError(t, err)

	// Stat：元数据回读（size/modtime；digest 不重算——端口契约）。
	st, err := p.Stat(ctx, k1)
	require.NoError(t, err)
	assert.Equal(t, k1, st.Key)
	assert.Equal(t, int64(9), st.Size)
	assert.False(t, st.ModTime.IsZero())
	assert.Empty(t, st.Digest)

	got, err := p.Get(ctx, k1)
	require.NoError(t, err)
	body, err := io.ReadAll(got)
	require.NoError(t, err)
	require.NoError(t, got.Close())
	assert.Equal(t, "payload-1", string(body))

	all, err := p.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, k1, all[0].Key)
	assert.Equal(t, int64(9), all[0].Size)
	assert.False(t, all[0].ModTime.IsZero())
	db1, err := p.List(ctx, "backups/shop/db1/")
	require.NoError(t, err)
	require.Len(t, db1, 1)
	assert.Equal(t, k1, db1[0].Key)

	require.NoError(t, p.Delete(ctx, k1))
	// 幂等删除：不存在不报错。
	require.NoError(t, p.Delete(ctx, k1))
	all, err = p.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, 1)

	_, err = p.Get(ctx, k1)
	assert.ErrorIs(t, err, capability.ErrObjectNotFound)
	_, err = p.Stat(ctx, k1)
	assert.ErrorIs(t, err, capability.ErrObjectNotFound)
}

// sha256Hex 铸期望摘要（回执校验的参照）。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Get 的 ctx 贯通（端口契约）：取消即断流——在途 Read 报错，不悬挂。
func TestGetCancelledMidStream(t *testing.T) {
	p := newProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	const k = "backups/shop/db1/big.archive"
	_, err := p.Put(context.Background(), k, strings.NewReader(strings.Repeat("x", 4096)))
	require.NoError(t, err)

	r, err := p.Get(ctx, k)
	require.NoError(t, err)
	buf := make([]byte, 16)
	_, err = r.Read(buf)
	require.NoError(t, err)
	cancel()
	// 看门狗关闭与在途 Read 有调度窗口：有界轮询断流生效（不悬挂）。
	deadline := time.Now().Add(2 * time.Second)
	for err == nil {
		if time.Now().After(deadline) {
			t.Fatal("read after cancel must fail, not hang")
		}
		_, err = r.Read(buf)
		time.Sleep(time.Millisecond)
	}
	_ = r.Close()
}

// Get 的入界取消：取消后新开读直接拒绝（不打开无用句柄）。
func TestGetCancelledBeforeOpen(t *testing.T) {
	p := newProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.Put(context.Background(), "backups/shop/db1/x.archive", strings.NewReader("x"))
	require.NoError(t, err)
	cancel()
	_, err = p.Get(ctx, "backups/shop/db1/x.archive")
	require.ErrorIs(t, err, context.Canceled)
}

// 键钳制：路径穿越/盘符/反斜杠/空段一律拒绝（端口不信任调用方）。
func TestKeySanitizationRejectsTraversal(t *testing.T) {
	p := newProvider(t)
	ctx := context.Background()
	for _, key := range []string{
		"../escape", "a/../../escape", "/absolute", "C:/win", `back\slash`,
		"a//b", "a/./b", "", ".",
	} {
		_, err := p.Put(ctx, key, strings.NewReader("x"))
		assert.Error(t, err, "key %q must be rejected", key)
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
		_, err := p.Put(ctx, k, strings.NewReader("x"))
		require.NoError(t, err)
	}
	all, err := p.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 4)
	allKeys := make([]string, len(all))
	for i := range all {
		allKeys[i] = all[i].Key
	}

	for _, prefix := range []string{"backups/shop/db1/", "backups/shop/db1", "backups/shop/", "backups"} {
		got, err := p.List(ctx, prefix)
		require.NoError(t, err, "prefix %q", prefix)
		norm := strings.TrimSuffix(prefix, "/") // List 尾斜杠归一（结果语义按归一后前缀）
		var want []string
		for _, k := range allKeys {
			if strings.HasPrefix(k, norm) {
				want = append(want, k)
			}
		}
		gotKeys := make([]string, len(got))
		for i := range got {
			gotKeys[i] = got[i].Key
		}
		assert.Equal(t, want, gotKeys, "pruned listing must equal the full-scan filter for prefix %q", prefix)
	}
}

// TestPutSyncFailureFailsWithoutLanding 钉 E30-1③：rename 前 fsync 失败 →
// Put 报错且对象不落位（备份落位承诺含数据落盘；错误路径不留 tmp）。
func TestPutSyncFailureFailsWithoutLanding(t *testing.T) {
	p := newProvider(t)
	prev := fileSync
	fileSync = func(*os.File) error { return errors.New("injected sync failure") }
	t.Cleanup(func() { fileSync = prev })

	_, err := p.Put(context.Background(), "backups/shop/db1/x.archive", strings.NewReader("x"))
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
