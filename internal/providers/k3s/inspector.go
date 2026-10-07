package k3s

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// InspectWorkloads 实现 RuntimeInspector 子面（ADR-0022 spec 对照 drift）：
// 域内载体对象快照 + 标记还原平台身份 + spec 读取（镜像/入口覆盖命令/
// 期望副本）。one-shot Pod 也在枚举面（终态 Run 的残留 spec 对照）。
func (p *Provider) InspectWorkloads(ctx context.Context, ns capability.NamespaceRef) ([]capability.WorkloadObservation, error) {
	nsName := namespaceName(ns)
	sel := labels.Set(nsSelector(ns)).AsSelector()
	out := make([]capability.WorkloadObservation, 0)
	deployments, err := p.cli.AppsV1().Deployments(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("k3s inspect %s: %w", ns, err)
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if d.Labels[labelManaged] != "true" {
			continue
		}
		out = append(out, observeFromPodSpec(d.Labels, &d.Spec.Template.Spec))
	}
	daemonsets, err := p.cli.AppsV1().DaemonSets(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("k3s inspect %s: %w", ns, err)
	}
	for i := range daemonsets.Items {
		ds := &daemonsets.Items[i]
		if ds.Labels[labelManaged] != "true" {
			continue
		}
		obs := observeFromPodSpec(ds.Labels, &ds.Spec.Template.Spec)
		obs.Replicas = 0 // Global 形态：副本语义不参与（每节点一载体）
		out = append(out, obs)
	}
	pods, err := p.cli.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("k3s inspect %s: %w", ns, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Labels[labelManaged] != "true" || ownerIsController(pod.OwnerReferences) {
			continue
		}
		out = append(out, observeFromPodSpec(pod.Labels, &pod.Spec))
	}
	return out, nil
}

// observeFromPodSpec 从载体标签与 pod spec 还原观测（字段只增；未观测
// 字段零值 = 该面无对照）。
func observeFromPodSpec(lb map[string]string, spec *corev1.PodSpec) capability.WorkloadObservation {
	gen, _ := strconv.ParseUint(lb[labelGeneration], 10, 64)
	obs := capability.WorkloadObservation{
		WorkloadID: lb[labelWorkload],
		Generation: capability.Generation(gen),
		State:      capability.WorkloadRunning,
		Replicas:   1,
	}
	if len(spec.Containers) > 0 {
		obs.Image = spec.Containers[0].Image
		obs.Command = spec.Containers[0].Command
	}
	return obs
}
