package api

// 引擎自身健康面（R02-M7 / R16-L3）：单写者循环的终态死亡与收敛能力
// （Runtime Inspector 扩展接口）都不得静默——生命周期 runner 在 panic 后
// 有界退避重启（瞬时失败只记日志与 Loop.Restarts 计数），重启预算耗尽才
// markRingDead 记账；HealthComponents 汇入控制面健康聚合（GetStatus
// components；doctor 消费其中的不健康行）。
//
// 真源在本包：systemgrpc 只依赖本契约面，不 import engine 实现；engine
// 侧经别名零漂移。

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
