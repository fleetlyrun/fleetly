package networkpeer_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// 声明状态机（ADR-0013 附录 A.1）：declare(pending) → approve(approved,
// 落 approved_at) → revoke(revoked)；撤销后重新声明进新审批环（部分唯一
// 索引只约束非 revoked 行）。
func TestPeerLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	peers := networkpeer.New(clock)

	const (
		networkID = "01JD0NET000000000000000001"
		peerProj  = "01JD0PROJ00000000000000009"
	)
	p := &networkpeer.Peer{ID: "01JD0PEER00000000000000001", NetworkID: networkID, PeerProjectID: peerProj}
	require.NoError(t, peers.Create(ctx, db.Runner(), p))
	assert.Equal(t, networkpeer.StatePending, p.State)

	got, err := peers.Get(ctx, db.Runner(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, networkpeer.StatePending, got.State)
	assert.Empty(t, got.ApprovedAt)

	// 同 (network, peer) 活跃声明重复 → ErrAlreadyExists。
	err = peers.Create(ctx, db.Runner(), &networkpeer.Peer{ID: "01JD0PEER00000000000000002", NetworkID: networkID, PeerProjectID: peerProj})
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// 批准：CAS pending → approved。
	require.NoError(t, peers.Approve(ctx, db.Runner(), p.ID))
	got, err = peers.Get(ctx, db.Runner(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, networkpeer.StateApproved, got.State)
	assert.NotEmpty(t, got.ApprovedAt, "approval timestamp must land")

	// 再批 → ErrConflict（非 pending 前置）。
	assert.ErrorIs(t, peers.Approve(ctx, db.Runner(), p.ID), state.ErrConflict)

	// FindActive 命中批准行。
	active, err := peers.FindActive(ctx, db.Runner(), networkID, peerProj)
	require.NoError(t, err)
	assert.Equal(t, networkpeer.StateApproved, active.State)

	// 撤销 → revoked；FindActive 不再命中；重复撤销 → ErrConflict（幂等
	// 短路由调用方 Get 判态承载）。
	require.NoError(t, peers.Revoke(ctx, db.Runner(), p.ID))
	_, err = peers.FindActive(ctx, db.Runner(), networkID, peerProj)
	assert.ErrorIs(t, err, state.ErrNotFound)
	assert.ErrorIs(t, peers.Revoke(ctx, db.Runner(), p.ID), state.ErrConflict)

	// 撤销后重新声明：新行合法（新审批环）；revoked 行留档。
	re := &networkpeer.Peer{ID: "01JD0PEER00000000000000003", NetworkID: networkID, PeerProjectID: peerProj}
	require.NoError(t, peers.Create(ctx, db.Runner(), re))

	// 未知行：ErrNotFound。
	assert.ErrorIs(t, peers.Approve(ctx, db.Runner(), "01JD0PEER000000000000000FF"), state.ErrNotFound)
}

// List 过滤与游标（ADR-0026 after_* + limit）+ 挂靠方批准视图。
func TestPeerListFiltersAndCursor(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	peers := networkpeer.New(clock)

	const (
		netA  = "01JD0NET000000000000000001"
		netB  = "01JD0NET000000000000000002"
		projP = "01JD0PROJ00000000000000009"
		projQ = "01JD0PROJ00000000000000008"
	)
	rows := []*networkpeer.Peer{
		{ID: "01JD0PEER00000000000000001", NetworkID: netA, PeerProjectID: projP},
		{ID: "01JD0PEER00000000000000002", NetworkID: netA, PeerProjectID: projQ},
		{ID: "01JD0PEER00000000000000003", NetworkID: netB, PeerProjectID: projP},
	}
	for _, p := range rows {
		require.NoError(t, peers.Create(ctx, db.Runner(), p))
	}
	require.NoError(t, peers.Approve(ctx, db.Runner(), rows[0].ID))

	// 接收方视图（network 过滤）：新→旧。
	got, err := peers.List(ctx, db.Runner(), netA, "", "", 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, rows[1].ID, got[0].ID)

	// 挂靠方视图（peer project 过滤）+ after 游标翻页。
	got, err = peers.List(ctx, db.Runner(), "", projP, "", 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	page2, err := peers.List(ctx, db.Runner(), "", projP, got[0].ID, 0)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, rows[0].ID, page2[0].ID)

	// 批准视图（隔离扫描面）。
	approved, err := peers.ListApprovedByPeerProject(ctx, db.Runner(), projP)
	require.NoError(t, err)
	require.Len(t, approved, 1)
	assert.Equal(t, rows[0].ID, approved[0].ID)
}
