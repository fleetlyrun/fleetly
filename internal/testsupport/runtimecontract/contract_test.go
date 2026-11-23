package runtimecontract

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// stubRuntime 是清单单测的最小 Runtime 实现（语义按契约清单裁定：Ensure
// 幂等、scale-only 中间支保 wire-gen、Remove 幂等——FakeRuntime 语义的
// 契约面直译）。B3b 定稿：单测侧对本 stub 断言清单通过；真机侧由
// dind-runtime-contract.sh 对真 swarm 执行同一清单（守卫对账同名）。
type stubRuntime struct {
	mu    sync.Mutex
	specs map[string]capability.Workload // service name → 期望态
	gens  map[string]capability.Generation
	calls int
}

func newStubRuntime() *stubRuntime {
	return &stubRuntime{specs: map[string]capability.Workload{}, gens: map[string]capability.Generation{}}
}

func serviceKey(ns capability.NamespaceRef, w capability.Workload) string {
	return fmt.Sprintf("%s/%s", ns.String(), w.ID)
}

func (s *stubRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "stub", Capability: capability.KindRuntime}
}

func (s *stubRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (s *stubRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, _ capability.Materials) (capability.EnsureReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for _, w := range ws {
		key := serviceKey(ns, w)
		s.specs[key] = w
		s.gens[key] = gen
	}
	return capability.EnsureReport{}, nil
}

func (s *stubRuntime) Remove(_ context.Context, ns capability.NamespaceRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := ns.String() + "/"
	for key := range s.specs {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(s.specs, key)
			delete(s.gens, key)
		}
	}
	return nil
}

func (s *stubRuntime) Addresses(context.Context, capability.NamespaceRef, []capability.Workload) ([]capability.Endpoint, error) {
	return nil, nil
}

// 其余 Runtime 方法是清单外消费面（Watch/网络维护/节点管理/exec/log/中继/
// 排空/清扫/卷键控）：embed capability.Runtime 让未实现方法 panic（清单
// 不触即死代码面可见），清单触达的方法显式实现如上。
func (s *stubRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return nil, fmt.Errorf("stub: watch not used by the contract checklist")
}

func (s *stubRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	return capability.ClusterView{}, fmt.Errorf("stub: describe cluster not used by the contract checklist")
}

func (s *stubRuntime) Enrollment(context.Context, bool, capability.EnrollmentOptions) (capability.EnrollKit, error) {
	return capability.EnrollKit{}, fmt.Errorf("stub: enrollment not used by the contract checklist")
}

func (s *stubRuntime) callsTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestRunContractOnStub：清单对 stub Runtime 全过（两侧同名对账的 Fake 侧）。
func TestRunContractOnStub(t *testing.T) {
	rt := newStubRuntime()
	Run(t, rt, Spec{
		NS: capability.NamespaceRef{Team: "t", Project: "p", App: "contract-app"},
		Workload: capability.Workload{
			ID: "01jcontractfn0000000000000", Process: "handler",
			Image: "img:1", Replicas: 1, Restart: capability.RestartAlways,
			Ports: []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolHTTP}},
		},
	})
	// 清单后 spec 已清（Remove 幂等兜底）。
	if len(rt.specs) != 0 {
		t.Fatalf("contract cleanup left %d carriers", len(rt.specs))
	}
}
