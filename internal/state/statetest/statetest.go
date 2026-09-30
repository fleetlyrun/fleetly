// Package statetest 是 state 层的 hermetic 测试夹具（架构 §11 自制
// hermetic：真 SQLite + 假底座 + 假时钟）：temp 目录真库 + 可推进假时钟，
// golden 与竞态测试据此获得确定性时间戳。
package statetest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// FakeClock 是可控墙钟（并发安全；Advance 单调推进）。
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// fixedOrigin 是假时钟固定起点（golden 时间戳确定性锚）。
const fixedOrigin = "2026-01-01T00:00:00Z"

// NewFakeClock 返回固定起点假时钟。
func NewFakeClock() *FakeClock {
	return &FakeClock{now: MustParse(fixedOrigin)}
}

// Now 返回当前假时刻（UTC）。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 推进假时钟。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// MustParse 解析 RFC3339（夹具内固定字面量，失败即测试代码错误）。
func MustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("statetest: bad fixture time " + s + ": " + err.Error())
	}
	return t
}

// New 打开 temp 真库（假时钟注入）并完成迁移；t.Cleanup 自动关闭。
func New(t testing.TB) (*state.DB, *FakeClock) {
	t.Helper()
	clock := NewFakeClock()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	db, err := state.Open(context.Background(), path, clock)
	if err != nil {
		t.Fatalf("statetest: open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("statetest: close: %v", err)
		}
	})
	return db, clock
}
