// ensure.go 是收敛域签名短路确保缓存的句柄实现（批 1 句柄化，设计稿
// §3.5）：表本体与互斥量未导出于句柄结构内——影子表与绕锁直写结构性
// 不可能（memoMapRegistry 登记制退役）。互斥量在所有句柄与引擎间共享
// （ensureMu 单锁拓扑零漂移）；强制重放节拍构造期自引擎选项定值。
package engine

import (
	"context"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

// ensureMemo 是确保缓存记录（contract.Memo 的域内别名；句柄化后消费面
// 直接用契约类型，别名仅存于引擎内部表形态）。
type ensureMemo = contract.Memo

// 引擎本体即 EnsureCaches 工厂（句柄铸造面；编译期断言）。
var _ contract.EnsureCaches = (*Engine)(nil)

// ensureCache 是一张 memo 表的句柄实现（contract.EnsureCache）。
type ensureCache struct {
	mu     *sync.Mutex
	memo   map[string]ensureMemo
	replay time.Duration // 强制重放节拍（构造期自引擎选项定值）
}

// Fresh 实现 contract.EnsureCache 读半边：存在 + 签名未变 + 未到强制重放
// 节拍三条件同时成立才报告 fresh（持锁读）。
func (c *ensureCache) Fresh(key, sig string, now time.Time) (contract.Memo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.memo[key]
	if !ok || rec.Sig != sig || now.After(rec.At.Add(c.replay)) {
		return contract.Memo{}, false
	}
	return rec, true
}

// Remember 实现 contract.EnsureCache 写半边（持锁写；仅成功路径调用）。
func (c *ensureCache) Remember(key string, rec contract.Memo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.memo[key] = rec
}

// Forget 实现 contract.EnsureCache 清半边（持锁删；缺失 no-op）。
func (c *ensureCache) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.memo, key)
}

// NewEnsureCache 实现 contract.EnsureCaches：铸造一张新表（互斥量与重放
// 节拍自引擎——单锁拓扑不变）。
func (e *Engine) NewEnsureCache() contract.EnsureCache {
	return &ensureCache{mu: &e.ensureMu, memo: map[string]contract.Memo{}, replay: e.opts.Ring.ReconcileReplayInterval}
}

// appliedCache 是 dynamic 旋钮热应用缓存表的句柄实现（contract.AppliedCache）。
type appliedCache struct {
	mu   *sync.Mutex
	memo map[string]string
}

// Remember 实现 contract.AppliedCache 写半边（热应用成功后、行落库同窗口
// 调用；持锁写）。
func (c *appliedCache) Remember(key, digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.memo[key] = digest
}

// Forget 实现 contract.AppliedCache 清半边（失败路径强制调用；持锁删）。
func (c *appliedCache) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.memo, key)
}

// DigestOf 实现 contract.AppliedCache 读半边（循环重放门的失配判据输入；
// 持锁读）。
func (c *appliedCache) DigestOf(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.memo[key]
	return v, ok
}

// NewAppliedCache 实现 contract.AppliedCaches。
func (e *Engine) NewAppliedCache() contract.AppliedCache {
	return &appliedCache{mu: &e.ensureMu, memo: map[string]string{}}
}

// lockTask 取 Task 级互斥（惰性建；驱动环/DeleteTask 共享）。
func (e *Engine) lockTask(taskID string) *sync.Mutex {
	mu, _ := e.task.locks.LoadOrStore(taskID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// boundedStep 给受管收敛步派生 ManagedStepTimeout 硬上限 ctx（统一入口：
// managed.go/database.go/drift.go 既有 stepCtx 形态的共用底座）。options.go
// Options.ManagedStepTimeout 注释的实证背景——docker API hang 卡死单写者
// 环，TTL janitor/租约排空/停止兜底全住环上，卡死即全部时间看门狗失明。
// 收敛步本就是失败下拍重试语义，带界无损。
func (e *Engine) boundedStep(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, e.opts.Ring.ManagedStepTimeout)
}

// lockApp 取 App 级互斥（惰性建；admission/基线重放/收口共享）。
func (e *Engine) lockApp(appID string) *sync.Mutex {
	mu, _ := e.appLocks.LoadOrStore(appID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
