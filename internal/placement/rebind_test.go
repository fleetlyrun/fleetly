package placement

// 显式换点与迁移 runbook 测试（multi-node §2.6/§2.8，E1-7）：目标校验
// （直读存在 + ready + 已锚定）、data_ack 门（有卷必填 →
// E_VOLUME_NODE_MISMATCH；discarded 需 confirm → E_PLACEMENT_MOVE_REQUIRES_ACK）、
// 落库同事务（绑定换绑 + 卷行 prev 登记 + placement.changed + 审计）、
// 换点不自动部署；迁移计划为只读 runbook。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// rebindHarness 在 newHarness 之上追加：已锚定 worker + 本机已绑定 + 卷在
// 本机 + worker 的 runtime ref（Preflight/rebind 校验面完整）。
func rebindHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	wID := "n_" + ulid.Make().String()
	h.addWorker("worker-01", wID)
	if _, err := h.store.UpsertRuntimeNodeRef(context.Background(), h.platformID, h.swarmID, time.Now().UTC()); err != nil {
		t.Fatalf("seed self ref: %v", err)
	}
	if _, err := h.store.UpsertRuntimeNodeRef(context.Background(), wID, "swarm-worker-01", time.Now().UTC()); err != nil {
		t.Fatalf("seed worker ref: %v", err)
	}
	if _, err := h.resolver.Apply(context.Background(), h.input()); err != nil {
		t.Fatalf("apply initial binding: %v", err)
	}
	return h, wID
}

// TestRebindDataAckGate：有卷应用换点缺 data_ack → E_VOLUME_NODE_MISMATCH；
// discarded 无 confirm → E_PLACEMENT_MOVE_REQUIRES_ACK；restored 放行。
func TestRebindDataAckGate(t *testing.T) {
	ctx := context.Background()
	h, wID := rebindHarness(t)

	_, err := h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: "worker-01"})
	assertCode(t, err, "E_VOLUME_NODE_MISMATCH")

	_, err = h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: "worker-01", DataAck: DataAckDiscarded})
	assertCode(t, err, "E_PLACEMENT_MOVE_REQUIRES_ACK")

	res, err := h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: "worker-01", DataAck: DataAckRestored})
	if err != nil {
		t.Fatalf("rebind restored: %v", err)
	}
	if res.Placement.PlatformNodeID != wID || res.Placement.State != state.PlacementBound {
		t.Fatalf("placement = %+v, want bound to worker", res.Placement)
	}
	if res.FromNode != h.platformID {
		t.Fatalf("from = %s, want %s", res.FromNode, h.platformID)
	}
	// 卷行换绑：platform_node_id → 目标，prev 登记源节点（残留指引锚）。
	vols, err := h.store.ListAppVolumes(ctx, h.appID)
	if err != nil || len(vols) != 1 {
		t.Fatalf("volumes = %+v err=%v", vols, err)
	}
	if vols[0].PlatformNodeID != wID || vols[0].PrevPlatformNodeID != h.platformID {
		t.Fatalf("volume node = %s prev = %s, want %s/%s", vols[0].PlatformNodeID,
			vols[0].PrevPlatformNodeID, wID, h.platformID)
	}
	// 事件与审计（同事务 Outbox 面）。
	assertEvent(t, h.store, "placement.changed")
	assertAudit(t, h.store, "placement.rebind")
}

// TestRebindDiscardedRequiresConfirm：confirm 门通过后 discarded 落库并
// 发 volume.discarded；prev 语义与 restored 一致（源节点副本 = 残留）。
func TestRebindDiscardedRequiresConfirm(t *testing.T) {
	ctx := context.Background()
	h, wID := rebindHarness(t)

	res, err := h.resolver.Rebind(ctx, RebindInput{
		AppID: h.appID, Node: wID, DataAck: DataAckDiscarded, Confirm: true,
	})
	if err != nil {
		t.Fatalf("rebind discarded+confirm: %v", err)
	}
	if !res.Discarded || res.Placement.PlatformNodeID != wID {
		t.Fatalf("result = %+v", res)
	}
	assertEvent(t, h.store, "volume.discarded")
	vols, _ := h.store.ListAppVolumes(ctx, h.appID)
	if vols[0].PrevPlatformNodeID != h.platformID {
		t.Fatalf("prev = %q, want source node recorded", vols[0].PrevPlatformNodeID)
	}
}

