package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNodeIdentityLifecycle 平台节点身份（state-model §2.3）：首次生成
// n_<ULID> + 审计；二次启动复用同一 ID；锚定写 label + runtime_node_refs；
// 重复锚定幂等（无多余 label 写）。
func TestNodeIdentityLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	fake := newFakeDocker()
	fake.setSelf("swarm-self", nil)
	fake.addNode("swarm-self", "this-node", "ready", 2)

	id := NewNodeIdentity(st, fake, testLogger())

	// 首次：生成 + 持久化 + 审计。
	pid1, err := id.EnsurePlatformID(ctx)
	if err != nil {
		t.Fatalf("ensure platform id: %v", err)
	}
	if len(pid1) != len(NodeIDPrefix)+26 {
		t.Fatalf("platform id = %q, want %s<ULID26>", pid1, NodeIDPrefix)
	}
	if got, _ := st.GetMeta(ctx, MetaKeyPlatformNodeID); got != pid1 {
		t.Fatalf("meta platform id = %q, want %q", got, pid1)
	}
	audits, _ := st.RecentAudits(ctx, 10)
	if len(audits) != 1 || audits[0].Action != "node.identity_created" {
		t.Fatalf("audits = %+v, want one node.identity_created", audits)
	}

	// 锚定：直读取令牌 → 写 label → 登记映射 + 审计。
	if err := id.Anchor(ctx); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if !id.Anchored() {
		t.Fatal("anchored = false after successful anchor")
	}
	if got := fake.labels["swarm-self"][LabelNodeID]; got != pid1 {
		t.Fatalf("swarm label %s = %q, want %q", LabelNodeID, got, pid1)
	}
	ref, err := st.GetRuntimeNodeRef(ctx, pid1)
	if err != nil || ref.SwarmNodeID != "swarm-self" {
		t.Fatalf("runtime ref = %+v err=%v", ref, err)
	}
	audits, _ = st.RecentAudits(ctx, 10)
	if len(audits) != 2 || audits[0].Action != "node.identity_anchored" {
		t.Fatalf("audits after anchor = %+v, want identity_created + identity_anchored", audits)
	}

	// 二次启动：同一 ID 复用；重复锚定幂等（label 已就位 → 不再产生
	// 更新调用，仅保留直读令牌的校验读）。
	id2 := NewNodeIdentity(st, fake, testLogger())
	pid2, err := id2.EnsurePlatformID(ctx)
	if err != nil || pid2 != pid1 {
		t.Fatalf("second boot id = %q err=%v, want %q", pid2, err, pid1)
	}
	_, _, update, resolve, _ := fake.snapshotCalls()
	if err := id2.Anchor(ctx); err != nil {
		t.Fatalf("re-anchor: %v", err)
	}
	_, _, update2, resolve2, _ := fake.snapshotCalls()
	if update2 != update {
		t.Fatalf("re-anchor must not write label again: update calls %d -> %d", update, update2)
	}
	if resolve2 != resolve+1 {
		t.Fatalf("re-anchor keeps exactly one fresh token read: resolve calls %d -> %d", resolve, resolve2)
	}
	audits, _ = st.RecentAudits(ctx, 10)
	if len(audits) != 2 {
		t.Fatalf("re-anchor must not duplicate audit rows, got %d", len(audits))
	}
}

// TestNodeIdentityConflictRetry 写前直读令牌在写入时失效：重取版本重试
// 成功（乐观并发纪律的真实路径）；持续冲突耗尽重试后报错。
func TestNodeIdentityConflictRetry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	fake := newFakeDocker()
	fake.setSelf("swarm-self", nil)
	fake.addNode("swarm-self", "this-node", "ready", 2)

	id := NewNodeIdentity(st, fake, testLogger())
	if _, err := id.EnsurePlatformID(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// 注入一次并发冲突：第一次更新失败，重试（重取令牌）成功。
	fake.mu.Lock()
	fake.labelUpdateConflicts["swarm-self"] = 1
	fake.mu.Unlock()
	if err := id.Anchor(ctx); err != nil {
		t.Fatalf("anchor with one conflict: %v", err)
	}
	if got := fake.labels["swarm-self"][LabelNodeID]; got == "" {
		t.Fatal("label must be written after retry")
	}

	// 持续冲突：耗尽重试次数后失败，label 仍可能未落（调用方退避重来）。
	fake2 := newFakeDocker()
	fake2.setSelf("swarm-self", nil)
	fake2.addNode("swarm-self", "this-node", "ready", 2)
	fake2.mu.Lock()
	fake2.labelUpdateConflicts["swarm-self"] = labelWriteRetries + 1
	fake2.mu.Unlock()
	id2 := NewNodeIdentity(st, fake2, testLogger())
	if _, err := id2.EnsurePlatformID(ctx); err != nil {
		t.Fatalf("ensure 2: %v", err)
	}
	if err := id2.Anchor(ctx); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("persistent conflict must surface ErrVersionConflict, got: %v", err)
	}
}

// TestNodeIdentityNotSwarm 引擎未启用 Swarm：Anchor 返回
// ErrNotSwarmManager（调用方退避重试），身份本体（meta）不受影响。
func TestNodeIdentityNotSwarm(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	fake := newFakeDocker()
	fake.setSelf("", ErrNotSwarmManager)

	id := NewNodeIdentity(st, fake, testLogger())
	pid, err := id.EnsurePlatformID(ctx)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := id.Anchor(ctx); !errors.Is(err, ErrNotSwarmManager) {
		t.Fatalf("anchor without swarm must be ErrNotSwarmManager, got: %v", err)
	}
	if id.Anchored() {
		t.Fatal("must not report anchored without swarm")
	}
	// 底座恢复后同一身份成功锚定（Start 前手动验证重试路径语义）。
	fake.setSelf("swarm-self", nil)
	fake.addNode("swarm-self", "this-node", "ready", 1)
	if err := id.Anchor(ctx); err != nil {
		t.Fatalf("anchor after swarm up: %v", err)
	}
	if ref, err := st.GetRuntimeNodeRef(ctx, pid); err != nil || ref.SwarmNodeID != "swarm-self" {
		t.Fatalf("ref after recovery = %+v err=%v", ref, err)
	}
}

// TestNodeIdentityAnchorLoop 退避守护：Swarm 不可用 → 退避重试不放弃，
// 恢复后自动锚定成功 → Stop 退出。
func TestNodeIdentityAnchorLoop(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := newFakeDocker()
	fake.setSelf("", ErrNotSwarmManager)

	id := NewNodeIdentity(st, fake, testLogger())
	if _, err := id.EnsurePlatformID(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := id.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		_, _, _, _, self := fake.snapshotCalls()
		return self > 1 // 至少两次尝试 = 失败后退避重试在跑
	}, "anchor loop should keep retrying while swarm unavailable")

	// Swarm 就绪 → 循环内自动锚定成功（指数退避最长 10s 内重试到）。
	fake.setSelf("swarm-self", nil)
	fake.addNode("swarm-self", "this-node", "ready", 1)
	waitFor(t, 12*time.Second, id.Anchored, "anchor loop should converge after swarm becomes active")

	if err := id.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
