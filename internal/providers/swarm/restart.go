package swarm

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Restart 实现 RuntimeRestart 可选子面（IA v3 二期④）：按平台 workload ID
// 定位服务并自增 ForceUpdate——swarm 滚动重排（任务替换即重启；与 Ensure
// 的"仅 ForceUpdate 差异跳过"口径互补：显式重启必须真重排）。
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
	return fmt.Errorf("swarm restart %s: workload not found in %s", workloadID, ns)
}
