package engine

// 受管域 Placement 钉住测试（F2.3 收口 F1.15 挂账）：受管 zot/proxy 带本地
// 卷无钉住——spec 变更滚动替换可把 task 漂到无卷节点 preparing 打转。
// 带卷受管 Workload 钉控制面节点（首个可用 manager），卷外 Workload 保持
// 无约束（调度自由）；锚不可解析 = 本拍整组不下发（下拍重试——漏钉住
// 比部署失败更糟，applyVolumePinning 的 Q-8 同款取舍）。
//
// 注：本测试独立成文件是 Windows 内容缓存坑的逃逸（编译器对同名文件读
// 旧字节——2026-10-04 会话实证；新文件名 = 新文件身份）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func TestManagedVolumeWorkloadPinnedToControlPlane(t *testing.T) {
	ctx := context.Background()

	t.Run("volume-bearing workload pins the manager, volumeless stays free", func(t *testing.T) {
		db, _ := statertest.New(t)
		rt := newFakeRuntime()
		proxy := &fakeProxy{ws: []capability.Workload{
			{ID: "fleetly-vol", Process: "vol", Image: "fake/vol:1", Replicas: 1,
				Volumes: []capability.VolumeMount{{VolumeID: "fleetly-data", Target: "/data"}}},
			{ID: "fleetly-novol", Process: "novol", Image: "fake/novol:1", Replicas: 1},
		}}
		e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
		e.managedStep(ctx)

		last := rt.calls()[len(rt.calls())-1]
		assert.Equal(t, []string{"01JD0NODE00000000000000000"}, last.Spec["vol"].Placement.NodeIDs,
			"a volume-bearing managed workload must pin to the control-plane node")
		assert.Empty(t, last.Spec["novol"].Placement.NodeIDs,
			"a volumeless managed workload keeps scheduling freedom")
	})

	t.Run("unresolvable control-plane anchor skips the whole pass", func(t *testing.T) {
		db, _ := statertest.New(t)
		rt := newFakeRuntime()
		rt.clusterOverride = true // 可编程视图（空 = 无可用 manager）
		proxy := &fakeProxy{ws: []capability.Workload{
			{ID: "fleetly-vol", Process: "vol", Image: "fake/vol:1", Replicas: 1,
				Volumes: []capability.VolumeMount{{VolumeID: "fleetly-data", Target: "/data"}}},
		}}
		e := New(Deps{DB: db, Runtime: rt, Proxy: proxy, Logger: discardLogger()}, Options{})
		e.managedStep(ctx)
		assert.Empty(t, rt.calls(), "no ensure may go out without the pinning anchor")

		// 锚恢复：下拍正常下发（带钉住）。
		rt.clusterView = capability.ClusterView{Nodes: []capability.NodeView{{
			NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmmanager", Role: "manager", Available: true,
		}}}
		e.managedStep(ctx)
		require.NotEmpty(t, rt.calls())
		last := rt.calls()[len(rt.calls())-1]
		assert.Equal(t, []string{"01JD0NODE00000000000000000"}, last.Spec["vol"].Placement.NodeIDs)
	})
}
