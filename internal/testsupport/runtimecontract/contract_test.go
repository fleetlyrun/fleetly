package runtimecontract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// stubRuntime 是清单模块自测用的最小 Runtime（语义按契约清单裁定：Ensure
// 幂等、scale-only 中间支保 wire-gen、Remove 幂等）。它只验证清单自身的
// 机制（通过与失败路径），不是 B3b 的 Fake 侧——Fake 侧是 apitest 与引擎
// 白盒测试里真实在用的假底座，各自包内调 Run；真机侧由
// dind-runtime-contract.sh 对真 swarm 执行同一清单（守卫对账同名）。
type stubRuntime struct {
	mu    sync.Mutex
	specs map[string]capability.Workload // service name → 期望态
	gens  map[string]capability.Generation
	calls int
	// failEnsure 非空时 Ensure 失败（失败路径注入）。
	failEnsure error
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
	if s.failEnsure != nil {
		return capability.EnsureReport{}, s.failEnsure
	}
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

// Runtime 接口其余方法是清单外消费面（Watch/DescribeCluster/Enrollment）：
// 清单不触达，显式返回「未被清单使用」错误——清单若开始依赖它们即显式
// 失败，而不是静默通过。
func (s *stubRuntime) Watch(context.Context) (<-chan capability.WorkloadEvent, error) {
	return nil, fmt.Errorf("stub: watch not used by the contract checklist")
}

func (s *stubRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	return capability.ClusterView{}, fmt.Errorf("stub: describe cluster not used by the contract checklist")
}

func (s *stubRuntime) Enrollment(context.Context, bool, capability.EnrollmentOptions) (capability.EnrollKit, error) {
	return capability.EnrollKit{}, fmt.Errorf("stub: enrollment not used by the contract checklist")
}

// TestRunContractOnStub：清单对 stub Runtime 全过（清单模块自测）。
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

// TestRunEReportsFailingCase：任一用例失败即返回带用例名的错误（清单失败
// 路径；e2e 腿以非零退出呈现）。
func TestRunEReportsFailingCase(t *testing.T) {
	rt := newStubRuntime()
	rt.failEnsure = errors.New("injected ensure failure")
	err := RunE(context.Background(), rt, Spec{
		NS:       capability.NamespaceRef{Team: "t", Project: "p", App: "contract-app"},
		Workload: capability.Workload{ID: "01jcontractfn0000000000000", Process: "handler", Image: "img:1", Replicas: 1},
	})
	if err == nil || !strings.Contains(err.Error(), CaseEnsureIdempotent) {
		t.Fatalf("expected the failing case name in the error, got %v", err)
	}
}
