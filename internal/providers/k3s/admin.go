package k3s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Drain 把节点置为排空：cordon + 逐 pod eviction（DaemonSet 载体豁免——
// 全局形态本就每节点一份；单副本 PVC pod 驱逐后受 PV 节点亲和在同节点
// 重建，卷钉住语义不破——ADR-0052 决策 4）。
func (p *Provider) Drain(ctx context.Context, nodeID string) error {
	node, err := p.nodeByPlatformID(ctx, nodeID)
	if err != nil {
		return err
	}
	node.Spec.Unschedulable = true
	if _, err := p.cli.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("k3s drain: cordon: %w", err)
	}
	return p.evictNodePods(ctx, node.Name)
}

// Cordon 封锁节点（拒绝新调度，存量不动）。
func (p *Provider) Cordon(ctx context.Context, nodeID string) error {
	node, err := p.nodeByPlatformID(ctx, nodeID)
	if err != nil {
		return err
	}
	node.Spec.Unschedulable = true
	if _, err := p.cli.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("k3s cordon: %w", err)
	}
	return nil
}

// Uncordon 解除封锁。
func (p *Provider) Uncordon(ctx context.Context, nodeID string) error {
	node, err := p.nodeByPlatformID(ctx, nodeID)
	if err != nil {
		return err
	}
	node.Spec.Unschedulable = false
	if _, err := p.cli.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("k3s uncordon: %w", err)
	}
	return nil
}

// nodeByPlatformID 按平台节点 ID 解析 Node（未锚定/不存在映射
// capability.ErrNodeNotFound 哨兵——API 面映射 E_NOT_FOUND）。
func (p *Provider) nodeByPlatformID(ctx context.Context, nodeID string) (*corev1.Node, error) {
	nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: labelNodeID + "=" + nodeID,
	})
	if err != nil {
		return nil, fmt.Errorf("k3s node lookup: %w", err)
	}
	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("k3s node %s: %w", nodeID, capability.ErrNodeNotFound)
	}
	return &nodes.Items[0], nil
}

// evictNodePods 驱逐节点上的平台管辖 pod（eviction API 尊重 PDB；平台
// 域单副本无 PDB，驱逐即删除重建）。非平台 pod 不动（诚实边界：跨域
// 处置是运维动作）。
func (p *Provider) evictNodePods(ctx context.Context, nodeName string) error {
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
		LabelSelector: labelManaged + "=true",
	})
	if err != nil {
		return fmt.Errorf("k3s drain: list pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if ownerIsController(pod.OwnerReferences) {
			if ref := controllerKind(pod.OwnerReferences); ref == "DaemonSet" {
				continue
			}
		}
		err := p.cli.PolicyV1().Evictions(pod.Namespace).Evict(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("k3s drain: evict %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}
	return nil
}

// controllerKind 返回 controller owner 的 Kind（空 = 无）。
func controllerKind(refs []metav1.OwnerReference) string {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return r.Kind
		}
	}
	return ""
}
