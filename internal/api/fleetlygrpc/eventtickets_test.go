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

	ticket, ttl, err := store.issue(ticketPurposeEvents, "")
	require.NoError(t, err)
	assert.NotEmpty(t, ticket)
	assert.Equal(t, eventTicketTTL, ttl)

	require.True(t, store.redeem(ticketPurposeEvents, "", ticket), "first redemption must succeed")
	assert.False(t, store.redeem(ticketPurposeEvents, "", ticket), "second redemption must fail (single-use)")
	assert.False(t, store.redeem(ticketPurposeEvents, "", ""), "empty ticket must fail")
	assert.False(t, store.redeem(ticketPurposeEvents, "", "bogus"), "unknown ticket must fail")
}

func TestEventTicketExpiry(t *testing.T) {
	db, _ := statertest.New(t)
	clock := db.Clock().(*statertest.FakeClock)
	store := newEventTicketStore(db.Clock())

	ticket, _, err := store.issue(ticketPurposeEvents, "")
	require.NoError(t, err)
	clock.Advance(eventTicketTTL + time.Second)
	// 失败兑换不消费票据（ADR-0049：载荷不匹配/过期的尝试不烧票据——
	// 错会话试兑不构成对真会话的拒绝服务）；过期条目经铸造路径惰性清理。
	assert.False(t, store.redeem(ticketPurposeEvents, "", ticket), "an expired ticket must fail")
	_, _, err = store.issue(ticketPurposeEvents, "")
	require.NoError(t, err)
	store.mu.Lock()
	assert.Len(t, store.tickets, 1, "expired tickets are swept lazily on issue")
	store.mu.Unlock()
}
