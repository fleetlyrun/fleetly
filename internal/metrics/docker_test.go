package metrics

// readyNodeAddresses 投影矩阵单测（§6 挂账票动态 targets 的节点注册表读
// 面——真实现 moby NodeList 的纯投影函数；hermetic，不触底座）：ready 且
// active 入选、down/pause/drain 排除、Status.Addr 缺省回落
// ManagerStatus.Addr、无地址 Ready 节点如实跳过、底座原序保留（排序在
// spec 层 scrapeAddrs）。

import (
	"slices"
	"testing"

	"github.com/moby/moby/api/types/swarm"
)

// TestReadyNodeAddressesProjection 投影矩阵：混合状态/可用性/地址形态的
// 节点清单 → 可直连的 advertise 地址集。
func TestReadyNodeAddressesProjection(t *testing.T) {
	nodes := []swarm.Node{
		// manager：ready+active，Status.Addr 在——入选。
		{ID: "n1", Status: swarm.NodeStatus{State: swarm.NodeStateReady, Addr: "10.0.0.1"},
			Spec: swarm.NodeSpec{Availability: swarm.NodeAvailabilityActive}},
		// worker：ready+active——入选（跨节点采集的目标面）。
		{ID: "n2", Status: swarm.NodeStatus{State: swarm.NodeStateReady, Addr: "10.0.0.2"},
			Spec: swarm.NodeSpec{Availability: swarm.NodeAvailabilityActive}},
		// ready 但 drained：global 采集器不在其上运行——排除。
		{ID: "n3", Status: swarm.NodeStatus{State: swarm.NodeStateReady, Addr: "10.0.0.3"},
			Spec: swarm.NodeSpec{Availability: swarm.NodeAvailabilityPause}},
		// down：不可直连——排除。
		{ID: "n4", Status: swarm.NodeStatus{State: swarm.NodeStateDown, Addr: "10.0.0.4"},
			Spec: swarm.NodeSpec{Availability: swarm.NodeAvailabilityActive}},
		// ready+active 但 Status.Addr 缺省：回落 ManagerStatus.Addr（manager
		// 注册过渡形态）——入选。
		{ID: "n5", Status: swarm.NodeStatus{State: swarm.NodeStateReady},
			ManagerStatus: &swarm.ManagerStatus{Addr: "10.0.0.5:2377"},
			Spec:          swarm.NodeSpec{Availability: swarm.NodeAvailabilityActive}},
		// ready+active 但两处地址全空：不可直连——如实跳过（不产出空 target）。
		{ID: "n6", Status: swarm.NodeStatus{State: swarm.NodeStateReady},
			ManagerStatus: &swarm.ManagerStatus{Addr: ""},
			Spec:          swarm.NodeSpec{Availability: swarm.NodeAvailabilityActive}},
	}
	got := readyNodeAddresses(nodes)
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.5:2377"}
	if !slices.Equal(got, want) {
		t.Fatalf("readyNodeAddresses = %v, want %v", got, want)
	}
	if len(readyNodeAddresses(nil)) != 0 {
		t.Fatal("empty node list must yield empty address set")
	}
}
