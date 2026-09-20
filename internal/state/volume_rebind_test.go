package state

// 迁移 00010 与卷换绑原语测试（multi-node §2.8，E1-7）：prev_platform_node_id
// 只加列（空缺省 = 现状语义——既有行零迁移、行为逐字节不变）；RebindAppVolumes
// 跨节点登记 prev、同节点幂等不洗写；ListAllVolumes 跨 app 清单。

import (
	"context"
	"testing"
)

// TestVolumePrevNodeColumnDefaultsEmpty：00010 加列的既有行语义——已登记
// 卷的 PrevPlatformNodeID 读出空串（从未跨节点迁移），行为与加列前等价。
func TestVolumePrevNodeColumnDefaultsEmpty(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	app, err := st.CreateApp(ctx, "", "app-1")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app.ID, Key: "data", Name: "fleetly-app-1-data-aaaaaaaa",
		PlatformNodeID: "n_source", MountPath: "/data",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	v, err := st.GetAppVolume(ctx, app.ID, "data")
	if err != nil {
		t.Fatalf("get volume: %v", err)
	}
	if v.PrevPlatformNodeID != "" {
		t.Fatalf("prev = %q, want empty (00010 default = current semantics)", v.PrevPlatformNodeID)
	}
}

// TestRebindAppVolumes：跨节点换绑登记 prev；已在目标的卷不动（幂等不洗
// 写）；孤儿卷不参与换绑。
func TestRebindAppVolumes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	app, err := st.CreateApp(ctx, "", "app-1")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	app2, err := st.CreateApp(ctx, "", "app-2")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app.ID, Key: "data", Name: "fleetly-app-1-data-aaaaaaaa", PlatformNodeID: "n_src",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app.ID, Key: "cache", Name: "fleetly-app-1-cache-aaaaaaaa", PlatformNodeID: "n_src",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app2.ID, Key: "orphan", Name: "fleetly-app-2-orphan-aaaaaaaa", PlatformNodeID: "n_src",
	}); err != nil {
		t.Fatalf("register orphan: %v", err)
	}
	if _, err := st.MarkAppVolumesOrphaned(ctx, app2.ID); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	err = st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.RebindAppVolumes(ctx, app.ID, "n_dst")
		return err
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	v, err := st.GetAppVolume(ctx, app.ID, "data")
	if err != nil || v.PlatformNodeID != "n_dst" || v.PrevPlatformNodeID != "n_src" {
		t.Fatalf("data volume = %+v err=%v, want dst with prev src", v, err)
	}
	// 幂等：再次换绑（同目标）不洗写 prev。
	err = st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.RebindAppVolumes(ctx, app.ID, "n_dst")
		return err
	})
	if err != nil {
		t.Fatalf("rebind 2: %v", err)
	}
	if v, _ = st.GetAppVolume(ctx, app.ID, "data"); v.PrevPlatformNodeID != "n_src" {
		t.Fatalf("prev washed by idempotent rebind: %q", v.PrevPlatformNodeID)
	}
	// 孤儿卷不参与换绑。
	if v2, _ := st.GetAppVolume(ctx, app2.ID, "orphan"); v2.PlatformNodeID != "n_src" {
		t.Fatalf("orphan volume must not rebind: %+v", v2)
	}
}

// TestListAllVolumes：跨 app 清单含 active 与 orphaned 行（residual 标记
// 的派生由消费方按 prev 计算）。
func TestListAllVolumes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	// volumes 表有 apps 外键：先落两个应用行并以真实平台 ID 登记。
	app1, err := st.CreateApp(ctx, "", "app-1")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	app2, err := st.CreateApp(ctx, "", "app-2")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app1.ID, Key: "data", Name: "a-data", PlatformNodeID: "n_dst",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(ctx, VolumeWrite{
		AppID: app2.ID, Key: "orphan", Name: "b-orphan", PlatformNodeID: "n_src",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := st.MarkAppVolumesOrphaned(ctx, app2.ID); err != nil {
		t.Fatalf("orphan: %v", err)
	}
	all, err := st.ListAllVolumes(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("volumes = %+v, want 2 rows across apps", all)
	}
	statuses := map[string]VolumeStatus{}
	for _, v := range all {
		statuses[v.Name] = v.Status
	}
	if statuses["a-data"] != VolumeActive || statuses["b-orphan"] != VolumeOrphaned {
		t.Fatalf("statuses = %+v", statuses)
	}
}
