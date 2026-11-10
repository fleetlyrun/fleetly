package metrics

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (m *Metrics) Name() string { return "metrics" }

// Loops implements contract.Module: the scrape/alert loop.
func (m *Metrics) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "metrics", Step: m.Step}}
}

var _ contract.Module = (*Metrics)(nil)
var _ contract.Metrics = (*Metrics)(nil)

// Deps 与 contract.MetricsPorts 形状同源（字段名/类型/顺序逐一相同；双向
// 转换在编译期断言——任一侧增删/改型即编译红，组合根端口袋不会静默漂移）。
var (
	_ = func(d Deps) contract.MetricsPorts { return contract.MetricsPorts(d) }
	_ = func(p contract.MetricsPorts) Deps { return Deps(p) }
)
