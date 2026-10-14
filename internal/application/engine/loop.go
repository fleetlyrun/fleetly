// Package engine 承载单写者收敛循环（架构 §2）：Deployment 状态机驱动、
// Runtime Watch 消费、Spec→Workload 投影。四件一拍（状态 CAS + Outbox +
// 审计，部署记录无 tombstone）经 state.Tx 组合；决策只读平台权威表，
// 观测缓存不参与决策（参与决策前必直读）。
//
// 循环骨架纪律（架构 §0 修正 2："每段只许有一份"）：kick/tick 驱动的
// 收敛循环一律复用本包 loop.go 的 Loop，不得自建 for-select 骨架（守卫
// 见 internal/guards：time.NewTicker 在 engine 内只许出现在 loop.go）。
package engine

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Loop 是单写者收敛循环骨架（全仓唯一一份）：kick 或 tick 唤醒一次
// step；step 错误由调用方在闭包内处置（记日志后返回，下次唤醒重试）；
// panic 恢复后终止本循环并关闭 done——单写者死亡必须显式可见（日志
// ERROR + done 信号），不静默吞掉也不弄死整个控制面。
type Loop struct {
	name string
	log  *slog.Logger
	kick chan struct{}
	done chan struct{}
	// doneOnce 守护 done 的单次关闭（B12 P3-6 修面）：Stop 后重启会再次
	// 进入 Run（Start 见 cancel=nil 放行），裸 close 二次触发 panic——
	// done 观察面只保首任循环的关闭信号（当前无消费方）。
	doneOnce sync.Once
}

// NewLoop 构造循环（name 供日志与观测面）。
func NewLoop(name string, log *slog.Logger) *Loop {
	return &Loop{
		name: name,
		log:  log,
		kick: make(chan struct{}, 1), // 容量 1：重复投递合并
		done: make(chan struct{}),
	}
}

// Kick 非阻塞唤醒（合并；admission 入队与 Watch 观测到达后调用）。
func (l *Loop) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Done 在循环退出（ctx 取消或 panic 终止）后关闭。
func (l *Loop) Done() <-chan struct{} { return l.done }

// Run 阻塞驱动直至 ctx 取消。tick 是兜底节拍（观察窗到期、超时看门狗
// 靠它推进；kick 提供事件驱动的即时路径）。优雅退出契约：ctx 取消后
// 当前 step 收尾（in-flight Ensure 完成或被其内部 ctx 取消）即返回，
// 不再开始新 step——重启后按 Generation 幂等重放（领域模型场景 1）。
func (l *Loop) Run(ctx context.Context, tick time.Duration, step func(context.Context)) {
	defer l.doneOnce.Do(func() { close(l.done) })
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.kick:
		case <-ticker.C:
		}
		if !l.runStep(ctx, step) {
			return
		}
	}
}

// runStep 执行一次收敛步；返回 false 表示循环应终止（panic 已恢复）。
func (l *Loop) runStep(ctx context.Context, step func(context.Context)) (alive bool) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("engine loop terminated after panic",
				"loop", l.name, "panic", r, "stack", string(debug.Stack()))
			alive = false
		}
	}()
	step(ctx)
	return true
}
