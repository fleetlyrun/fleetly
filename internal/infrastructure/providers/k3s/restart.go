package k3s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Restart 实现 RuntimeRestart 可选子面（IA v3 二期④）：按平台 workload ID
// 驱逐 pod——Deployment/StatefulSet 自愈重建即重启（副本逐个替换）。
//
// 零命中 = 未找到（R02-M3）：旧实现直接 DeleteCollection，选择器匹配 0 个
// 对象也返回 nil（agent 面假成功）。先按同一选择器列表取得命中集，空集即
// capability.ErrRestartTargetNotFound（引擎映射既有 E_NOT_FOUND 语义，与
// 未知进程同码）；命中即 DeleteCollection（服务端集合删除语义不变，RBAC
// 面同前）。
func (p *Provider) Restart(ctx context.Context, ns capability.NamespaceRef, workloadID string) error {
	nsName := namespaceName(ns)
	sel := labels.Set(map[string]string{labelWorkload: workloadID}).AsSelector()
	pods, err := p.cli.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return fmt.Errorf("k3s restart %s: list pods: %w", workloadID, err)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("k3s restart %s: workload not found in %s: %w", workloadID, ns, capability.ErrRestartTargetNotFound)
	}
	if err := p.cli.CoreV1().Pods(nsName).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: sel.String()}); err != nil {
		return fmt.Errorf("k3s restart %s: %w", workloadID, err)
	}
	return nil
}
