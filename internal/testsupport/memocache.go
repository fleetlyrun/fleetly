// Package testsupport 测试支撑面（元层）。本文件是 ensure cache 句柄的测试
// 替身（批 1 句柄化）：内存表 + 纯签名判据（无强制重放窗——与旧 memoBox
// 夹具语义一致），卫星测试的 contract.EnsureCache 桩单源。
package testsupport

import (
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

// MemoCache 是 contract.EnsureCache 的内存桩（Fresh 只比签名、不看重放窗
// ——旧行为锚；并发安全）。
type MemoCache struct {
	mu   sync.Mutex
	memo map[string]contract.Memo
}

// NewMemoCache 建空表桩。
func NewMemoCache() *MemoCache {
	return &MemoCache{memo: map[string]contract.Memo{}}
}

// Fresh 实现 contract.EnsureCache 读半边（存在 + 签名未变即 fresh）。
func (m *MemoCache) Fresh(key, sig string, _ time.Time) (contract.Memo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.memo[key]
	if !ok || rec.Sig != sig {
		return contract.Memo{}, false
	}
	return rec, true
}

// Remember 实现 contract.EnsureCache 写半边。
func (m *MemoCache) Remember(key string, rec contract.Memo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memo[key] = rec
}

// Forget 实现 contract.EnsureCache 清半边。
func (m *MemoCache) Forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memo, key)
}

// MemoCaches 是测试侧的 EnsureCaches 铸造桩：每柄独立内存表（dataservices
// 多表模块的 NewEnsureCache/NewAppliedCache 消费面）。
type MemoCaches struct{}

// NewEnsureCache 铸一张 memo 桩表。
func (MemoCaches) NewEnsureCache() contract.EnsureCache { return NewMemoCache() }

// NewAppliedCache 铸一张热应用摘要桩表。
func (MemoCaches) NewAppliedCache() contract.AppliedCache {
	return &AppliedMemoStub{memo: map[string]string{}}
}

// appliedMemoStub 是热应用摘要轻半边的内存桩。
type AppliedMemoStub struct {
	mu   sync.Mutex
	memo map[string]string
}

func (m *AppliedMemoStub) Remember(key, digest string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memo[key] = digest
}

func (m *AppliedMemoStub) Forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memo, key)
}

func (m *AppliedMemoStub) DigestOf(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	digest, ok := m.memo[key]
	return digest, ok
}

// NewMemoCaches 建铸造桩（MemoCaches 无状态——值接收者即可复用）。
func NewMemoCaches() contract.EnsureCaches { return MemoCaches{} }

var (
	_ contract.EnsureCache  = (*MemoCache)(nil)
	_ contract.AppliedCache = (*AppliedMemoStub)(nil)
	_ contract.EnsureCaches = MemoCaches{}
)
