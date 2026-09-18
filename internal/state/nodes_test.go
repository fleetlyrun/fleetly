package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSyncNodeObservationsUpsertAndPrune 观测缓存写入：upsert、修剪消失
// 节点、last_seen_at 单调（观测语义：只随成功观测拍推进）。
func TestSyncNodeObservationsUpsertAndPrune(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	t1 := time.Now().UTC().Add(-2 * time.Hour)
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 3)
	fake.addNode("swarm-b", "node-b", "down", 7)
	if err := st.SyncNodeObservations(ctx, mustList(t, fake), t1); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rows, err := st.ListCachedNodes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byID := map[string]CachedNode{}
	for _, r := range rows {
		byID[r.SwarmNodeID] = r
	}
	a := byID["swarm-a"]
	if a.Hostname != "node-a" || a.State != "ready" || a.Stale {
		t.Fatalf("node-a row = %+v", a)
	}
	if a.ObservedAt != t1 || a.LastSeenAt != t1 {
		t.Fatalf("node-a stamps = %v/%v, want %v", a.ObservedAt, a.LastSeenAt, t1)
	}
	if b := byID["swarm-b"]; b.State != "down" || b.SubstrateVersion != 7 {
		t.Fatalf("node-b row = %+v", b)
	}

	// 第二拍：swarm-a 仍在（内容变化）；swarm-b 消失（修剪）；新增 swarm-c。
	t2 := time.Now().UTC()
	fake2nodes := []SubstrateNode{
		{SwarmNodeID: "swarm-a", Hostname: "node-a", State: "ready", Availability: "drain", IsManager: true, Version: ObjectVersion{Index: 4}},
		{SwarmNodeID: "swarm-c", Hostname: "node-c", State: "ready", Availability: "active", Version: ObjectVersion{Index: 1}},
	}
	if err := st.SyncNodeObservations(ctx, fake2nodes, t2); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	rows, err = st.ListCachedNodes(ctx)
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after prune = %d, want 2 (swarm-b pruned)", len(rows))
	}
	byID = map[string]CachedNode{}
	for _, r := range rows {
		byID[r.SwarmNodeID] = r
	}
	a = byID["swarm-a"]
	if a.Availability != "drain" || !a.IsManager || a.SubstrateVersion != 4 {
		t.Fatalf("node-a row after update = %+v", a)
	}
	if !a.LastSeenAt.Equal(t2) || !a.ObservedAt.Equal(t2) {
		t.Fatalf("node-a stamps after update = %v/%v, want %v", a.LastSeenAt, a.ObservedAt, t2)
	}
}

// TestMarkAllNodesStale 底座不可达降级：全部行置 stale、observed_at /
// last_seen_at 保持最后一次真实观测；下一拍成功后 stale 清零。
func TestMarkAllNodesStale(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	t1 := time.Now().UTC().Add(-time.Hour)
	if err := st.SyncNodeObservations(ctx, []SubstrateNode{
		{SwarmNodeID: "swarm-a", Hostname: "node-a", State: "ready", Availability: "active", Version: ObjectVersion{Index: 1}},
	}, t1); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := st.MarkAllNodesStale(ctx); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	rows, _ := st.ListCachedNodes(ctx)
	if len(rows) != 1 || !rows[0].Stale {
		t.Fatalf("rows after stale mark = %+v", rows)
	}
	if !rows[0].ObservedAt.Equal(t1) {
		t.Fatalf("observed_at must keep last real observation, got %v", rows[0].ObservedAt)
	}

	// 恢复拍：stale 清零。
	t2 := time.Now().UTC()
	if err := st.SyncNodeObservations(ctx, []SubstrateNode{
		{SwarmNodeID: "swarm-a", Hostname: "node-a", State: "ready", Availability: "active", Version: ObjectVersion{Index: 2}},
	}, t2); err != nil {
		t.Fatalf("sync recover: %v", err)
	}
	rows, _ = st.ListCachedNodes(ctx)
	if len(rows) != 1 || rows[0].Stale {
		t.Fatalf("stale must clear after successful sync: %+v", rows)
	}
}

// TestRuntimeNodeRef 适配器映射：幂等 upsert、换绑冲突拒绝。
func TestRuntimeNodeRef(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	at := time.Now().UTC()
	changed, err := st.UpsertRuntimeNodeRef(ctx, "n_AAA", "swarm-1", at)
	if err != nil || !changed {
		t.Fatalf("first ref: changed=%v err=%v, want created", changed, err)
	}
	// 同映射重复写：无变更（不再重复审计）。
	changed, err = st.UpsertRuntimeNodeRef(ctx, "n_AAA", "swarm-1", at.Add(time.Minute))
	if err != nil || changed {
		t.Fatalf("idempotent ref: changed=%v err=%v, want unchanged", changed, err)
	}
	// 同平台 ID 换到新 swarm id（换机/重建）：允许更新映射（重绑人工流程
	// 的数据面），本次映射变更会触发审计。
	changed, err = st.UpsertRuntimeNodeRef(ctx, "n_AAA", "swarm-2", at.Add(2*time.Minute))
	if err != nil || !changed {
		t.Fatalf("rebind ref: changed=%v err=%v, want changed", changed, err)
	}
	// 另一平台 ID 抢占已被占用的 swarm node id：拒绝（不猜测）。
	if _, err := st.UpsertRuntimeNodeRef(ctx, "n_BBB", "swarm-2", at); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("conflicting mapping must be ErrVersionConflict, got: %v", err)
	}
	ref, err := st.GetRuntimeNodeRef(ctx, "n_AAA")
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	if ref.SwarmNodeID != "swarm-2" {
		t.Fatalf("ref swarm id = %s, want swarm-2", ref.SwarmNodeID)
	}
	if _, err := st.GetRuntimeNodeRef(ctx, "n_MISSING"); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("missing ref must be ErrRefNotFound, got: %v", err)
	}
}

// mustList 取测试替身的快照（ListNodeObservations 的便捷封装）。
func mustList(t *testing.T, f *fakeDocker) []SubstrateNode {
	t.Helper()
	nodes, err := f.ListNodeObservations(context.Background())
	if err != nil {
		t.Fatalf("fake list: %v", err)
	}
	return nodes
}
