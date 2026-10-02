package localobjectstore

import (
	"context"
	"io"
	"strings"
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
