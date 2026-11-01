package engine

// H10 节点生命周期清理单测（§4.1/§4.9）：node.left 先清安全材料（个体 relay
// 凭证行 + 在连中继）再发事件；事件失败不阻断清理；仅实际删行才落
// node.relay_credential_revoked（reason=node_left）。
//
// S11b 宽限窗：单拍缺行不再等价于离场——连续缺失满 opts.Node.LeaveGrace
// 才清理；窗内回归即清零（不删凭证、不断连、不发事件）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// testRelayCredential 是节点生命周期夹具的节点个体凭证（node-1/carrier-1
// 绑定；H10：中继附着只认 node_relay_credentials 的 hash 或 engine 内部
// token）。P5 wave-2 起 fakeExecFace/relaySink 夹具已随 execs 卫星包迁走，
// 本文件持本地最小副本（子面注入经 Runtime 包裹点亮）。
//
//nolint:gosec // 测试夹具常量，非真实凭证
const testRelayCredential = "fltrelay_apitest_engine_fixture"

// nodesExecFace 是节点生命周期夹具的 exec 子面（实例解析恒定单实例——
// RelayAttach 只探测子面在场性，实例解析面不触达）。
type nodesExecFace struct{}

func (nodesExecFace) ExecInstances(context.Context, string) ([]capability.ExecTargetInstance, error) {
	return []capability.ExecTargetInstance{{Instance: "i1", CarrierNodeID: "carrier-1"}}, nil
}
func (nodesExecFace) ExecWorkload(context.Context, capability.ExecWorkloadRequest) (int, error) {
	return 0, nil
}
func (nodesExecFace) RunNodeRelay(context.Context, capability.NodeRelayOptions) error { return nil }

// execCapableRuntime 包裹 fakeRuntime 点亮 exec 子面（capability.FacesOf
// 对包裹体的接口探测；底座事件/快照仍读原 rt）。
type execCapableRuntime struct {
	*fakeRuntime
	nodesExecFace
}

// relaySink 是最小 RelayWriter（本文件只要求中继在连）。
type relaySink struct{}

func (relaySink) WriteRelayFrame([]byte) error { return nil }

// departedWorker 布置一台已离开集群的 worker：观测行 + 个体凭证 + 在连
// 中继（exec 子面经 Runtime 包裹点亮——P5 wave-2 拆包后卫星装配面经
// Deps 注入，白盒字段注入面不再可达）。
func departedWorker(t *testing.T) (*Engine, *fakeRuntime, *statetest.FakeClock) {
	t.Helper()
	e, rt, clock := newTestEngineBuilt(t, Options{}, discardLogger(), nil, func(fr *fakeRuntime) capability.Runtime {
		return &execCapableRuntime{fakeRuntime: fr}
	})
	ctx := context.Background()
	require.NoError(t, e.repos.Nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "worker", Available: true,
	}))
	require.NoError(t, e.repos.Nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "node-1", CarrierID: "carrier-1", SHA256: node.HashRelayCredential(testRelayCredential),
	}))
	_, detach, err := e.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", relaySink{})
	require.NoError(t, err)
	t.Cleanup(detach)
	require.Len(t, e.RelayStatuses(), 1)

	// 快照切空 = 节点显式离开（空视图也是显式结果）。
	setClusterNodes(rt)
	return e, rt, clock
}

// setClusterNodes 覆写假集群快照（显式启用编程视图；空集 = 节点全离开）。
func setClusterNodes(rt *fakeRuntime, nodes ...capability.NodeView) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.clusterOverride = true
	rt.clusterView = capability.ClusterView{Nodes: nodes}
}

// nodeSnapshot 是 node-1 在快照内的回归形态（carrier 与观测行同源）。
func nodeSnapshot() capability.NodeView {
	return capability.NodeView{NodeID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "worker", Available: true}
}

// revocationAudit 读 node.relay_credential_revoked 审计行。
func revocationAudit(t *testing.T, e *Engine) []audit.Entry {
	t.Helper()
	rows, err := e.repos.Audits.ListFiltered(context.Background(), audit.Filter{
		ActionPrefix: "node.relay_credential_revoked", Limit: 10,
	})
	require.NoError(t, err)
	return rows
}

// assertNodeStillAttached：宽限窗内节点材料原样（凭证在、中继在连、无审计、
// 无 node.left 事件、观测缓存仍可用）。
func assertNodeStillAttached(t *testing.T, e *Engine) {
	t.Helper()
	ctx := context.Background()
	_, err := e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(testRelayCredential))
	require.NoError(t, err, "a node inside the leave grace window must keep its relay credential")
	assert.Len(t, e.RelayStatuses(), 1, "a node inside the leave grace window must keep its live relay")
	assert.Empty(t, revocationAudit(t, e), "a jitter inside the grace window must not append a revocation row")
	assert.Empty(t, eventNames(t, e, "node-1"), "a jitter inside the grace window must not emit node.left")
	n, err := e.repos.Nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.True(t, n.Available, "the observation cache must stay available while the node is only transiently missing")
}

