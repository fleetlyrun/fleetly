package apitest

// FakeRuntime 的 RuntimeVolumePurge 假面（B7，ADR-0094 决策 5 升级正典③
// volumes purge）：状态字段挂 FakeRuntime（harness.go 的结构性拆分——
// sizeguard 文件预算）。载体集 = 卷键 → 容量字节的在场映射（在场即存在、
// 值即容量）；purge 删键 + 记流水。RuntimeVolumeInspection 假面刻意不在
// FakeRuntime 上实现（adoption 守卫维持跳过——本面只服务 purge 链路测试）。

import (
	"context"
	"sort"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// volumePurgeFakeState 是 purge 假面状态（carriers 在场映射 + 调用流水）。
type volumePurgeFakeState struct {
	mu       sync.Mutex
	carriers map[string]int64
	calls    []string
}

// SetVolumeCarriers 布置物理卷载体集（卷键 → 容量字节；nil/空 = 全部缺席
// ——purge 幂等同判的空世界）。
func (f *FakeRuntime) SetVolumeCarriers(carriers map[string]int64) {
	f.purgeState.mu.Lock()
	f.purgeState.carriers = carriers
	f.purgeState.mu.Unlock()
}

// VolumePurgeCalls 返回 purge 调用快照（卷键序稳定——golden 确定性）。
func (f *FakeRuntime) VolumePurgeCalls() []string {
	f.purgeState.mu.Lock()
	defer f.purgeState.mu.Unlock()
	out := make([]string, len(f.purgeState.calls))
	copy(out, f.purgeState.calls)
	sort.Strings(out)
	return out
}

// PurgeVolumeCarrier 实现 RuntimeVolumePurge 删除半边：删键（幂等——缺席
// no-op）+ 记流水。
func (f *FakeRuntime) PurgeVolumeCarrier(_ context.Context, _ capability.NamespaceRef, volumeKey string) error {
	f.purgeState.mu.Lock()
	defer f.purgeState.mu.Unlock()
	delete(f.purgeState.carriers, volumeKey)
	f.purgeState.calls = append(f.purgeState.calls, volumeKey)
	return nil
}

// VolumeCarrierUsage 实现 RuntimeVolumePurge 容量半边：在场映射透传
// （缺席 = (0, false, nil)）。
func (f *FakeRuntime) VolumeCarrierUsage(_ context.Context, _ capability.NamespaceRef, volumeKey string) (int64, bool, error) {
	f.purgeState.mu.Lock()
	defer f.purgeState.mu.Unlock()
	if f.purgeState.carriers == nil {
		return 0, false, nil
	}
	bytes, ok := f.purgeState.carriers[volumeKey]
	return bytes, ok, nil
}
