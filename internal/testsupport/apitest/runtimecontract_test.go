package apitest

// B3b 契约双跑的 Fake 侧（zeabur 设计 §5）：runtime 契约清单对 apitest
// FakeRuntime 执行——全部 interfaces/CLI 端到端测试共用的假 Runtime 必须
// 满足与真 swarm 同一份清单（Ensure/Remove 幂等、scale-only 零替换、代戳
// 恒同移）；假底座语义漂移在此红，而不是在依赖它的几百个用例里变成
// 「测试过了但生产不对」。真机侧由 e2e/dind-runtime-contract.sh 对真
// swarm 执行同一 RunE；两侧同名对账由 runtimecontractguard 执法。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/runtimecontract"
)

func TestRuntimeContractOnFakeRuntime(t *testing.T) {
	rt := &FakeRuntime{obs: make(chan capability.WorkloadEvent, 64)}
	runtimecontract.Run(t, rt, runtimecontract.Spec{
		NS: capability.NamespaceRef{Team: "t", Project: "p", App: "contract-app"},
		Workload: capability.Workload{
			ID: "01jcontractfn0000000000000", Process: "handler",
			Image: "img:1", Replicas: 1, Restart: capability.RestartAlways,
			Ports: []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolHTTP}},
		},
	})
}
