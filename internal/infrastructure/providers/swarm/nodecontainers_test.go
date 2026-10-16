package swarm

// NodeContainers 单测（节点观测面）：三缝注入（nodeList/managedServices/
// listTasks）hermetic 钉测——节点定位、running 过滤、归属域投影、排序与
// 未知节点哨兵；投影纯函数单列钉测（taskEvent 同纪律）。

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

const (
	ncNodePlatform = "01JD0NODE00000000000000000"
	ncNodeOther    = "01JD0NODE00000000000000001"
	ncNodeCarrier  = "carrier-node-1"
)

// ncNode 是带锚定标记的载体节点夹具。
func ncNode() swarm.Node {
	return swarm.Node{ID: ncNodeCarrier, Spec: swarm.NodeSpec{Annotations: swarm.Annotations{Labels: map[string]string{labelNodeID: ncNodePlatform}}}}
}

// ncTask 是任务夹具：节点落点 + 状态 + 身份标记 + 镜像（labels 与 image
// 都零值 = 无 ContainerSpec 的 spec 未回填形态）。
func ncTask(id, node string, state swarm.TaskState, labels map[string]string, image string) swarm.Task {
	t := swarm.Task{ID: id, NodeID: node, Status: swarm.TaskStatus{State: state}}
	if labels != nil || image != "" {
		t.Spec = swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Labels: labels, Image: image}}
	}
	return t
}

// ncService 是受管服务夹具。
func ncService(id, name string, labels map[string]string) swarm.Service {
	return swarm.Service{ID: id, Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: name, Labels: labels}}}
}

// appLabels 是 App 域归属标记夹具（workloadLabels 同构）。
func appLabels(app, process, workload string) map[string]string {
	return map[string]string{
		labelManaged: "true", labelTeam: "acme", labelProject: "shop",
		labelApp: app, labelProcess: process, labelWorkload: workload,
	}
}

// TestNodeContainersFiltersSortsProjects：只列锚定节点的 running 任务；
// 行序 (WorkloadID, Instance) 字典序；App 域归属投影（任务级标记优先，
// nil ContainerSpec 回退服务级）。
func TestNodeContainersFiltersSortsProjects(t *testing.T) {
	p := &Provider{}
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{ncNode()}}, nil
	}
	p.managedServices = func(context.Context) ([]swarm.Service, error) {
		return []swarm.Service{
			ncService("svc-db", "fleetly-db-db1", map[string]string{labelManaged: "true", labelDatabase: "db1", labelWorkload: "db1"}),
			ncService("svc-app", "fleetly-acme-shop-web", appLabels("web", "api", "web-1")),
		}, nil
	}
	p.listTasks = func(_ context.Context, serviceID string) (client.TaskListResult, error) {
		switch serviceID {
		case "svc-app":
			return client.TaskListResult{Items: []swarm.Task{
				// 他节点 running：不计。
				ncTask("t-other-node", "carrier-node-9", swarm.TaskStateRunning, appLabels("web", "api", "web-1"), "nginx:1"),
				// 本节点非 running：不计（替换期旧任务）。
				ncTask("t-shutdown", ncNodeCarrier, swarm.TaskStateShutdown, appLabels("web", "api", "web-1"), ""),
				// 本节点 running：计（任务级标记，镜像投影）。
				ncTask("t-b", ncNodeCarrier, swarm.TaskStateRunning, appLabels("web", "api", "web-1"), "nginx:1.27"),
			}}, nil
		case "svc-db":
			return client.TaskListResult{Items: []swarm.Task{
				// Database 域 running：计（无 ContainerSpec——服务级标记兜底）。
				ncTask("t-a", ncNodeCarrier, swarm.TaskStateRunning, nil, "pg:16"),
			}}, nil
		}
		return client.TaskListResult{}, nil
	}

	rows, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.NoError(t, err)
	require.Len(t, rows, 2, "only running tasks anchored to the carrier node are listed")
	// 排序锚：WorkloadID 字典序（db1 < web-1）。
	assert.Equal(t, "t-a", rows[0].Instance)
	assert.Equal(t, "db1", rows[0].WorkloadID)
	assert.Equal(t, capability.NodeDomainDatabase, rows[0].Domain)
	assert.Equal(t, "pg:16", rows[0].Image, "database domain falls back to service labels; image projects from container spec")
	assert.Empty(t, rows[0].AppID, "non-app domains carry no app attribution")

	assert.Equal(t, "t-b", rows[1].Instance)
	assert.Equal(t, "web-1", rows[1].WorkloadID)
	assert.Equal(t, capability.NodeDomainApp, rows[1].Domain)
	assert.Equal(t, "web", rows[1].AppID)
	assert.Equal(t, "api", rows[1].Process)
	assert.Equal(t, "acme", rows[1].Team)
	assert.Equal(t, "shop", rows[1].Project)
	assert.Equal(t, "nginx:1.27", rows[1].Image)
}

