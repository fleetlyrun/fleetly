package engine

// per-受管域 Generation 的重启续接测试（F2.5 修复：CI 升级零扰动锚咬出
// 的全域滚动缺陷——全局计数 + 进程重置 = Edge 标签差 = traefik 滚动）。

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestManagedGenSeedContinuity 钉续接语义：播种 → 首见同指纹沿用（不 +1）；
// 指纹变化 +1；无播种冷启 1。
func TestManagedGenSeedContinuity(t *testing.T) {
	st := &managedGenState{}
	st.seedFromRuntime(7)
	assert.Equal(t, uint64(7), st.next("fp-a"), "first-seen fingerprint adopts the seeded generation (no fake bump)")
	assert.Equal(t, uint64(7), st.next("fp-a"), "same fingerprint reuses")
	assert.Equal(t, uint64(8), st.next("fp-b"), "changed fingerprint advances")
	assert.Equal(t, uint64(9), st.next("fp-c"), "each change advances once")

	cold := &managedGenState{}
	assert.Equal(t, uint64(1), cold.next("fp-x"), "cold start (no seed) begins at 1")
	assert.Equal(t, uint64(1), cold.next("fp-x"))
	assert.Equal(t, uint64(2), cold.next("fp-y"))
}

// TestManagedGenSeedMonotonic 钉播种的保守面：只升不降（观测乱序/陈旧
// 不回退计数器）。
func TestManagedGenSeedMonotonic(t *testing.T) {
	st := &managedGenState{}
	st.seedFromRuntime(5)
	st.seedFromRuntime(3) // 陈旧观测
	assert.Equal(t, uint64(5), st.next("fp"), "stale observations never lower the counter")
}

var _ = atomic.Bool{} // 保持 import（seeded/adopted 字段的类型面）
