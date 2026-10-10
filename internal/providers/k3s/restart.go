package k3s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Restart 实现 RuntimeRestart 可选子面（IA v3 二期④）：按平台 workload ID
// 驱逐 pod——Deployment/StatefulSet 自愈重建即重启（副本逐个替换）。
func (p *Provider) Restart(ctx context.Context, ns capability.NamespaceRef, workloadID string) error {
	sel := labels.Set(map[string]string{labelWorkload: workloadID}).AsSelector()
	if err := p.cli.CoreV1().Pods(namespaceName(ns)).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: sel.String()}); err != nil {
		return fmt.Errorf("k3s restart %s: %w", workloadID, err)
	}
	return nil
}
