package engine

// 节点锚定护栏的回归群（2026-11-24，缺陷⑤；ADR-0070 落实注）：视图里
// 没有平台 ID 的载体不是节点——不落表、不参与离场处置；带空 ID 的
// node.joined 不落行。Provider 视图已不放未锚定节点，这里是汇点护栏。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// TestReconcileNodesSkipsUnanchoredCarriers（2026-11-24）：视图里没有平台
// ID 的载体不是节点——不落表（空主键行会与同载体的正规行互撞，载体反查
// 被抢先应答）；锚定后同一载体以正规行入表。
func TestReconcileNodesSkipsUnanchoredCarriers(t *testing.T) {
	e, rt, _ := newTestEngineBuilt(t, Options{}, discardLogger(), nil, func(fr *fakeRuntime) capability.Runtime { return fr })
	ctx := context.Background()
	anchored := capability.NodeView{NodeID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "worker", Available: true}
	fresh := capability.NodeView{CarrierID: "carrier-fresh", Hostname: "fresh", Role: "worker"}

	setClusterNodes(rt, anchored, fresh)
	e.reconcileNodes(ctx)
	rows, err := e.repos.Nodes.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a carrier without a platform id must not get a node row")
	assert.Equal(t, "node-1", rows[0].PlatformID)
	_, err = e.repos.Nodes.ByCarrier(ctx, "carrier-fresh")
	require.ErrorIs(t, err, state.ErrNotFound, "no row is answerable for an unanchored carrier")

	setClusterNodes(rt, anchored, capability.NodeView{NodeID: "node-2", CarrierID: "carrier-fresh", Hostname: "fresh", Role: "worker", Available: true})
	e.reconcileNodes(ctx)
	n, err := e.repos.Nodes.ByCarrier(ctx, "carrier-fresh")
	require.NoError(t, err)
	assert.Equal(t, "node-2", n.PlatformID, "once anchored the carrier enters under its minted id")
}

// TestReconcileNodesIgnoresIDlessCachedRow（2026-11-24）：历史缺陷落下的
// 空主键行不参与离场处置——不删凭证、不写吊销审计、不发 node.left、行
// 原样保留（迁移 00052 清存量；每次进程重启都为空 ID 走一遍处置是错的）。
func TestReconcileNodesIgnoresIDlessCachedRow(t *testing.T) {
	e, rt, clock := newTestEngineBuilt(t, Options{}, discardLogger(), nil, func(fr *fakeRuntime) capability.Runtime { return fr })
	ctx := context.Background()
	require.NoError(t, e.repos.Nodes.Upsert(ctx, &node.Node{PlatformID: "", CarrierID: "carrier-ghost", Role: "worker", Available: true}))
	require.NoError(t, e.repos.Nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "", CarrierID: "carrier-ghost", SHA256: node.HashRelayCredential("fltrelay_ghost"),
	}))

	setClusterNodes(rt)
	e.reconcileNodes(ctx)
	clock.Advance(e.opts.Node.LeaveGrace + time.Second)
	e.reconcileNodes(ctx)

	ghost, err := e.repos.Nodes.ByCarrier(ctx, "carrier-ghost")
	require.NoError(t, err)
	assert.True(t, ghost.Available, "an id-less row is not a departing node: the reconcile must leave it alone")
	_, err = e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential("fltrelay_ghost"))
	require.NoError(t, err, "no credential cleanup runs for an id-less row")
	assert.Empty(t, revocationAudit(t, e))
	assert.Empty(t, eventNames(t, e, ""), "no node.left is emitted for an empty platform id")
	assert.False(t, e.nodeLeftSeen[""])
}

// TestHandleNodeJoinedIgnoresUnanchoredCarrier（2026-11-24）：带空平台 ID
// 的 node.joined 不落表；带 ID 的照常落行。
func TestHandleNodeJoinedIgnoresUnanchoredCarrier(t *testing.T) {
	e, _, _ := newTestEngineBuilt(t, Options{}, discardLogger(), nil, func(fr *fakeRuntime) capability.Runtime { return fr })
	ctx := context.Background()

	e.handleNodeJoined(ctx, &capability.NodeJoined{CarrierID: "carrier-fresh", Minted: true})
	rows, err := e.repos.Nodes.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "a joined event without a platform id must not create a node row")

	e.handleNodeJoined(ctx, &capability.NodeJoined{NodeID: "node-9", CarrierID: "carrier-fresh"})
	n, err := e.repos.Nodes.ByCarrier(ctx, "carrier-fresh")
	require.NoError(t, err)
	assert.Equal(t, "node-9", n.PlatformID)
}
