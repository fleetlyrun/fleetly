package engine

// 引擎自身健康面（R02-M7 / R16-L3）：单写者循环的终态死亡与收敛能力
//（Runtime Inspector 扩展接口）都不得静默——生命周期 runner 在 panic 后
// 有界退避重启（瞬时失败只记日志与 Loop.Restarts 计数），重启预算耗尽才
// markRingDead 记账；HealthComponents 汇入控制面健康聚合（GetStatus
// components；doctor 消费其中的不健康行），DeadRings 供进程内查询与测试。
// 进程不因单循环失败崩溃（不弄死整个控制面）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ringNames 返回引擎管辖的循环名序（循环表序 + drift 扫描循环殿后——drift
// 是独立 goroutine 但同属单写者收敛生命期）。
func (e *Engine) ringNames() []string {
	names := make([]string, 0, len(e.rings)+1)
	for _, r := range e.rings {
		names = append(names, r.name)
	}
	return append(names, e.driftLoop.Name())
}

// markRingDead 记一条终态循环失败（幂等；日志 + 健康面双出口）。瞬时
// panic 重启不落账——只有重启预算耗尽（或循环意外退出）才走到这里。
func (e *Engine) markRingDead(name string, restarts uint64) {
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
	e.log.Error("engine ring is dead: restart budget exhausted, its single-writer face is halted",
		"ring", name, "restarts", restarts)
}

// DeadRings 返回因 panic 重启预算耗尽而终态死亡的循环名（循环表序 +
// drift 尾，稳定序）。瞬时重启中的环不出现在此面。
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
//  1. 每个终态失败循环一行 unhealthy，Details 稳定为 "ring=<name> dead"
//     （瞬时 panic 重启不出行，只在日志与 Loop.Restarts 计数可见）；
//  2. 收敛能力一行（ConvergenceHealthComponent）：Inspector 扩展接口在场 =
//     healthy；缺席 = unhealthy（spec 漂移/gen 偏离/缺席检测失明）。mode
//     =all 的缺席形态由装配期 fail-fast 拦下，这里覆盖 detect 显式降级与
//     手工装配（测试/apitest）形态；
//  3. 受管 Provider 载体存活行（"managed proxy" / "managed registry" /
//     "managed logging" / "managed metrics"，Proxy → Registry → Logging →
//     Metrics 稳定序）：观测为失联/劣化/退出态即 unhealthy；未观测（watch
//     首事件前）不出行（未知不等于不健康）。Provider 的 Health() 只报自身
//     面（配置就绪/端点可达），载体存活面单源在此——受管 Proxy 死掉不再
//     "status 全绿"。
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
	// 受管 Provider 载体存活行（Health 的未观测/启动窗裁决在应用模块内，
	// 这里只做 contract.ManagedHealthRow → HealthComponent 的形状映射——不
	// 重复 Provider 自身的 Health() 行）。
	if e.manageds != nil {
		for _, row := range e.manageds.Health() {
			out = append(out, HealthComponent{Name: row.Name, Healthy: row.Healthy, Details: row.Details})
		}
	}
	return out
}
