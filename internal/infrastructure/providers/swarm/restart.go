package swarm

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Restart 实现 RuntimeRestart 可选子面（IA v3 二期④）：按平台 workload ID
// 定位服务并自增 ForceUpdate——swarm 滚动重排（任务替换即重启；与 Ensure
// 的"仅 ForceUpdate 差异跳过"口径互补：显式重启必须真重排）。
//
// 零命中 = 未找到（capability.ErrRestartTargetNotFound）：代次化载体 ID
// （BG 进程）由引擎按基线 Generation 解析后传入，裸 ID 未命中不得静默
// （R02-M3）。
func (p *Provider) Restart(ctx context.Context, ns capability.NamespaceRef, workloadID string) error {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return fmt.Errorf("swarm restart %s: %w", workloadID, err)
	}
	for _, svc := range services {
		if svc.Spec.Labels[labelWorkload] != workloadID {
			continue
		}
		spec := svc.Spec
		spec.TaskTemplate.ForceUpdate++
		if _, err := p.cli.ServiceUpdate(ctx, svc.ID, client.ServiceUpdateOptions{
			Version: svc.Version,
			Spec:    spec,
		}); err != nil {
			return fmt.Errorf("swarm restart %s: force update: %w", workloadID, err)
		}
		return nil
	}
	return fmt.Errorf("swarm restart %s: workload not found in %s: %w", workloadID, ns, capability.ErrRestartTargetNotFound)
}
