package k3s

// NodeContainers 单测（节点观测面）：fake clientset hermetic 钉测——节点
// 定位、nodeName+Running 内存过滤、归属域投影、排序与未知节点哨兵；投影
// 纯函数单列钉测（swarm nodeContainerFromTask 同纪律）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

const (
	ncNodePlatform = "01JD0NODE00000000000000000"
	ncNodeOther    = "01JD0NODE00000000000000001"
	ncK8sNode      = "k3s-e2e-0"
)

// ncAnchorNode 是带锚定标记的 k8s 节点夹具。
func ncAnchorNode() runtime.Object {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: ncK8sNode, Labels: map[string]string{labelNodeID: ncNodePlatform}},
	}
}

// ncPod 是 pod 夹具：节点落点 + 相位 + 归属标记 + 镜像。
func ncPod(ns, name, node string, phase corev1.PodPhase, labels map[string]string, image string) runtime.Object {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Image: image}}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func ncAppLabels() map[string]string {
	return map[string]string{
		labelManaged: "true", labelTeam: "acme", labelProject: "shop",
		labelApp: "web", labelProcess: "api", labelWorkload: "web-1",
	}
}

// TestNodeContainersFiltersSortsProjects：只列锚定节点的 Running pod；
// 行序 (WorkloadID, Instance) 字典序；归属投影齐全。
func TestNodeContainersFiltersSortsProjects(t *testing.T) {
	p, _ := newFakeProvider(
		ncAnchorNode(),
		// 他节点 Running：不计。
		ncPod("fleetly-shop", "web-elsewhere", "k3s-e2e-9", corev1.PodRunning, ncAppLabels(), "nginx:1"),
		// 本节点非 Running：不计（替换期旧 pod）。
		ncPod("fleetly-shop", "web-succeeded", ncK8sNode, corev1.PodSucceeded, ncAppLabels(), "nginx:1"),
		// 本节点 Running 两条：排序锚（db1 < web-1）。
		ncPod("fleetly-shop", "pod-b", ncK8sNode, corev1.PodRunning, ncAppLabels(), "nginx:1.27"),
		ncPod("fleetly-system", "pod-a", ncK8sNode, corev1.PodRunning, map[string]string{
			labelManaged: "true", labelDatabase: "db1", labelWorkload: "db1",
		}, "pg:16"),
	)
	rows, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.NoError(t, err)
	require.Len(t, rows, 2, "only running pods anchored to the node are listed")

	assert.Equal(t, "pod-a", rows[0].Instance)
	assert.Equal(t, "db1", rows[0].WorkloadID)
	assert.Equal(t, capability.NodeDomainDatabase, rows[0].Domain)
	assert.Equal(t, "pg:16", rows[0].Image)
	assert.Empty(t, rows[0].AppID, "non-app domains carry no app attribution")

	assert.Equal(t, "pod-b", rows[1].Instance)
	assert.Equal(t, "web-1", rows[1].WorkloadID)
	assert.Equal(t, capability.NodeDomainApp, rows[1].Domain)
	assert.Equal(t, "web", rows[1].AppID)
	assert.Equal(t, "api", rows[1].Process)
	assert.Equal(t, "acme", rows[1].Team)
	assert.Equal(t, "shop", rows[1].Project)
	assert.Equal(t, "nginx:1.27", rows[1].Image)
}

// TestNodeContainersEmptyIsHonest：节点在场但无在跑 pod → 空切片非错误。
func TestNodeContainersEmptyIsHonest(t *testing.T) {
	p, _ := newFakeProvider(ncAnchorNode())
	rows, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestNodeContainersUnknownNode：平台节点 ID 无锚定映射 → ErrNodeNotFound。
func TestNodeContainersUnknownNode(t *testing.T) {
	p, _ := newFakeProvider(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: ncK8sNode, Labels: map[string]string{labelNodeID: ncNodeOther}},
	})
	_, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.ErrorIs(t, err, capability.ErrNodeNotFound)
}

// TestNodeContainerFromPodDomains：投影纯函数的四域分支与标记缺席形态。
func TestNodeContainerFromPodDomains(t *testing.T) {
	pod := func(labels map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Labels: labels},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "img:1"}}}}
	}

	row := nodeContainerFromPod(pod(map[string]string{labelTask: "run-9", labelWorkload: "run-9"}))
	assert.Equal(t, capability.NodeDomainTask, row.Domain)
	assert.Empty(t, row.AppID)

	row = nodeContainerFromPod(pod(map[string]string{labelBrowse: "s-1", labelWorkload: "s-1"}))
	assert.Equal(t, capability.NodeDomainBrowse, row.Domain)

	row = nodeContainerFromPod(pod(map[string]string{labelDatabase: "db-1", labelWorkload: "db-1"}))
	assert.Equal(t, capability.NodeDomainDatabase, row.Domain)

	row = nodeContainerFromPod(pod(ncAppLabels()))
	assert.Equal(t, capability.NodeDomainApp, row.Domain)
	assert.Equal(t, "web", row.AppID)

	// 标记整体缺席：归属未知（域留空，不冒认）；镜像投影不受影响。
	row = nodeContainerFromPod(pod(nil))
	assert.Empty(t, row.Domain)
	assert.Empty(t, row.WorkloadID)
	assert.Equal(t, "img:1", row.Image)
}
