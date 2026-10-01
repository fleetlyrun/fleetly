package fleetlygrpc

// SSE 一次性票据面单测（ADR-0026）：单用途、秒级 TTL、惰性清理。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

func TestEventTicketSingleUse(t *testing.T) {
	db, _ := statertest.New(t)
	store := newEventTicketStore(db.Clock())

	ticket, ttl, err := store.issue()
	require.NoError(t, err)
	assert.NotEmpty(t, ticket)
	assert.Equal(t, eventTicketTTL, ttl)

	require.True(t, store.redeem(ticket), "first redemption must succeed")
	assert.False(t, store.redeem(ticket), "second redemption must fail (single-use)")
	assert.False(t, store.redeem(""), "empty ticket must fail")
	assert.False(t, store.redeem("bogus"), "unknown ticket must fail")
}

func TestEventTicketExpiry(t *testing.T) {
	db, _ := statertest.New(t)
	clock := db.Clock().(*statertest.FakeClock)
	store := newEventTicketStore(db.Clock())

	ticket, _, err := store.issue()
	require.NoError(t, err)
	clock.Advance(eventTicketTTL + time.Second)
	assert.False(t, store.redeem(ticket), "an expired ticket must fail")
	// 过期票据经铸造路径惰性清理（sweepLocked）。
	store.mu.Lock()
	assert.Empty(t, store.tickets, "expired tickets are swept lazily on issue")
	store.mu.Unlock()
}
