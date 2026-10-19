package engine

// 引擎自身健康面（R02-M7 / R16-L3）：单写者环的 panic 死亡与收敛能力
// （Runtime Inspector 子面）都不得静默——supervisor 消费 Loop.Done 记账，
// HealthComponents 汇入控制面健康聚合（GetStatus components；doctor 消费
// 其中的不健康行），DeadRings 供进程内查询与测试。进程不因单环死亡崩溃
//（既有语义：环自愈靠重启，不弄死整个控制面）。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ConvergenceHealthComponent 是收敛能力健康行的组件名（doctor 按此名渲染
// 「convergence capability」行；stable contract）。
const ConvergenceHealthComponent = "convergence"

// HealthComponent 是引擎侧健康组件行（Name/Healthy/Details 直出到
// systemv1.ComponentHealth；不引入新契约面）。
type HealthComponent struct {
	Name    string
	Healthy bool
	Details string
}

// ringNames 返回引擎管辖的环名序（ring 表序 + drift 扫描环殿后——drift
// 是独立 goroutine 但同属单写者收敛生命期）。
func (e *Engine) ringNames() []string {
	names := make([]string, 0, len(e.rings)+1)
	for _, r := range e.rings {
		names = append(names, r.name)
	}
	return append(names, e.driftLoop.Name())
}

// superviseRings 消费各环 Done 面（R02-M7）：panic 终止即记 ring dead；
// ctx 取消（优雅停）不记。每个环一个观察 goroutine，随 engine 生命周期
// 计入 wg（Stop 排水覆盖）。
func (e *Engine) superviseRings(ctx context.Context) {
	watch := func(l *Loop) {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			select {
			case <-l.Done():
				if l.Panicked() {
					e.markRingDead(l.Name())
				}
			case <-ctx.Done():
			}
		}()
	}
	for _, r := range e.rings {
		watch(r.loop)
	}
	watch(e.driftLoop)
}

// markRingDead 记一条环死亡（幂等；日志 + 健康面双出口）。
func (e *Engine) markRingDead(name string) {
	e.ringMu.Lock()
	if e.ringDeaths == nil {
		e.ringDeaths = make(map[string]bool)
	}
	already := e.ringDeaths[name]
	e.ringDeaths[name] = true
	e.ringMu.Unlock()
	if already {
		return
	}
	e.log.Error("engine ring is dead after panic: its single-writer face is halted until restart", "ring", name)
}

// DeadRings 返回因 panic 终止的环名（ring 表序 + drift 尾，稳定序）。
func (e *Engine) DeadRings() []string {
	e.ringMu.Lock()
	defer e.ringMu.Unlock()
	var out []string
	for _, name := range e.ringNames() {
		if e.ringDeaths[name] {
			out = append(out, name)
		}
	}
	return out
}

// HealthComponents 返回引擎侧健康组件行（GetStatus components 的数据源，
// R02-M7 / R16-L3）：
//  1. 每个死亡环一行 unhealthy，Details 稳定为 "ring=<name> dead"；
//  2. 收敛能力一行（ConvergenceHealthComponent）：Inspector 子面在场 =
//     healthy；缺席 = unhealthy（spec 漂移/gen 偏离/缺席检测失明）。posture
//     =all 的缺席形态由装配期 fail-fast 拦下，这里覆盖 detect 显式降级与
//     手工装配（测试/apitest）形态。
func (e *Engine) HealthComponents() []HealthComponent {
	e.ringMu.Lock()
	out := make([]HealthComponent, 0, len(e.ringDeaths)+1)
	for _, name := range e.ringNames() {
		if e.ringDeaths[name] {
			out = append(out, HealthComponent{
				Name: "engine ring " + name, Healthy: false, Details: "ring=" + name + " dead",
			})
		}
	}
	e.ringMu.Unlock()

	posture := e.opts.Converge.Posture
	if capability.FacesOf(e.runtime).Inspector != nil {
		out = append(out, HealthComponent{
			Name: ConvergenceHealthComponent, Healthy: true,
			Details: "posture=" + posture + ", inspector=present",
		})
	} else {
		out = append(out, HealthComponent{
			Name: ConvergenceHealthComponent, Healthy: false,
			Details: "posture=" + posture + ", inspector=missing: spec drift, generation deviation and absence detection are disabled (drift scan never converges)",
		})
	}
	return out
}
