package testsupport

// ObsDomain 是 contract.Observations 的记录桩（批 1①b 观测域句柄的模块
// 测试替身，MemoCache/MemoCaches 的观测轴同款）：归属/投影/期望记账按域内
// 锚 ID 存表，断言面走导出读法。零值不可用——经 NewObsDomain 构造。

import (
	"sync"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ObsDomain 记录归属/期望记账的全部调用（并发安全）。
type ObsDomain struct {
	mu        sync.Mutex
	owners    map[string]uint64                // 锚 ID → 最近登记 gen
	specs     map[string][]capability.Workload // 锚 ID → 投影 spec 账本
	expected  map[string]uint64                // 锚 ID → 最近期望 gen
	forgotten map[string]bool                  // 锚 ID → 已遗忘
}

// NewObsDomain 铸一枚空桩。
func NewObsDomain() *ObsDomain {
	return &ObsDomain{
		owners:    map[string]uint64{},
		specs:     map[string][]capability.Workload{},
		expected:  map[string]uint64{},
		forgotten: map[string]bool{},
	}
}

// RecordOwners 记固定锚归属登记。
func (o *ObsDomain) RecordOwners(id string, gen uint64, ws []capability.Workload) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.owners[id] = gen
}

// RecordOwnersByWorkload 记逐载体登记（锚 = 载体进程名）。
func (o *ObsDomain) RecordOwnersByWorkload(gen uint64, ws []capability.Workload) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, w := range ws {
		o.owners[w.Process] = gen
	}
}

// RecordProjected 记投影 spec 账本。
func (o *ObsDomain) RecordProjected(id string, ws []capability.Workload) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.specs[id] = append(o.specs[id], ws...)
}

// RecordExpected 记期望锚登记。
func (o *ObsDomain) RecordExpected(id string, gen uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expected[id] = gen
}

// ForgetExpected 记期望锚遗忘。
func (o *ObsDomain) ForgetExpected(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.forgotten[id] = true
	delete(o.expected, id)
}

// OwnerGenOf 返回锚 ID 最近登记的归属 gen（缺席即 false）。
func (o *ObsDomain) OwnerGenOf(id string) (uint64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	gen, ok := o.owners[id]
	return gen, ok
}

// ProjectedSpecsOf 返回锚 ID 的投影账本副本。
func (o *ObsDomain) ProjectedSpecsOf(id string) []capability.Workload {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]capability.Workload, len(o.specs[id]))
	copy(out, o.specs[id])
	return out
}

// ExpectedGenOf 返回锚 ID 最近登记的期望 gen（缺席即 false）。
func (o *ObsDomain) ExpectedGenOf(id string) (uint64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	gen, ok := o.expected[id]
	return gen, ok
}

// ForgottenOf 返回锚 ID 是否被显式遗忘。
func (o *ObsDomain) ForgottenOf(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.forgotten[id]
}

// 桩实现契约（编译期断言）。
var _ contract.Observations = (*ObsDomain)(nil)
