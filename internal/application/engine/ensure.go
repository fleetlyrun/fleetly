package engine

import (
	"context"
	"sync"
	"time"
)

// 收敛域签名短路备忘族（N1 C16/C17：每拍全量无条件重活的跳过判定面）：
// 记忆"上次成功 Ensure 的完整签名 + 时刻"，签名未变且未到强制重放节拍
// 即跳过本拍；Ensure 失败不落/清签名（下拍重试）；重启丢失首拍全量自愈。
// ensureMemo 的域内键与签名字面由各域定义（受管=namespace，Database=行
// ID，releasing=Deployment 行锚，Task=taskID，Route 发布=单槽恒键）。
// 签名构造族（域内"全部输入"的口径各自冻结，政策刻意分立不合流）：
// workloadSetSignature（Task 语义等价缩面）/ managedFingerprint（受管
// 全量保守）/ materialsFingerprint / routesFingerprint（行集内容指纹）/
// releaseEnsureSignature（行锚）。

// ensureMemo 是收敛域"上次成功 Ensure"的备忘（N1 C16/C17）：sig 是该域
// Ensure 全部输入的签名字面（域内定义，见字段处签名构造族清单），at 驱动
// 周期强制重放（签名未变也周期收敛——自愈面，ReconcileReplayInterval 统一
// 节拍），gen 备短路拍需要下发编号的消费面（Database 状态推进）。
type ensureMemo struct {
	sig string
	gen uint64
	at  time.Time
}

// ensureFresh 报告键上备忘是否存在且（签名未变 + 未到强制重放节拍）——
// 双条件同时成立才允许跳过本拍重活。
func (e *Engine) ensureFresh(memo map[string]ensureMemo, key, sig string, now time.Time) (ensureMemo, bool) {
	e.ensureMu.Lock()
	rec, ok := memo[key]
	e.ensureMu.Unlock()
	if !ok || rec.sig != sig || now.After(rec.at.Add(e.opts.Ring.ReconcileReplayInterval)) {
		return ensureMemo{}, false
	}
	return rec, true
}

// ensureRemember 落/刷键上备忘（仅成功路径调用）。
func (e *Engine) ensureRemember(memo map[string]ensureMemo, key string, rec ensureMemo) {
	e.ensureMu.Lock()
	memo[key] = rec
	e.ensureMu.Unlock()
}

// ensureForget 清键上备忘（Ensure 失败/域收口；缺失为 no-op）。
func (e *Engine) ensureForget(memo map[string]ensureMemo, key string) {
	e.ensureMu.Lock()
	delete(memo, key)
	e.ensureMu.Unlock()
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
