package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func TestOutboxMonotonicSeq(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	events := outbox.New(clock)

	var last int64
	for i := 0; i < 3; i++ {
		seq, err := events.Append(ctx, db.Runner(), "deployment.queued", "deployment",
			"01JD0DEPLOY0000000000000000"+string(rune('0'+i)), []byte(`{}`))
		require.NoError(t, err)
		require.Greater(t, seq, last, "seq must be monotonically increasing")
		last = seq
	}

	// 未注册事件名落库即拒绝（注册表三链咬合的运行时面）。
	_, err := events.Append(ctx, db.Runner(), "deployment.nope", "deployment", "x", nil)
	require.ErrorContains(t, err, "not registered")

	after, err := events.ListAfter(ctx, db.Runner(), 1, 10)
	require.NoError(t, err)
	assert.Len(t, after, 2)
	assert.Equal(t, int64(2), after[0].Seq)

	seq, err := events.LastSeq(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, int64(3), seq)

	// 保留窗（ADR-0026）：earliest_seq 划界 + 窗外回收（"只增"修订为
	// "窗内只增"）；窗内行不受 trim 影响。
	earliest, err := events.EarliestSeq(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, int64(1), earliest)

	clock.Advance(8 * 24 * time.Hour)
	n, err := events.TrimBefore(ctx, db.Runner(), clock.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "all three rows are outside the 7d window")
	earliest, err = events.EarliestSeq(ctx, db.Runner())
	require.NoError(t, err)
	assert.Zero(t, earliest, "empty table reports earliest 0 (not a gap)")
}

func TestAuditAppendList(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	audits := audit.New(clock)

	require.NoError(t, audits.Append(ctx, db.Runner(), &audit.Entry{
		ID: "01JD0AUDIT00000000000000000", Actor: "ops", Source: audit.SourceCLI,
		Action: "project.create", Resource: "project/01JD0PROJ00000000000000000",
		AfterFP: "sha256:aa",
	}))
	list, err := audits.List(ctx, db.Runner(), 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "cli", string(list[0].Source))
	assert.Equal(t, "2026-01-01T00:00:00Z", list[0].CreatedAt)
}

func TestNodeObservationCache(t *testing.T) {
	db, clock := statertest.New(t)
	ctx := context.Background()
	nodes := node.New(clock)

	n := &node.Node{PlatformID: "01JD0NODE00000000000000000", CarrierID: "swarmabc", Hostname: "node-1", Role: "manager", Available: true}
	require.NoError(t, nodes.Upsert(ctx, db.Runner(), n))

	// 观测刷新：available 翻转、last_seen_at 推进，first_seen_at 不变。
	clock.Advance(5 * time.Second)
	n.Available = false
	require.NoError(t, nodes.Upsert(ctx, db.Runner(), n))
	got, err := nodes.Get(ctx, db.Runner(), n.PlatformID)
	require.NoError(t, err)
	assert.False(t, got.Available)
	assert.Equal(t, "2026-01-01T00:00:00Z", got.FirstSeenAt)
	assert.Equal(t, "2026-01-01T00:00:05Z", got.LastSeenAt)

	list, err := nodes.List(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, list, 1)
}
