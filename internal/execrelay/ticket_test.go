package execrelay

// 一次性 ticket 单测：绑定、一次性语义（重放拒绝）、过期（注入缩短 TTL）
// 与水位清扫。

import (
	"errors"
	"testing"
	"time"
)

func TestTicketCreateAndRedeem(t *testing.T) {
	ts := NewTicketStore(0)
	b := ts.Create("tok1", "demo", "web")
	if b.Ticket == "" || b.TokenID != "tok1" || b.App != "demo" || b.Service != "web" {
		t.Fatalf("binding mismatch: %+v", b)
	}
	got, err := ts.Redeem(b.Ticket)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got.TokenID != "tok1" || got.App != "demo" || got.Service != "web" {
		t.Fatalf("redeemed binding mismatch: %+v", got)
	}
	// 一次性：二连必须拒（重放拒绝——安全面验收原文）。
	if _, err := ts.Redeem(b.Ticket); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("second redeem err = %v, want ErrTicketUsed", err)
	}
}

func TestTicketExpiry(t *testing.T) {
	ts := NewTicketStore(30 * time.Millisecond)
	b := ts.Create("tok1", "demo", "web")
	time.Sleep(50 * time.Millisecond)
	if _, err := ts.Redeem(b.Ticket); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("expired redeem err = %v, want ErrTicketExpired", err)
	}
}

func TestTicketSweepOnCreate(t *testing.T) {
	ts := NewTicketStore(5 * time.Millisecond)
	first := ts.Create("tok1", "demo", "web")
	time.Sleep(20 * time.Millisecond)
	_ = ts.Create("tok1", "demo", "api") // 触发惰性清扫
	if _, err := ts.Redeem(first.Ticket); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("swept ticket err = %v, want ErrTicketUsed (absent from table)", err)
	}
	if ts.Len() != 1 {
		t.Fatalf("table size = %d, want 1", ts.Len())
	}
}

func TestTicketUniqueness(t *testing.T) {
	ts := NewTicketStore(0)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		b := ts.Create("tok", "demo", "web")
		if seen[b.Ticket] {
			t.Fatalf("duplicate ticket %q at iteration %d", b.Ticket, i)
		}
		seen[b.Ticket] = true
	}
}
