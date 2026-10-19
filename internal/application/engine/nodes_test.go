package engine

// H10 节点生命周期清理单测（§4.1/§4.9）：node.left 先清安全材料（个体 relay
// 凭证行 + 在连中继）再发事件；事件失败不阻断清理；仅实际删行才落
// node.relay_credential_revoked（reason=node_left）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// departedWorker 布置一台已离开集群的 worker：观测行 + 个体凭证 + 在连
// 中继（exec 面白盒注入，与 exec 域单测同款夹具）。
func departedWorker(t *testing.T) *Engine {
	t.Helper()
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	e.execFace = fakeExecFace{}
	require.NoError(t, e.repos.Nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "worker", Available: true,
	}))
	seedRelayCredential(t, e, "node-1", "carrier-1", testRelayCredential)
	_, detach, err := e.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err)
	t.Cleanup(detach)
	require.Len(t, e.RelayStatuses(), 1)

	// 快照切空 = 节点显式离开（空视图也是显式结果）。
	rt.mu.Lock()
	rt.clusterOverride = true
	rt.clusterView = capability.ClusterView{}
	rt.mu.Unlock()

	return e
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

// TestNodeLeftRevokesRelayCredential：node.left 清理凭证 + 断中继 + 审计
// reason=node_left，且 node.left 事件照发、观测缓存照常下线。
func TestNodeLeftRevokesRelayCredential(t *testing.T) {
	e := departedWorker(t)
	e.reconcileNodes(context.Background())
	assertDepartedRelayClean(t, e)
}

// TestNodeLeftRevokesEvenWhenEventFails（"事件失败仍清理"验收锚）：事件面
// 故障（outbox 表不可写）时安全材料清理与观测缓存下线不受影响——清理不
// 得挂在事件 continue 之后。审计面（audit 表）仍在，故吊销事实可见。
func TestNodeLeftRevokesEvenWhenEventFails(t *testing.T) {
	e := departedWorker(t)
	// 注入事件面故障：outbox 改名 → StreamOnly 事件 Append 失败；audit 表
	// 不受影响（LedgerOnly 只触碰取证面）。
	_, err := e.db.Runner().ExecContext(context.Background(),
		`ALTER TABLE outbox RENAME TO outbox_unavailable`)
	require.NoError(t, err)

	e.reconcileNodes(context.Background())

	ctx := context.Background()
	_, err = e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(testRelayCredential))
	require.ErrorIs(t, err, state.ErrNotFound, "credential cleanup must not depend on the node.left event")
	assert.Empty(t, e.RelayStatuses(), "relay drop must not depend on the node.left event")
	assert.Len(t, revocationAudit(t, e), 1, "the ledger row must survive an event-side failure")

	n, err := e.repos.Nodes.Get(ctx, "node-1")
	require.NoError(t, err)
	assert.False(t, n.Available, "the unavailable mark must still run when the event fails")
}