// assertDepartedRelayClean：凭证行已删、在连中继已断、审计恰一行且 Detail
// 只带 carrier 与 node_left 原因。
func assertDepartedRelayClean(t *testing.T, e *Engine) {
	t.Helper()
	ctx := context.Background()
	_, err := e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(testRelayCredential))
	require.ErrorIs(t, err, state.ErrNotFound, "the departed node's relay credential must be deleted")
	assert.Empty(t, e.RelayStatuses(), "the departed node's live relay must be dropped")

	rows := revocationAudit(t, e)
	require.Len(t, rows, 1, "a deleted credential leaves exactly one revocation row")
	assert.Equal(t, "node.relay_credential_revoked", rows[0].Action)
	assert.Equal(t, "node/node-1", rows[0].Resource)
	assert.Contains(t, rows[0].Detail, "carrier-1")
	assert.Contains(t, rows[0].Detail, "node_left")

	n, err := e.repos.Nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.False(t, n.Available, "departed node marked unavailable in the observation cache")
	assert.Equal(t, []string{"node.left"}, eventNames(t, e, "node-1"))

	// 去抖：再对账不重复审计。
	e.reconcileNodes(ctx)
	assert.Len(t, revocationAudit(t, e), 1, "a debounced node.left must not append a second revocation row")
}

// TestNodeLeftRevokesRelayCredential：连续缺失满宽限窗 → 清理凭证 + 断中继
// + 审计 reason=node_left，且 node.left 事件照发、观测缓存照常下线。窗内
// （含首拍与接近满窗）不清理——单拍缺行不再触发离场。
func TestNodeLeftRevokesRelayCredential(t *testing.T) {
	e, _, clock := departedWorker(t)
	ctx := context.Background()

	// 首拍缺行：只是宽限窗起算，不清理。
	e.reconcileNodes(ctx)
	assertNodeStillAttached(t, e)

	// 窗内（差 1s 满窗）：仍不清理。
	clock.Advance(e.opts.Node.LeaveGrace - time.Second)
	e.reconcileNodes(ctx)
	assertNodeStillAttached(t, e)

	// 窗满：执行离场清理。
	clock.Advance(time.Second)
	e.reconcileNodes(ctx)
	assertDepartedRelayClean(t, e)
}

// TestNodeLeftGraceToleratesSnapshotJitter：单拍快照抖动（节点短暂缺行后
// 回归）不删凭证/不断连/不发事件；回归清除宽限计时，之后再离开仍按宽限
// 重新计——leave/join 循环不失真（第二次离场照发 node.left）。
func TestNodeLeftGraceToleratesSnapshotJitter(t *testing.T) {
	e, rt, clock := departedWorker(t)
	ctx := context.Background()

	// 抖动：缺行一拍 → 回归（窗内）→ 再缺行一拍。
	e.reconcileNodes(ctx)
	setClusterNodes(rt, nodeSnapshot())
	e.reconcileNodes(ctx)
	setClusterNodes(rt)
	e.reconcileNodes(ctx)
	assertNodeStillAttached(t, e)

	// 回归后再离场：宽限窗重新计（不是立即清理），满窗才清理。
	clock.Advance(e.opts.Node.LeaveGrace / 2)
	e.reconcileNodes(ctx)
	assertNodeStillAttached(t, e)
	clock.Advance(e.opts.Node.LeaveGrace)
	e.reconcileNodes(ctx)
	assertDepartedRelayClean(t, e)
}

// TestNodeLeftRevokesEvenWhenEventFails（"事件失败仍清理"验收锚）：事件面
// 故障（outbox 表不可写）时安全材料清理与观测缓存下线不受影响——清理不
// 得挂在事件 continue 之后。审计面（audit 表）仍在，故吊销事实可见。
func TestNodeLeftRevokesEvenWhenEventFails(t *testing.T) {
	e, _, clock := departedWorker(t)
	// 注入事件面故障：outbox 改名 → StreamOnly 事件 Append 失败；audit 表
	// 不受影响（LedgerOnly 只触碰取证面）。
	_, err := e.db.Runner().ExecContext(context.Background(),
		`ALTER TABLE outbox RENAME TO outbox_unavailable`)
	require.NoError(t, err)

	e.reconcileNodes(context.Background()) // 宽限起算
	clock.Advance(e.opts.Node.LeaveGrace)
	e.reconcileNodes(context.Background()) // 窗满：清理（事件侧失败）

	ctx := context.Background()
	_, err = e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(testRelayCredential))
	require.ErrorIs(t, err, state.ErrNotFound, "credential cleanup must not depend on the node.left event")
	assert.Empty(t, e.RelayStatuses(), "relay drop must not depend on the node.left event")
	assert.Len(t, revocationAudit(t, e), 1, "the ledger row must survive an event-side failure")

	n, err := e.repos.Nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.False(t, n.Available, "the unavailable mark must still run when the event fails")
}
