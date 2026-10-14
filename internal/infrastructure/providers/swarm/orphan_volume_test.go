package swarm

// 匿名孤儿卷清扫单测（N2 评审 P2-4；fakeDaemon 的卷路由面）。判据五重
// 各钉一枚"不碰"形态，删除限额与空集 no-op 同批。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/moby/moby/api/types/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anonVolumeName 铸 64 位小写 hex 匿名卷名（判据形态面；tag 承载区分位，
// %064x 零填充即全 hex 小写形态）。
func anonVolumeName(tag int) string {
	return fmt.Sprintf("%064x", tag)
}

// seedVolume 预置一枚卷形态（created 零值 = 无出生事实——CreatedAt 字段
// 缺席即空串形态；labels nil = 无标记面；inUse = 容器引用事实）。
func seedVolume(f *fakeDaemon, name string, created time.Time, labels map[string]string, inUse bool) {
	birth := ""
	if !created.IsZero() {
		birth = created.Format(time.RFC3339Nano)
	}
	f.addVolume(volume.Volume{Name: name, CreatedAt: birth, Labels: labels}, inUse)
}

// TestSweepOrphanVolumes 钉 P2-4：匿名悬空超窗才删；命名卷/带 label 卷/
// 未超窗/在引用/无出生事实/大写 hex/集群卷全部不碰；dangling 过滤走
// daemon 服务端；删除有界；空集 no-op；非正预算 no-op。
func TestSweepOrphanVolumes(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-8 * 24 * time.Hour) // 越过 7d 年龄窗

	t.Run("reaps old dangling anonymous volumes only", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		victim := anonVolumeName(1)
		seedVolume(f, victim, old, nil, false) // 匿名悬空超窗：唯一删除面
		seedVolume(f, "torchwood-pg-data", old, nil, false)
		seedVolume(f, anonVolumeName(2), old, map[string]string{"com.example.keep": "1"}, false)
		seedVolume(f, anonVolumeName(3), time.Now().Add(-time.Hour), nil, false)
		seedVolume(f, anonVolumeName(4), old, nil, true)
		seedVolume(f, anonVolumeName(5), time.Time{}, nil, false)
		seedVolume(f, "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789", old, nil, false)
		cluster := volume.Volume{Name: anonVolumeName(6), CreatedAt: old.Format(time.RFC3339Nano)}
		cluster.ClusterVolume = &volume.ClusterVolume{}
		f.addVolume(cluster, false)

		n, err := p.SweepOrphanVolumes(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "only the old dangling anonymous volume is swept")
		// 字典序（ASCII）：'0' < 'A' < 't'——零填充 hex 名在前、大写形态居中、
		// 命名卷殿后。
		assert.Equal(t, []string{
			anonVolumeName(2), anonVolumeName(3), anonVolumeName(4), anonVolumeName(5), anonVolumeName(6),
			"ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
			"torchwood-pg-data",
		}, f.volumeNames(), "named/labeled/fresh/referenced/no-birth/uppercase/cluster volumes must survive")
	})

	t.Run("filters dangling server-side", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		seedVolume(f, anonVolumeName(1), old, nil, false)
		seedVolume(f, anonVolumeName(2), old, nil, true) // 在引用：daemon 侧就该出局
		n, err := p.SweepOrphanVolumes(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		qs := f.queriesOf("GET /volumes")
		require.Len(t, qs, 1, "one list call per sweep")
		assert.Contains(t, qs[0].Get("filters"), `"dangling"`, "dangling filter must be sent to the daemon")
		assert.Contains(t, qs[0].Get("filters"), `"true"`)
	})

	t.Run("caps deletions per call", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		for i := 1; i <= 3; i++ {
			seedVolume(f, anonVolumeName(i), old, nil, false)
		}
		n, err := p.SweepOrphanVolumes(ctx, 2)
		require.NoError(t, err)
		assert.Equal(t, 2, n, "delete budget caps one call (janitor tick rate limiting)")
		assert.Equal(t, []string{anonVolumeName(3)}, f.volumeNames(), "remaining orphan waits for the next tick")
	})

	t.Run("empty set is a no-op", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		n, err := p.SweepOrphanVolumes(ctx, 100)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Empty(t, f.volumeNames())
		assert.Equal(t, 1, f.count("GET /volumes"), "the sweep still lists once (empty candidate set)")
	})

	t.Run("non-positive budget is a no-op", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		seedVolume(f, anonVolumeName(1), old, nil, false)
		n, err := p.SweepOrphanVolumes(ctx, 0)
		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Equal(t, []string{anonVolumeName(1)}, f.volumeNames())
	})
}
