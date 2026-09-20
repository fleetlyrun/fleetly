package state

import (
	"context"
	"testing"
	"time"
)

// TestObserverSyncAndHealth 观测缓存刷新器：启动首拍全量同步（成功前
// checker 不健康）→ 成功后行非 stale、checker 健康；底座不可达 → 全部
// 行置 stale、checker 不健康（指数退避，服务不崩）；恢复后自愈。
func TestObserverSyncAndHealth(t *testing.T) {
	st := newTestStore(t)
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 1)
	ob := NewObserver(st, fake, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ob.Start(ctx); err != nil {
		t.Fatalf("start observer: %v", err)
	}
	defer func() { _ = ob.Stop(ctx) }()

	// 首拍成功：缓存行 + 健康置位（两步窗口同下——条件并等两者）。
	waitFor(t, 3*time.Second, func() bool {
		rows, err := st.ListCachedNodes(ctx)
		return err == nil && len(rows) == 1 && !rows[0].Stale && ob.CheckHealth() == nil
	}, "first resync should populate cache")
	if ob.CheckHealth() != nil {
		t.Fatalf("checker must be healthy after successful sync: %v", ob.CheckHealth())
	}

	// 底座不可达：下一拍全部置 stale + checker 不健康。
	fake.setPingErr(context.DeadlineExceeded)
	ob.Invalidate()
	waitFor(t, 5*time.Second, func() bool {
		rows, err := st.ListCachedNodes(ctx)
		return err == nil && len(rows) == 1 && rows[0].Stale
	}, "unreachable substrate must mark all cached nodes stale")
	if ob.CheckHealth() == nil {
		t.Fatal("checker must be unhealthy while substrate unreachable")
	}

	// 恢复：stale 清零、checker 回健康（指数退避不阻断恢复）。健康位与
	// 行写入是两步（syncOnce 先落行、resync 后置 syncOK）——等待条件必须
	// 同时覆盖两者，避免在窗口内断言（-race 高负载下窗口可观测）。
	fake.setPingErr(nil)
	ob.Invalidate()
	waitFor(t, 5*time.Second, func() bool {
		rows, err := st.ListCachedNodes(ctx)
		return err == nil && len(rows) == 1 && !rows[0].Stale && ob.CheckHealth() == nil
	}, "recovered substrate must clear stale")
	if ob.CheckHealth() != nil {
		t.Fatalf("checker must recover: %v", ob.CheckHealth())
	}
}

// TestObserverEventDrivenRefresh 事件驱动失效：node/service/task 事件触发
// 刷新（变更 1s 内节流口径），无关事件不触发。
func TestObserverEventDrivenRefresh(t *testing.T) {
	st := newTestStore(t)
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 1)
	ob := NewObserver(st, fake, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ob.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ob.Stop(ctx) }()
	waitFor(t, 3*time.Second, func() bool {
		rows, _ := st.ListCachedNodes(ctx)
		return len(rows) == 1
	}, "initial sync")

	// 首拍之后底座出现新节点（模拟外部变化），事件先于周期拍到达。
	fake.addNode("swarm-b", "node-b", "ready", 1)
	fake.events <- SubstrateEvent{Type: EventTypeService, Action: "update", At: time.Now().UTC()}
	waitFor(t, 3*time.Second, func() bool {
		rows, _ := st.ListCachedNodes(ctx)
		for _, r := range rows {
			if r.SwarmNodeID == "swarm-b" {
				return true
			}
		}
		return false
	}, "node/service/task events must trigger prompt resync")

	// 事件消失（节点移除）→ 事件拍修剪缓存行。
	fake.removeNode("swarm-b")
	fake.events <- SubstrateEvent{Type: EventTypeTask, Action: "die", At: time.Now().UTC()}
	waitFor(t, 3*time.Second, func() bool {
		rows, _ := st.ListCachedNodes(ctx)
		return len(rows) == 1
	}, "removed nodes must be pruned from cache on resync")

	// 无关事件（container 等）不触发刷新（快照未变，行集保持）。
	fake.events <- SubstrateEvent{Type: "container", Action: "start", At: time.Now().UTC()}
	time.Sleep(300 * time.Millisecond)
	rows, _ := st.ListCachedNodes(ctx)
	if len(rows) != 1 {
		t.Fatalf("irrelevant events must not disturb cache, rows = %d", len(rows))
	}
}

// TestObserverSubstrateDownAtBoot 底座启动即不可达：服务不崩（循环存活、
// 可 Stop），缓存为空、checker 不健康；底座恢复后收敛。
func TestObserverSubstrateDownAtBoot(t *testing.T) {
	st := newTestStore(t)
	fake := newFakeDocker()
	fake.setPingErr(context.DeadlineExceeded)
	ob := NewObserver(st, fake, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ob.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		ping, _, _, _, _ := fake.snapshotCalls()
		return ping > 1 // 退避重试在跑（未崩溃、未放弃）
	}, "observer must keep retrying while substrate down")
	if ob.CheckHealth() == nil {
		t.Fatal("checker must be unhealthy while substrate down")
	}

	fake.setPingErr(nil)
	fake.addNode("swarm-a", "node-a", "ready", 1)
	ob.Invalidate()
	waitFor(t, 5*time.Second, func() bool { return ob.CheckHealth() == nil },
		"observer must converge after substrate recovery")

	if err := ob.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// TestObserverInvalidateThrottle 失效信号合并：连发多次 Invalidate 只补
// 有限次数刷新（1s 节流窗不放大底座压力）。
func TestObserverInvalidateThrottle(t *testing.T) {
	st := newTestStore(t)
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 1)
	ob := NewObserver(st, fake, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ob.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = ob.Stop(ctx) }()
	waitFor(t, 3*time.Second, func() bool {
		rows, _ := st.ListCachedNodes(ctx)
		return len(rows) == 1
	}, "initial sync")

	_, list0, _, _, _ := fake.snapshotCalls()
	for i := 0; i < 20; i++ {
		ob.Invalidate()
	}
	// 节流窗（1s）+ 刷新执行预算后：20 连发最多产生个位数额外同步。
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, list1, _, _, _ := fake.snapshotCalls()
		if list1 > list0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, list1, _, _, _ := fake.snapshotCalls()
	if merged := list1 - list0; merged > 5 {
		t.Fatalf("invalidate storm produced %d syncs, throttle should coalesce to a few", merged)
	}
}
