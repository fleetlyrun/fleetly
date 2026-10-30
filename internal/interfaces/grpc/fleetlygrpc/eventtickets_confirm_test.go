package fleetlygrpc

// Confirm Ticket 原语单测（B1，设计稿 2026-10-30 §1.4 甲'）：peek 不删、
// discard 烧 + tombstone 短窗辨因（已确认过 vs 过期 vs 伪造）、既有三
// purpose 的 redeem 语义零改动（跨面票不误 peek 命中）、并发同票单烧。

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func confirmPayloadFor(verb, app, digest string) governance.ConfirmTicketPayload {
	return governance.ConfirmTicketPayload{
		Verb: verb, TargetApp: app, RequestDigest: digest,
		PreviewDigest: "pv-" + digest,
		Operator:      governance.ConfirmOperator{TokenID: "tok-1", TeamID: "default"},
	}
}

// peek 不删：重复 peek 恒 Hit、票面结构化 roundtrip。
func TestConfirmTicketPeekDoesNotDelete(t *testing.T) {
	db, _ := statetest.New(t)
	store := newEventTicketStore(db.Clock())

	ticket, ttl, err := store.issueConfirm(confirmPayloadFor(
		"/fleetly.delivery.v1.DeploymentsService/Deploy", "app-1", "deadbeef"))
	require.NoError(t, err)
	assert.Equal(t, confirmTicketTTL, ttl)

	for i := 0; i < 3; i++ {
		payload, status := store.PeekConfirm(ticket)
		assert.Equal(t, governance.ConfirmPeekHit, status, "peek must never delete")
		assert.Equal(t, "/fleetly.delivery.v1.DeploymentsService/Deploy", payload.Verb)
		assert.Equal(t, "app-1", payload.TargetApp)
		assert.Equal(t, "deadbeef", payload.RequestDigest)
		assert.Equal(t, "pv-deadbeef", payload.PreviewDigest)
		assert.Equal(t, "tok-1", payload.Operator.TokenID)
	}
}

// discard 烧：单用途；烧后 peek 撞 tombstone（已确认过——与过期/伪造可
// 分辨，不同辨因）；第二烧 false 且不刷新窗口；tombstone 窗过即彻底
// 遗忘（Unknown）。
func TestConfirmTicketDiscardTombstone(t *testing.T) {
	db, fake := statetest.New(t)
	store := newEventTicketStore(db.Clock())

	ticket, _, err := store.issueConfirm(confirmPayloadFor("v", "a", "d1"))
	require.NoError(t, err)

	require.True(t, store.DiscardConfirm(ticket), "a live ticket must burn")
	assert.False(t, store.DiscardConfirm(ticket), "a burned ticket must not burn again")
	_, status := store.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekConsumed, status,
		"inside the tombstone window the ticket reads as already redeemed (distinguishable from expired/forged)")

	// 窗内不刷新：再advance到窗过，第二烧没有延寿。
	fake.Advance(confirmTombstoneTTL + time.Second)
	_, status = store.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekUnknown, status,
		"past the tombstone window the token is forgotten (no redemption memory beyond the window)")

	// 伪造/不存在：Unknown。
	_, status = store.PeekConfirm("bogus-token")
	assert.Equal(t, governance.ConfirmPeekUnknown, status)
}

// 过期：在册但超 TTL → Expired（时间维度不新鲜，与伪造分腿）。
func TestConfirmTicketExpiryDistinguishable(t *testing.T) {
	db, fake := statetest.New(t)
	store := newEventTicketStore(db.Clock())

	ticket, _, err := store.issueConfirm(confirmPayloadFor("v", "a", "d2"))
	require.NoError(t, err)
	fake.Advance(confirmTicketTTL + time.Second)
	_, status := store.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekExpired, status)
	assert.False(t, store.DiscardConfirm(ticket), "an expired ticket must not burn (no tombstone for time-outs)")
}

// 既有三 purpose 的 redeem 语义零改动：confirm 票不经 redeem 兑换、
// events 票不经 PeekConfirm 命中（purpose 隔离）。
func TestConfirmTicketPurposeIsolation(t *testing.T) {
	db, _ := statetest.New(t)
	store := newEventTicketStore(db.Clock())

	eventsTicket, _, err := store.issue(ticketPurposeEvents, "")
	require.NoError(t, err)
	confirmTicket, _, err := store.issueConfirm(confirmPayloadFor("v", "a", "d3"))
	require.NoError(t, err)

	// events 票 peek confirm 面：purpose 不符 → Unknown（跨面票不可辨——
	// 沿票据店「不区分理由」纪律）。
	_, status := store.PeekConfirm(eventsTicket)
	assert.Equal(t, governance.ConfirmPeekUnknown, status)

	// confirm 票不经 redeem（单步兑换面不消费两步票）。
	assert.False(t, store.redeem(ticketPurposeEvents, "", confirmTicket), "a confirm ticket must not be redeemable through the single-step face")
	_, status = store.PeekConfirm(confirmTicket)
	assert.Equal(t, governance.ConfirmPeekHit, status, "the failed redeem must not burn the confirm ticket")

	// events 票照常单步兑换（既有语义零改动）。
	assert.True(t, store.redeem(ticketPurposeEvents, "", eventsTicket))
}

// 并发同票：discard 单烧原子——恰一 true。
func TestConfirmTicketConcurrentDiscardSingleWinner(t *testing.T) {
	db, _ := statetest.New(t)
	store := newEventTicketStore(db.Clock())

	ticket, _, err := store.issueConfirm(confirmPayloadFor("v", "a", "d4"))
	require.NoError(t, err)

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = store.DiscardConfirm(ticket)
		}(i)
	}
	wg.Wait()
	var winners int
	for _, r := range results {
		if r {
			winners++
		}
	}
	assert.Equal(t, 1, winners, "exactly one concurrent discard may burn the ticket")
}
