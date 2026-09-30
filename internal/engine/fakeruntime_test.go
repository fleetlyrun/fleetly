package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeRuntime 是 hermetic 测试假底座（架构 §11：真 SQLite + 假底座 + 假
// 时钟）：记录 Ensure 调用、可编程失败、观测事件由测试手动注入。
type fakeRuntime struct {
	mu sync.Mutex

	ensures  []ensureCall
	failNext bool // 下一次 Ensure 失败（一次性注入；消费后自动清除）
	removed  []capability.NamespaceRef

	obsCh chan capability.WorkloadEvent

	endpoints map[string][]capability.Endpoint // ns → 后端地址（Route 解析面）

	health capability.HealthReport
}

type ensureCall struct {
	NS   capability.NamespaceRef
	Gen  capability.Generation
	Spec map[string]capability.Workload // process → workload
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		obsCh:     make(chan capability.WorkloadEvent, 64),
		endpoints: map[string][]capability.Endpoint{},
	}
}

func (f *fakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindRuntime, Version: "test"}
}

func (f *fakeRuntime) Health(context.Context) capability.HealthReport { return f.health }

func (f *fakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, _ capability.Materials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures = append(f.ensures, ensureCall{
		NS: ns, Gen: gen,
		Spec: func() map[string]capability.Workload {
			m := make(map[string]capability.Workload, len(ws))
			for _, w := range ws {
				m[w.Process] = w
			}
			return m
		}(),
	})
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("injected ensure failure")
	}
	return nil
}

func (f *fakeRuntime) Remove(_ context.Context, ns capability.NamespaceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

func (f *fakeRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return f.obsCh, nil
}

func (f *fakeRuntime) Addresses(_ context.Context, ns capability.NamespaceRef) ([]capability.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endpoints[ns.String()], nil
}

func (f *fakeRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmmanager", Role: "manager", Available: true,
	}}}, nil
}

func (f *fakeRuntime) Enrollment(context.Context) (capability.EnrollKit, error) {
	return capability.EnrollKit{Command: "fake-join"}, nil
}

// calls 返回 Ensure 调用快照。
func (f *fakeRuntime) calls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ensureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

var _ capability.Runtime = (*fakeRuntime)(nil)
