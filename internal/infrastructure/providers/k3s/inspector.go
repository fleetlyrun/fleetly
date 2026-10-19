package k3s

import (
	"context"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
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
		// 副本数是 engine spec 对照的决策字段（drift.go 逐字段比对）：
		// 必须读 Deployment 的期望副本（R06-M2——硬编码 1 会让 replicas≥2
		// 的 App 在每次重启/收敛状态重置后产出假 drift + 假收敛审计）。
		out = append(out, observeFromPodSpec(d.Labels, &d.Spec.Template.Spec, deploymentReplicas(d)))
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
		// Global 形态：副本语义不参与（每节点一载体，DaemonSet spec 无
		// replicas 字段）。记 0 是文档化取舍——Global 只用于受管域，受管域
		// 不落 ensuredSpec（observdomain.recordOwners），副本面无从对照。
		out = append(out, observeFromPodSpec(ds.Labels, &ds.Spec.Template.Spec, 0))
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
		out = append(out, observeFromPodSpec(pod.Labels, &pod.Spec, 1))
	}
	return out, nil
}

// deploymentReplicas 读 Deployment 的期望副本（k8s 默认 1 的裸对象兜底：
// apiserver 会补默认值，fake/手工对象可能缺席——缺席不得当 0，否则
// scale-to-zero 与"未设值"会被混为一谈而产生反向假 drift）。
func deploymentReplicas(d *appsv1.Deployment) int64 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return int64(*d.Spec.Replicas)
}

// observeFromPodSpec 从载体标签与 pod spec 还原观测（字段只增；未观测
// 字段零值 = 该面无对照）。replicas 是期望副本（Provider 载体形态还原，
// 非实时 pod 计数）。
func observeFromPodSpec(lb map[string]string, spec *corev1.PodSpec, replicas int64) capability.WorkloadObservation {
	gen, _ := strconv.ParseUint(lb[labelGeneration], 10, 64)
	obs := capability.WorkloadObservation{
		WorkloadID: lb[labelWorkload],
		Generation: capability.Generation(gen),
		State:      capability.WorkloadRunning,
		Replicas:   replicas,
	}
	if len(spec.Containers) > 0 {
		obs.Image = spec.Containers[0].Image
		obs.Command = spec.Containers[0].Command
	}
	return obs
}
