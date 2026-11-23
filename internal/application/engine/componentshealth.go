// componentshealth.go 是组件健康探活聚合的核导出面（B2b 共享导出——设计
// §2.4 件 4：systemgrpc 的探活聚合+TTL 缓存原为本包未导出方法，B2b 起搬
// 入 engine 单源，systemgrpc status 面与 diagnosis 事故装配双消费——消费
// 方只读快照，不得绕开缓存自行探活（复刻匿名放大）。诊断面同源：组件
// 不健康 → Platform Incident 装配（platform 的错永远不读成用户的错）。
package engine

import (
	"context"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// componentsHealthTimeout 是单项探活预算（TCP 拨号类检查自带更短超时；
// 此处兜底防慢检查拖垮 status 面）。
const componentsHealthTimeout = 2 * time.Second

// ComponentsHealthCacheTTL 是探活结果缓存窗（R08-M9 化：公开面无需凭证，
// 逐请求全量探活可被匿名放大——TTL 内复用上次结果；engine 单源后 status
// 面与事故装配共用同一缓存）。
const ComponentsHealthCacheTTL = 10 * time.Second

// componentsHealthAgg 是探活聚合实例态（TTL 缓存 + 单飞 mutex；engine 唯一
// 持有——Deps.Checkers 注入 Providers 集）。
type componentsHealthAgg struct {
	mu         sync.Mutex
	checkers   []capability.Provider
	ttl        time.Duration
	components []capability.ComponentHealth
	checkedAt  time.Time
}

// snapshot 返回 TTL 内的探活快照；未命中即探活并回写缓存。探活用脱离调用
// 方取消信号的 ctx（客户端断连不污染共享缓存）；并发请求在 mutex 上排队，
// 窗口内只跑一轮探活。
func (a *componentsHealthAgg) snapshot(ctx context.Context, now time.Time) []capability.ComponentHealth {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.components != nil && now.Sub(a.checkedAt) < a.ttl {
		return append([]capability.ComponentHealth(nil), a.components...)
	}
	out := a.probe(context.WithoutCancel(ctx), now)
	a.components = out
	a.checkedAt = now
	return append([]capability.ComponentHealth(nil), out...)
}

// probe 并行探活在册 Provider（单项预算兜底；超时/panic 按不健康计——
// 降级矩阵的诚实口径）。
func (a *componentsHealthAgg) probe(ctx context.Context, now time.Time) []capability.ComponentHealth {
	out := make([]capability.ComponentHealth, len(a.checkers))
	var wg sync.WaitGroup
	for i, provider := range a.checkers {
		if provider == nil {
			continue
		}
		wg.Add(1)
		go func(i int, provider capability.Provider) {
			defer wg.Done()
			describe := provider.Describe()
			out[i] = capability.ComponentHealth{
				Name:       describe.Name,
				Capability: string(describe.Capability),
				CheckedAt:  now,
			}
			defer func() {
				if r := recover(); r != nil {
					out[i].Healthy = false
					out[i].Details = "health check panicked"
				}
			}()
			checkCtx, cancel := context.WithTimeout(ctx, componentsHealthTimeout)
			defer cancel()
			done := make(chan capability.HealthReport, 1)
			go func() {
				done <- provider.Health(checkCtx)
			}()
			select {
			case report := <-done:
				out[i].Healthy = report.Healthy
				out[i].Details = report.Details
			case <-checkCtx.Done():
				out[i].Healthy = false
				out[i].Details = "health check timed out"
			}
		}(i, provider)
	}
	wg.Wait()
	return out
}

// ComponentsHealthSnapshot 是组件健康快照的核导出面（B2b 共享导出）：
// TTL 缓存单源——systemgrpc status 面与 diagnosis 事故装配双消费，调用方
// 只读快照不得自行探活。Checkers 缺席（手动测试装配）= 空快照。
func (e *Engine) ComponentsHealthSnapshot(ctx context.Context) []capability.ComponentHealth {
	if e.componentsHealth == nil || len(e.componentsHealth.checkers) == 0 {
		return nil
	}
	return e.componentsHealth.snapshot(ctx, e.clock.Now())
}
