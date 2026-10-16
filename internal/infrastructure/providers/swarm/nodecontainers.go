package swarm

// 节点观测子面的 swarm 实现（RuntimeNodeInspect）：按平台节点 ID 列在跑
// 容器。数据源 = 集群任务视图（pollTasks 同源——每 running task 对应一个
// 容器）；daemon 本地 ContainerList 是节点本地面，manager 看不到全集群，
// 不作数据源。

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// NodeContainers 实现 RuntimeNodeInspect：平台节点 ID 经节点锚定标记定位
// 载体节点（findNodeByPlatformID），受管服务逐个列任务后按节点 + running
// 内存过滤（pollTasks 同源发现面；C19-2 哨兵同款——单服务列任务失败只降级
// 该服务的行，其余照常）。归属取任务身份标记（taskLabels 单源：任务级
// ContainerSpec.Labels 优先、服务级兜底），行序按 (WorkloadID, Instance)
// 字典序。
func (p *Provider) NodeContainers(ctx context.Context, nodeID string) ([]capability.NodeContainerObservation, error) {
	node, err := p.findNodeByPlatformID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	services, err := p.listManagedServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("swarm node containers: managed service list: %w", err)
	}
	rows := make([]capability.NodeContainerObservation, 0, len(services))
	for i := range services {
		tasks, err := p.listServiceTasks(ctx, services[i].ID)
		if err != nil {
			slog.Warn("swarm node containers: task poll skipped a service", "service", services[i].Spec.Name, "error", err)
			continue
		}
		for _, t := range tasks.Items {
			if t.NodeID != node.ID || t.Status.State != swarm.TaskStateRunning {
				continue
			}
			rows = append(rows, nodeContainerFromTask(t, taskLabels(t, services[i].Spec.Labels)))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].WorkloadID != rows[j].WorkloadID {
			return rows[i].WorkloadID < rows[j].WorkloadID
		}
		return rows[i].Instance < rows[j].Instance
	})
	return rows, nil
}

// nodeContainerFromTask 把单条任务快照投影为节点容器观测行（纯函数，
// taskEvent 同理由——hermetic 钉测）。域主体互斥分支与 workloadLabels
// 同构反读；标记缺席（spec 未回填形态）的归属字段留空——诚实呈现。
func nodeContainerFromTask(t swarm.Task, labels map[string]string) capability.NodeContainerObservation {
	row := capability.NodeContainerObservation{
		Instance:   t.ID,
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
	if t.Spec.ContainerSpec != nil {
		row.Image = t.Spec.ContainerSpec.Image
	}
	return row
}

// listManagedServices 列出全部受管服务（managed 标记选择器，真源 =
// translate.go labelManaged——pollTasks 同一发现面；测试缝优先，p.nodes
// 同款缝契约）。
func (p *Provider) listManagedServices(ctx context.Context) ([]swarm.Service, error) {
	if p.managedServices != nil {
		return p.managedServices(ctx)
	}
	res, err := p.cli.ServiceList(ctx, client.ServiceListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}