// TestRebindTargetValidation：目标不存在 → E_PLACEMENT_NODE_NOT_FOUND +
// 候选清单；未锚定 → E_PLACEMENT_NODE_UNAVAILABLE；非 ready → 同码。
func TestRebindTargetValidation(t *testing.T) {
	ctx := context.Background()
	h, _ := rebindHarness(t)

	_, err := h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: "no-such-node", DataAck: DataAckRestored})
	assertCode(t, err, "E_PLACEMENT_NODE_NOT_FOUND")

	// 未锚定节点（无平台 ID）不可作为绑定锚。
	h.docker.nodes = append(h.docker.nodes, state.SubstrateNode{
		SwarmNodeID: "swarm-bare", Hostname: "bare-01", State: "ready", Availability: "active",
		Labels: map[string]string{},
	})
	_, err = h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: "bare-01", DataAck: DataAckRestored})
	assertCode(t, err, "E_PLACEMENT_NODE_UNAVAILABLE")

	// drain 目标 → UNAVAILABLE。
	drained := "n_" + ulid.Make().String()
	h.docker.nodes = append(h.docker.nodes, state.SubstrateNode{
		SwarmNodeID: "swarm-drained", Hostname: "drained-01", State: "ready", Availability: "drain",
		Labels: map[string]string{state.LabelNodeID: drained},
	})
	_, err = h.resolver.Rebind(ctx, RebindInput{AppID: h.appID, Node: drained, DataAck: DataAckRestored})
	assertCode(t, err, "E_PLACEMENT_NODE_UNAVAILABLE")

	// 校验失败路径零写入：绑定与卷行保持原样。
	p, err := h.store.GetPlacement(ctx, h.appID)
	if err != nil || p.PlatformNodeID != h.platformID {
		t.Fatalf("binding changed after failed rebinds: %+v err=%v", p, err)
	}
}

// TestMigrationPlan：runbook 含真实卷名/节点名与 rebind/deploy 收口步骤；
// 目标未 ready 输出警示；计划只读（不产生事件/不落库）。
func TestMigrationPlan(t *testing.T) {
	ctx := context.Background()
	h, wID := rebindHarness(t)

	plan, err := h.resolver.MigrationPlan(ctx, h.appID, "my-api", "worker-01")
	if err != nil {
		t.Fatalf("migration plan: %v", err)
	}
	if plan.ToNode == "" || !strings.Contains(plan.ToNode, "worker-01") {
		t.Fatalf("to = %q", plan.ToNode)
	}
	if plan.FromNode == "" || !strings.Contains(plan.FromNode, h.platformID) {
		t.Fatalf("from = %q, want current binding node", plan.FromNode)
	}
	if len(plan.Volumes) != 1 {
		t.Fatalf("volumes = %+v", plan.Volumes)
	}
	var backupStep, rebindStep, deployStep bool
	for _, s := range plan.Steps {
		if strings.Contains(s.Detail, plan.Volumes[0].Name) && strings.Contains(s.Title, "Backup") {
			backupStep = true
		}
		if strings.Contains(s.Detail, "placement rebind") {
			rebindStep = true
		}
		if strings.Contains(s.Detail, "deploy my-api") {
			deployStep = true
		}
	}
	if !backupStep || !rebindStep || !deployStep {
		t.Fatalf("runbook steps incomplete: %+v", plan.Steps)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("ready target must not warn: %v", plan.Warnings)
	}

	// 目标未 ready → 计划仍生成但带警示（计划是文档，rebind 才是闸门）。
	h.docker.nodes[1] = state.SubstrateNode{
		SwarmNodeID: "swarm-worker-01", Hostname: "worker-01", State: "ready", Availability: "drain",
		Labels: map[string]string{state.LabelNodeID: wID},
	}
	plan2, err := h.resolver.MigrationPlan(ctx, h.appID, "my-api", wID)
	if err != nil {
		t.Fatalf("migration plan (drained target): %v", err)
	}
	if len(plan2.Warnings) == 0 || !strings.Contains(strings.Join(plan2.Warnings, " "), "not ready") {
		t.Fatalf("drained target must warn: %v", plan2.Warnings)
	}

	// 计划只读：事件表无新增（placement.changed 只由 Rebind 发出）。
	events, err := h.store.EventsSince(ctx, 0, 200)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, e := range events {
		if e.Name == "placement.changed" {
			t.Fatal("migration plan must not write placement.changed")
		}
	}
}
