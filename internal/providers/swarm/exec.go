package swarm

// exec 子面的 swarm 实现（F3.2，ADR-0049）：管理侧实例解析（ExecTarget =
// 服务任务实时快照）、节点侧载体执行（ExecWorkload = 本地 daemon 的
// docker exec——exec API 只落在持有容器的 daemon 上，节点代理是唯一
// 抵达面）、凭证校验（ExecClusterToken = swarm join token 对照）。
// 中继代理循环（RunRelayAgent）在 relayagent.go。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// labelSwarmTaskID 是 swarm 原生任务标记（task 容器自带；agent 侧容器
// 定位锚——平台标记 + 实例标记双验）。
const labelSwarmTaskID = "com.docker.swarm.task.id"

// execInspectPoll 是退出码轮询节拍（docker exec 无 wait API；inspect 轮询
// 是 CLI 同款机制）。
const execInspectPoll = 150 * time.Millisecond

// ErrNoRunningTask 是 ExecTarget 无在跑实例的 Provider 侧哨兵（capability
// 层 ErrExecNoRunning 的别名——跨层哨兵词汇单源在 capability，engine 的
// 受理位信封映射不 import providers）。
var ErrNoRunningTask = capability.ErrExecNoRunning

// ExecTarget 实现 RuntimeExec：按 Workload 标记定位服务，任务列表实时
// 快照取首个 running 任务（ID 字典序——swarm task ID 时间有序，确定性
// 选择）。NodeID 是任务实际落点（载体节点 ID——与 Watch 锚定同源）。
func (p *Provider) ExecTarget(ctx context.Context, workloadID string) (capability.ExecTargetInstance, error) {
	services, err := p.cli.ServiceList(ctx, client.ServiceListOptions{
		Filters: client.Filters{}.Add("label", labelWorkload+"="+workloadID),
	})
	if err != nil {
		return capability.ExecTargetInstance{}, fmt.Errorf("swarm exec target: service list: %w", err)
	}
	if len(services.Items) == 0 {
		return capability.ExecTargetInstance{}, fmt.Errorf("%w: %s", ErrNoRunningTask, workloadID)
	}
	svc := services.Items[0]
	tasks, err := p.cli.TaskList(ctx, client.TaskListOptions{
		Filters: client.Filters{}.Add("service", svc.ID),
	})
	if err != nil {
		return capability.ExecTargetInstance{}, fmt.Errorf("swarm exec target: task list: %w", err)
	}
	running := make([]string, 0, len(tasks.Items))
	byID := make(map[string]swarm.Task, len(tasks.Items))
	for _, t := range tasks.Items {
		if t.Status.State == swarm.TaskStateRunning && t.NodeID != "" {
			running = append(running, t.ID)
			byID[t.ID] = t
		}
	}
	if len(running) == 0 {
		return capability.ExecTargetInstance{}, fmt.Errorf("%w: %s", ErrNoRunningTask, workloadID)
	}
	sort.Strings(running) // 确定性：多副本时稳定选首个
	t := byID[running[0]]
	return capability.ExecTargetInstance{Instance: t.ID, CarrierNodeID: t.NodeID}, nil
}

// ExecClusterToken 实现 RuntimeExec：对照 swarm 活 join token（worker 与
// manager 皆可——两者等价集群成员权，C3）。/v1/relay 与 /v1/platform/binary
// 原生入口的鉴权单源。
func (p *Provider) ExecClusterToken(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("empty relay credential")
	}
	inspect, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return fmt.Errorf("swarm exec token: inspect: %w", err)
	}
	if token == inspect.Swarm.JoinTokens.Worker || token == inspect.Swarm.JoinTokens.Manager {
		return nil
	}
	return errors.New("relay credential does not match an active swarm join token")
}

