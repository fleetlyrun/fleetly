package substrate

// 任务运行时投影（E7 W5-S6）：控制面对「任务 ↔ 节点 ↔ 容器」三元关系的
// 只读消费面——exec relay 的成员发现（relay 自报容器 hostname → NodeID 反
// 查）与会话目标选择（app service 的 running task → 容器 + 节点）共用。
// 通用 TaskList 投影（services.go）服务引擎对账，不含 NodeID/ContainerID；
// 本投影按需增列，不改既有投影（前序阶段语义冻结）。

import (
	"context"
	"fmt"
	"time"

	mobyclient "github.com/moby/moby/client"
)

// TaskRuntime 是会话路由/成员发现需要的任务运行时投影。
type TaskRuntime struct {
	// ID 是 swarm task ID。
	ID string
	// NodeID 是任务所在节点（swarm 节点对象 ID——relay 连接表的路由键）。
	NodeID string
	// ContainerID 是任务容器完整 ID（swarm 任务容器 hostname = 容器 ID，
	// relay 注册帧的 hostname 与此前缀匹配）。
	ContainerID string
	// Slot 是任务槽位（多副本序）。
	Slot int
	// State / DesiredState 是任务状态与期望状态（会话目标只取双 running）。
	State        string
	DesiredState string
	// Timestamp 是任务状态时间（多副本「最新优先」的排序键）。
	Timestamp time.Time
}

// TaskRuntimes 返回服务全部任务的运行时投影（service ps 语义 + NodeID/
// ContainerID 增列；服务无任务返回空切片——不是错误）。
func (c *Client) TaskRuntimes(ctx context.Context, serviceName string) ([]TaskRuntime, error) {
	ctx, cancel := withCallTimeout(ctx) // D2：非流式 per-call 超时
	defer cancel()
	res, err := c.cli.TaskList(ctx, mobyclient.TaskListOptions{
		Filters: mobyclient.Filters{}.Add("service", serviceName),
	})
	if err != nil {
		return nil, fmt.Errorf("substrate: task runtimes %s: %w", serviceName, err)
	}
	out := make([]TaskRuntime, 0, len(res.Items))
	for _, t := range res.Items {
		item := TaskRuntime{
			ID:           t.ID,
			NodeID:       t.NodeID,
			Slot:         int(t.Slot),
			State:        string(t.Status.State),
			DesiredState: string(t.DesiredState),
			Timestamp:    t.Status.Timestamp,
		}
		if t.Status.ContainerStatus != nil {
			item.ContainerID = t.Status.ContainerStatus.ContainerID
		}
		out = append(out, item)
	}
	return out, nil
}

// NodeHostnames 返回集群全部 ready 节点的「NodeID → hostname」映射（exec
// relay 的成员发现反查面：swarm 任务容器缺省 hostname = **节点 hostname**
// ——relay 自报 hostname 经此映射回 NodeID；容器 ID 前缀匹配只是
// 「daemon 以容器 ID 作缺省 hostname」形态下的补充路径）。节点清单读取
// 失败如实报错。
func (c *Client) NodeHostnames(ctx context.Context) (map[string]string, error) {
	ctx, cancel := withCallTimeout(ctx) // D2：非流式 per-call 超时
	defer cancel()
	res, err := c.cli.NodeList(ctx, mobyclient.NodeListOptions{})
	if err != nil {
		return nil, fmt.Errorf("substrate: node hostnames: %w", err)
	}
	out := make(map[string]string, len(res.Items))
	for _, n := range res.Items {
		out[n.ID] = n.Description.Hostname
	}
	return out, nil
}
