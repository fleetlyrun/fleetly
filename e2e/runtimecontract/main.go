// runtimecontract 是 B3b 真 swarm 侧清单执行器（e2e/dind-runtime-contract.sh
// 消费）：连接 dind 内 docker socket 构造 swarm provider，对同一份
// runtimecontract.Run 清单执行真机契约面。清单本体在
// internal/testsupport/runtimecontract（守卫对账同名零缺席——两侧消费
// 同一 CaseNames 锚）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/providers/swarm"
	"github.com/fleetlyrun/fleetly/internal/testsupport/runtimecontract"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "runtime-contract: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 执行器在 dind 容器内运行（e2e/dind-runtime-contract.sh 经 docker exec
	// 注入）：固定走容器内 unix socket，不读环境变量——清单口径不随宿主
	// 环境漂移。
	rt, err := swarm.New(context.Background(), "unix:///var/run/docker.sock")
	if err != nil {
		return fmt.Errorf("swarm runtime: %w", err)
	}
	// 节点 ID 铸造闭包：清单不触节点管理面，但 DescribeCluster 的节点
	// ID 归一依赖 minter（swarm node id → 平台 id 直译）。
	rt.SetNodeIDMinter(func(ctx context.Context) (string, error) {
		return "contract-node", nil
	})
	ctx := context.Background()
	view, err := rt.DescribeCluster(ctx)
	if err != nil {
		return fmt.Errorf("describe cluster: %w", err)
	}
	if len(view.Nodes) == 0 {
		return fmt.Errorf("cluster has no nodes")
	}

	ns := capability.NamespaceRef{Team: "fleetly", Project: "runtime-contract", App: "probe"}
	spec := runtimecontract.Spec{
		NS: ns,
		Workload: capability.Workload{
			ID:      "01jrcontractfn00000000000000",
			Process: "handler",
			Image:   "alpine:3",
			Command: []string{"/bin/sh", "-c", "sleep infinity"},
			// RestartAlways：清单 Ensure 面对常驻载体断言（running 才算
			// 探针就绪；RestartNever+自退命令会让 Ensure 轮询挂死——
			// dind 实证 2026-11-23）。生命周期由清单收尾 Remove 承担。
			Restart:  capability.RestartAlways,
			Replicas: 1,
			Ports:    []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolTCP}},
		},
	}
	if err := runtimecontract.RunE(ctx, rt, spec); err != nil {
		return err
	}
	fmt.Println("runtime contract: ALL GREEN")
	return nil
}
