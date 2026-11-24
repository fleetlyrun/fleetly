package k3s

// 节点锚定回归群（2026-11-24）：平台节点 ID 是 k8s 节点上的锚定标签，由
// 锚定扫描向平台铸造闭包取 ID 后写回。k3s Provider 曾缺 SetNodeIDMinter
// ——装配按可选接口接线、未实现者静默跳过——自 ADR-0070（2026-10-12）起
// 没有任何 k3s 节点被锚定：节点以空平台 ID 共用观测表的一行，受管组件按
// 标签钉控制面节点永不匹配。此前的测试直接给私有字段赋值，绕过了装配
// 通道，所以一直是绿的。

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TestSetNodeIDMinterAnchorsNewNode：经装配同款通道注入铸造闭包 → 新节点
// 被铸造、标签写回 k8s 节点、视图带平台 ID；再次对账复用已写回的标签，
// 不二次铸造。
func TestSetNodeIDMinterAnchorsNewNode(t *testing.T) {
	ctx := context.Background()
	p, cli := newFakeProvider(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}})
	var seq int
	p.SetNodeIDMinter(func(context.Context) (string, error) {
		seq++
		return fmt.Sprintf("01NODE%d", seq), nil
	})

	view, err := p.DescribeCluster(ctx)
	require.NoError(t, err)
	require.Len(t, view.Nodes, 1)
	assert.Equal(t, "01NODE1", view.Nodes[0].NodeID, "a new carrier must leave the anchoring pass with a minted platform id")
	assert.Equal(t, "node-a", view.Nodes[0].CarrierID)

	stored, err := cli.CoreV1().Nodes().Get(ctx, "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "01NODE1", stored.Labels[labelNodeID], "the minted id must be written back as the node label (managed workloads pin by it)")

	view, err = p.DescribeCluster(ctx)
	require.NoError(t, err)
	require.Len(t, view.Nodes, 1)
	assert.Equal(t, "01NODE1", view.Nodes[0].NodeID)
	assert.Equal(t, 1, seq, "a second pass must reuse the written label instead of minting again")
}

// TestDescribeClusterSkipsUnanchoredNodes：标签写回失败的载体不进视图——
// 空平台 ID 进观测表会落一行主键为空串的非法行。
func TestDescribeClusterSkipsUnanchoredNodes(t *testing.T) {
	anchored := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-ok", Labels: map[string]string{labelNodeID: "01OK"}}}
	fresh := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-fresh"}}
	p, cli := newFakeProvider(anchored, fresh)
	cli.PrependReactor("update", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected: label write-back rejected")
	})
	p.SetNodeIDMinter(func(context.Context) (string, error) { return "01FRESH", nil })

	view, err := p.DescribeCluster(context.Background())
	require.NoError(t, err, "a failed write-back is retried on the next pass; it must not fail the observation")
	require.Len(t, view.Nodes, 1, "a carrier whose label write-back failed stays out of the view until it is anchored")
	assert.Equal(t, "01OK", view.Nodes[0].NodeID)
}

// TestDescribeClusterFailsLoudlyWithoutMinter：有待锚定节点而铸造闭包未
// 注入 = 装配期接线缺失，如实带出（静默跳过曾让全部节点六周无人锚定）；
// 全部节点已锚定时不需要铸造闭包。
func TestDescribeClusterFailsLoudlyWithoutMinter(t *testing.T) {
	ctx := context.Background()
	anchored := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-ok", Labels: map[string]string{labelNodeID: "01OK"}}}

	p, _ := newFakeProvider(anchored)
	view, err := p.DescribeCluster(ctx)
	require.NoError(t, err, "a fully anchored cluster does not need the minter")
	require.Len(t, view.Nodes, 1)

	p, _ = newFakeProvider(anchored, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-fresh"}})
	_, err = p.DescribeCluster(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SetNodeIDMinter")
}

// TestUtilityVolumePreCreatesPVC（缺陷⑩后续，2026-11-24）：工具 Pod 挂平台
// 卷时 PVC 先行——新卷的卷源门探针挡在载体 Ensure 之前，PVC 只由 Ensure
// 创建即 Pending 死锁；对齐 swarm 语义（命名卷首挂即隐式创建）。幂等：
// 已有声明复用不重建。
func TestUtilityVolumePreCreatesPVC(t *testing.T) {
	ctx := context.Background()
	p, cli := newFakeProvider(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{labelNodeID: "01N1"}}})
	p.mintNodePlatformID = func(context.Context) (string, error) { return "01N1", nil }
	p.SetNodeIDMinter(p.mintNodePlatformID)
	p.SetNodeIDMinter(func(context.Context) (string, error) { return "01N1", nil })

	ns := capability.NamespaceRef{Team: "t", Project: "01PROJ"}
	req := capability.UtilityRequest{
		ID:        "volorigin-probe",
		Namespace: ns,
		Image:     "busybox:1.37",
		Argv:      []string{"true"},
		Networks:  []string{"default"},
		Volume:    &capability.UtilityVolumeMount{VolumeID: "01VOL", Target: "/seed"},
	}
	_, err := p.buildUtilityPod(ctx, req, "fleetly-01proj", "fleetly-util-volorigin-probe", "/tmp/hd")
	require.NoError(t, err)
	pvc, err := cli.CoreV1().PersistentVolumeClaims("fleetly-01proj").Get(ctx, pvcName("01VOL"), metav1.GetOptions{})
	require.NoError(t, err, "mounting a platform volume in a utility pod must pre-create the PVC")
	assert.Equal(t, "true", pvc.Labels[labelManaged], "the pre-created claim carries the managed mark (toPVC 单源)")

	// 幂等：二次装配不重建（预创建同名声明的尊重面）。
	_, err = p.buildUtilityPod(ctx, req, "fleetly-01proj", "fleetly-util-volorigin-probe", "/tmp/hd")
	require.NoError(t, err)
	got, err := cli.CoreV1().PersistentVolumeClaims("fleetly-01proj").Get(ctx, pvcName("01VOL"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, pvc.UID, got.UID, "the existing claim must be reused, not recreated")
}
