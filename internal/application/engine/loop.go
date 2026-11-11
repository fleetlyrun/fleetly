// Package engine 承载单写者收敛循环（架构 §2）：Deployment 状态机驱动、
// Runtime Watch 消费、Spec→Workload 投影。四件一拍（状态 CAS + Outbox +
// 审计，部署记录无 tombstone）经 state.Tx 组合；决策只读平台权威表，
// 观测缓存不参与决策（参与决策前必直读）。
//
// 循环骨架纪律（架构 §0 修正 2："每段只许有一份"）：kick/tick 驱动的
// 收敛循环一律复用本包 loop.go 的 Loop，不得自建 for-select 骨架（契约测试
// 见 internal/guards：time.NewTicker 在 engine 内只许出现在 loop.go）。
package engine

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Loop 是单写者收敛循环骨架（全仓唯一一份）：kick 或 tick 唤醒一次
// step；step 错误由调用方在闭包内处置（记日志后返回，下次唤醒重试）；
// panic 恢复后终止当前代（generation）并关闭 done——单写者死亡必须显式
// 可见（日志 ERROR + done 信号），不静默吞掉也不弄死整个控制面。生命周期
// 监督者（engine.runLoopWithRestart）可在退避后 Reset 开新代重跑；Done/
// Panicked 只描述当前代，旧代 done 保持已关闭。
type Loop struct {
	name string
	log  *slog.Logger

	// mu 守护当前代指针与 done 的关闭态。Reset 换代与 Kick/Done 快照互斥
	// ——旧 doneOnce 无法复位，重启后 Done 会永远返回已关闭通道。
	mu  sync.Mutex
	gen *loopGeneration

	// restarts 记录 Reset 换代次数（panic 重启的观测计数；优雅停止不计）。
	restarts atomic.Uint64
}

// loopGeneration 是一代循环生命期：kick/done 通道与 panic 标记都随代
// 更换，Reset 换新代即恢复可运行状态。
type loopGeneration struct {
	kick     chan struct{}
	done     chan struct{}
	closed   bool // done 的单次关闭（mu 内读写；换代后旧代仍保持已关闭）
	panicked atomic.Bool
}

// newLoopGeneration 构造一代可运行循环（kick 容量 1：重复投递合并）。
func newLoopGeneration() *loopGeneration {
	return &loopGeneration{
		kick: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// NewLoop 构造循环（name 供日志与观测面）。
func NewLoop(name string, log *slog.Logger) *Loop {
	return &Loop{
		name: name,
		log:  log,
		gen:  newLoopGeneration(),
	}
}

// currentGeneration 快照当前代（Kick/Done/Panicked/Run 的读取面）。
func (l *Loop) currentGeneration() *loopGeneration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen
}

// Kick 非阻塞唤醒（合并；admission 入队与 Watch 观测到达后调用）。
func (l *Loop) Kick() {
	g := l.currentGeneration()
	select {
	case g.kick <- struct{}{}:
	default:
	}
}

// Done 在当前代循环退出（ctx 取消或 panic 终止）后关闭。
func (l *Loop) Done() <-chan struct{} { return l.currentGeneration().done }

// Panicked 报告当前代是否因 panic 恢复而终止（Done 关闭后有效；优雅取消
// = false）。生命周期监督者的 loop failure 判据（R02-M7）。
func (l *Loop) Panicked() bool { return l.currentGeneration().panicked.Load() }

// Restarts 返回本循环因 panic 重启（Reset 换代）的累计次数。
func (l *Loop) Restarts() uint64 { return l.restarts.Load() }

// Reset 开启新一代：重建 kick/done、清除 panic 标记，使循环可再次 Run。
// 只应在上一代 Run 已返回后调用（生命周期监督者持此纪律）；旧代 Done
// 保持已关闭，新代 Done 为未关闭的新通道。
func (l *Loop) Reset() {
	l.mu.Lock()
	l.gen = newLoopGeneration()
	l.mu.Unlock()
	l.restarts.Add(1)
}

// Name 返回循环名（诊断/log 面）。
func (l *Loop) Name() string { return l.name }

// Run 阻塞驱动直至 ctx 取消或 panic 终止当前代。tick 是兜底节拍（观察窗
// 到期、超时看门狗靠它推进；kick 提供事件驱动的即时路径）。优雅退出契约：
// ctx 取消后当前 step 收尾（in-flight Ensure 完成或被其内部 ctx 取消）即
// 返回，不再开始新 step——重启后按 Generation 幂等重放（领域模型场景 1）。
func (l *Loop) Run(ctx context.Context, tick time.Duration, step func(context.Context)) {
	g := l.currentGeneration()
	defer l.closeDone(g)
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.kick:
		case <-ticker.C:
		}
		if !l.runStep(ctx, g, step) {
			return
		}
	}
}

// closeDone 单次关闭本代 done（同一代重复 Run 不二次 close；换代后旧代
// 的关闭态不受影响）。
func (l *Loop) closeDone(g *loopGeneration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	close(g.done)
}

// runStep 执行一次收敛步；返回 false 表示本代循环应终止（panic 已恢复）。
func (l *Loop) runStep(ctx context.Context, g *loopGeneration, step func(context.Context)) (alive bool) {
	defer func() {
		if r := recover(); r != nil {
			g.panicked.Store(true)
			l.log.Error("engine loop step panicked; generation halted",
				"loop", l.name, "panic", r, "stack", string(debug.Stack()))
			alive = false
		}
	}()
	step(ctx)
	return true
}
