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

	ensures    []ensureCall
	failNext   bool // 下一次 Ensure 失败（一次性注入；消费后自动清除）
	removed    []capability.NamespaceRef
	blockPoint chan struct{} // 非空时 Ensure 阻塞直至关闭或 ctx 取消（hang 注入）
	// ensured 非空时每次 Ensure 入口非阻塞发信号（测试同步：探知某次
	// Ensure 已进入并停在 blockPoint）。
	ensureEntered chan struct{}

	obsCh chan capability.WorkloadEvent

	endpoints map[string][]capability.Endpoint // ns → 后端地址（Route 解析面）

	clusterOverride bool                   // 显式启用编程视图（空视图=节点全离开）
	clusterView     capability.ClusterView // 可编程集群快照（节点对账面）

	tamper map[string]tamperEntry // workloadID → 人工改载体注入（场景 7）

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

func (f *fakeRuntime) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, _ capability.Materials) error {
	if f.ensureEntered != nil {
		select {
		case f.ensureEntered <- struct{}{}:
		default:
		}
	}
	if f.blockPoint != nil {
		select {
		case <-f.blockPoint:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clusterOverride {
		return f.clusterView, nil // 空视图也是显式结果（节点全离开）
	}
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: "01JD0NODE00000000000000000", CarrierID: "swarmmanager", Role: "manager", Available: true,
	}}}, nil
}

func (f *fakeRuntime) Enrollment(context.Context, bool) (capability.EnrollKit, error) {
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

// removedSnapshot 返回 Remove 调用快照。
func (f *fakeRuntime) removedSnapshot() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.removed))
	copy(out, f.removed)
	return out
}

// InspectWorkloads 实现 RuntimeInspector 子面（ADR-0022）：观测 = 最近
// 一次 Ensure 的 spec；tamper 非空时按 workloadID 覆写（人工改载体注入）。
func (f *fakeRuntime) InspectWorkloads(_ context.Context, ns capability.NamespaceRef) ([]capability.WorkloadObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var obs []capability.WorkloadObservation
	for _, c := range f.ensures {
		if c.NS.String() != ns.String() {
			continue
		}
		for _, w := range c.Spec {
			obs = append(obs, capability.WorkloadObservation{
				WorkloadID: w.ID, Generation: c.Gen, Image: w.Image, Command: w.Command,
				Replicas: w.Replicas, State: capability.WorkloadRunning,
			})
		}
		// 只取该域最近一次 Ensure（与真 Provider 的快照语义一致）。
		obs = obs[len(obs)-len(c.Spec):]
		break
	}
	for i := range obs {
		if t, ok := f.tamper[obs[i].WorkloadID]; ok {
			if t.image != "" {
				obs[i].Image = t.image
			}
			if t.command != nil {
				obs[i].Command = t.command
			}
			if t.replicas != 0 {
				obs[i].Replicas = t.replicas
			}
		}
	}
	return obs, nil
}

// tamperEntry 是人工改载体的注入面（场景 7）。command 非 nil 即覆写
// （含覆写为空切片 = 清掉入口覆盖）。
type tamperEntry struct {
	image    string
	command  []string
	replicas int64
}

var _ capability.RuntimeInspector = (*fakeRuntime)(nil)
