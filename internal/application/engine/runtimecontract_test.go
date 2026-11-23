package engine

// B3b 契约双跑的 Fake 侧（zeabur 设计 §5）：runtime 契约清单对引擎白盒
// 测试的 fakeRuntime 执行——与 apitest.FakeRuntime 同受一份清单（真机侧
// e2e/dind-runtime-contract.sh 对真 swarm 执行同一 RunE；两侧同名对账由
// runtimecontractguard 执法）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/runtimecontract"
)

func TestRuntimeContractOnFakeRuntime(t *testing.T) {
	runtimecontract.Run(t, newFakeRuntime(), runtimecontract.Spec{
		NS: capability.NamespaceRef{Team: "t", Project: "p", App: "contract-app"},
		Workload: capability.Workload{
			ID: "01jcontractfn0000000000000", Process: "handler",
			Image: "img:1", Replicas: 1, Restart: capability.RestartAlways,
			Ports: []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolHTTP}},
		},
	})
}
