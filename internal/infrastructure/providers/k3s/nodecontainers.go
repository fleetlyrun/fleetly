package k3s

// 节点观测子面的 k3s 实现（RuntimeNodeInspect 契约，ADR-0053 决策 1 集中
// 形态）：pod 列表实时快照（exec 同纪律——观测缓存不参与）。节点内过滤在
// 内存做：全集群受管 pod 数量级小，且 field selector 在 fake clientset 上
// 不生效（evictNodePods 的 spec.nodeName 是真 apiserver 面）——测试面与
// 生产面同代码路径。

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// NodeContainers 实现 RuntimeNodeInspect：平台节点 ID 经节点锚定标记解析
// k8s 节点名（nodeByPlatformID），受管 pod 按 nodeName + Running 内存过滤。
// 归属取 pod 标记（workloadLabels 同构反读），行序按 (WorkloadID, Instance)
// 字典序。
func (p *Provider) NodeContainers(ctx context.Context, nodeID string) ([]capability.NodeContainerObservation, error) {
	node, err := p.nodeByPlatformID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: labelManaged + "=true",
	})
	if err != nil {
		return nil, fmt.Errorf("k3s node containers: pod list: %w", err)
	}
	rows := make([]capability.NodeContainerObservation, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != node.Name || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		rows = append(rows, nodeContainerFromPod(pod))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].WorkloadID != rows[j].WorkloadID {
			return rows[i].WorkloadID < rows[j].WorkloadID
		}
		return rows[i].Instance < rows[j].Instance
	})
	return rows, nil
}

// nodeContainerFromPod 把单条 pod 快照投影为节点容器观测行（纯函数，swarm
// nodeContainerFromTask 同构）。域主体互斥分支与 workloadLabels 同构反读；
// 标记缺席的归属字段留空——诚实呈现。镜像取首容器（平台 Workload 是单容器
// 投影，podTemplate 单容器映射）。
func nodeContainerFromPod(pod *corev1.Pod) capability.NodeContainerObservation {
	labels := pod.Labels
	row := capability.NodeContainerObservation{
		Instance:   pod.Name,
		WorkloadID: labels[labelWorkload],
		Team:       labels[labelTeam],
		Project:    labels[labelProject],
		Process:    labels[labelProcess],
	}
	switch {
	case labels[labelTask] != "":
		row.Domain = capability.NodeDomainTask
	case labels[labelDatabase] != "":
		row.Domain = capability.NodeDomainDatabase
	case labels[labelBrowse] != "":
		row.Domain = capability.NodeDomainBrowse
	case labels[labelApp] != "":
		row.Domain = capability.NodeDomainApp
		row.AppID = labels[labelApp]
	}
	if len(pod.Spec.Containers) > 0 {
		row.Image = pod.Spec.Containers[0].Image
	}
	return row
}