// ExecWorkload 实现 RuntimeExec：本地 daemon 上解析（WorkloadID, Instance）
// 的载体容器并 docker exec。TTY 形态单流（stdout 合流——PTY 语义）；
// 管道形态 stdcopy 双路。stdin 泵在 Stdin EOF 时 CloseWrite（docker 侧
// EOF 传播尽力而为——ADR-0039 实录 wart 注记）；ctx 结束即关 hijack
// （TTY exec 由 daemon 随连接关闭收口，非 TTY 尽力语义——ADR-0049 后果）。
func (p *Provider) ExecWorkload(ctx context.Context, req capability.ExecWorkloadRequest) (int, error) {
	if req.WorkloadID == "" || req.Instance == "" || len(req.Argv) == 0 {
		return 0, fmt.Errorf("swarm exec: workload, instance and argv are required")
	}
	cid, err := p.execContainer(ctx, req.WorkloadID, req.Instance)
	if err != nil {
		return 0, err
	}
	created, err := p.cli.ExecCreate(ctx, cid, client.ExecCreateOptions{
		Cmd: req.Argv, TTY: req.TTY,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return 0, fmt.Errorf("swarm exec: create: %w", err)
	}
	attach, err := p.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: req.TTY})
	if err != nil {
		return 0, fmt.Errorf("swarm exec: attach: %w", err)
	}
	defer attach.Close()

	// resize 订阅泵（TTY 形态；通道关闭即退）。
	if req.Resize != nil {
		go func() {
			for sz := range req.Resize {
				_, _ = p.cli.ExecResize(ctx, created.ID, client.ExecResizeOptions{
					Height: uint(sz.Rows), Width: uint(sz.Cols),
				})
			}
		}()
	}

	// stdin 泵：请求流 → hijack 写半；EOF 即 CloseWrite（尽力传播）。
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, _ = io.Copy(attach.Conn, req.Stdin)
		_ = attach.CloseWrite()
	}()

	// 输出泵：容器退出后流自然 EOF；泵错误经 channel 回收（wait 先于管道
	// 排空是常态——RunUtility 同款）。
	pumpErr := make(chan error, 1)
	go func() {
		var err error
		if req.TTY {
			_, err = io.Copy(req.Stdout, attach.Reader)
		} else {
			_, err = stdcopy.StdCopy(req.Stdout, req.Stderr, attach.Reader)
		}
		pumpErr <- err
	}()

	for {
		insp, err := p.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return 0, fmt.Errorf("swarm exec: inspect: %w", err)
		}
		if !insp.Running {
			if err := <-pumpErr; err != nil {
				return insp.ExitCode, fmt.Errorf("swarm exec: output pump: %w", err)
			}
			return insp.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			// 连接关闭是终止面（TTY exec 由 daemon 收口；非 TTY 尽力）。
			return 0, fmt.Errorf("swarm exec: %w", ctx.Err())
		case <-stdinDone:
			// stdin 已尽：等容器进程自然退出（非 TTY 消费 stdin 的命令
			// 依赖 EOF 传播；轮询继续）。
		case <-time.After(execInspectPoll):
		}
	}
}

// execContainer 解析实例容器：swarm 任务标记定位 + 平台标记双验（只 exec
// fleetly 管辖且 Workload 匹配的容器——纵深防御）。
func (p *Provider) execContainer(ctx context.Context, workloadID, instance string) (string, error) {
	list, err := p.cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: client.Filters{}.Add("label", labelSwarmTaskID+"="+instance),
	})
	if err != nil {
		return "", fmt.Errorf("swarm exec: container list: %w", err)
	}
	for _, c := range list.Items {
		if c.Labels[labelManaged] != "true" || c.Labels[labelWorkload] != workloadID {
			continue
		}
		return c.ID, nil
	}
	return "", fmt.Errorf("%w: workload %s instance %s", capability.ErrExecTargetGone, workloadID, instance)
}

// relayHTTPClient 是节点代理拨号 manager 的 HTTP 客户端（明文 VPC 形态，
// ADR-0049 决策 2；代理安全承载 = join token）。
func relayHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true, // 每次拨号独立连接（重连语义清晰）
	}}
}