// TestNodeContainersEmptyIsHonest：节点在场但无在跑任务 → 空切片非错误。
func TestNodeContainersEmptyIsHonest(t *testing.T) {
	p := &Provider{}
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{ncNode()}}, nil
	}
	p.managedServices = func(context.Context) ([]swarm.Service, error) {
		return nil, nil
	}
	rows, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestNodeContainersUnknownNode：平台节点 ID 对不上任何载体节点 →
// ErrNodeNotFound 哨兵（API 面映射 E_NOT_FOUND）。
func TestNodeContainersUnknownNode(t *testing.T) {
	p := &Provider{}
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		n := ncNode()
		n.Spec.Labels[labelNodeID] = ncNodeOther // 锚定在别的平台 ID 上
		return client.NodeListResult{Items: []swarm.Node{n}}, nil
	}
	_, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.ErrorIs(t, err, capability.ErrNodeNotFound)
}

// TestNodeContainersServiceDegraded（C19-2 哨兵）：单服务列任务失败只降级
// 该服务的行，其余服务照常成表。
func TestNodeContainersServiceDegraded(t *testing.T) {
	p := &Provider{}
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{ncNode()}}, nil
	}
	p.managedServices = func(context.Context) ([]swarm.Service, error) {
		return []swarm.Service{
			ncService("svc-broken", "fleetly-broken", map[string]string{labelManaged: "true", labelWorkload: "broken"}),
			ncService("svc-ok", "fleetly-ok", appLabels("web", "api", "web-1")),
		}, nil
	}
	p.listTasks = func(_ context.Context, serviceID string) (client.TaskListResult, error) {
		if serviceID == "svc-broken" {
			return client.TaskListResult{}, errors.New("daemon hiccup")
		}
		return client.TaskListResult{Items: []swarm.Task{
			ncTask("t-1", ncNodeCarrier, swarm.TaskStateRunning, appLabels("web", "api", "web-1"), "nginx:1"),
		}}, nil
	}
	rows, err := p.NodeContainers(context.Background(), ncNodePlatform)
	require.NoError(t, err, "a single service failure must degrade that service only")
	require.Len(t, rows, 1)
	assert.Equal(t, "t-1", rows[0].Instance)
}

// TestNodeContainerFromTaskDomains：投影纯函数的四域分支与标记缺席形态
// （spec 未回填 → 归属字段诚实留空，域不冒认 app）。labels 经 taskLabels
// 取值（与 NodeContainers 主路径同序）。
func TestNodeContainerFromTaskDomains(t *testing.T) {
	row := func(labels map[string]string) capability.NodeContainerObservation {
		tk := ncTask("t-1", ncNodeCarrier, swarm.TaskStateRunning, labels, "img:1")
		return nodeContainerFromTask(tk, taskLabels(tk, nil))
	}

	r := row(map[string]string{labelTask: "run-9", labelWorkload: "run-9"})
	assert.Equal(t, capability.NodeDomainTask, r.Domain)
	assert.Empty(t, r.AppID)

	r = row(map[string]string{labelBrowse: "s-1", labelWorkload: "s-1"})
	assert.Equal(t, capability.NodeDomainBrowse, r.Domain)

	r = row(map[string]string{labelDatabase: "db-1", labelWorkload: "db-1"})
	assert.Equal(t, capability.NodeDomainDatabase, r.Domain)

	r = row(appLabels("web", "api", "web-1"))
	assert.Equal(t, capability.NodeDomainApp, r.Domain)
	assert.Equal(t, "web", r.AppID)

	// 标记整体缺席：归属未知（域留空，不冒认）。
	r = row(nil)
	assert.Empty(t, r.Domain)
	assert.Empty(t, r.WorkloadID)
	assert.Equal(t, "img:1", r.Image, "image projection survives missing attribution labels")
}
