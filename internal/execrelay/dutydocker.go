package execrelay

// duty 的 Docker API 消费面（internal/victorialogs docker.go 同款形态——
// 端口在本包定义、moby 实现在本文件、假实现注入单测；端口面按 relay duty
// 需要裁剪：服务收敛 + secret 原语 + 网络/Info 投影，无卷/无任务面）。
// 第三方（moby/swarm）类型不出本包的端口消费面——swarm.ServiceSpec 是部署
// 器构造载荷，只进不出（出口只有投影与 error）。

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"
)

// ErrNotSwarmReady 表示本机不是 active swarm manager（duty 可重试态——
// victorialogs 哨兵同语义不共享类型）。
var ErrNotSwarmReady = fmt.Errorf("docker engine is not an active swarm manager (execrelay duty)")

// DutyInfo 是 duty 关心的 Info 投影（advertise addr + swarm active——ingress
// traefik 部署器同款两字段）。
type DutyInfo struct {
	SwarmActive bool
	// NodeAddr 是 swarm advertise addr（FLEETLY_CONTROL_ADDR 的 host 段源；
	// worker 无此值——duty 只在 manager 收敛）。
	NodeAddr string
}

// dutyDocker 是 duty 对 Docker API 的最小消费面。
type dutyDocker interface {
	// Info 报告 swarm active 与 advertise addr。
	Info(ctx context.Context) (DutyInfo, error)
	// ServiceInspect 按名取服务实况；缺失返回 Exists=false（不是错误——
	// 「不存在」是收敛的正常输入）。
	ServiceInspect(ctx context.Context, name string) (DutyServiceState, error)
	// ServiceCreate 创建服务（duty 保证仅缺失时调用）。
	ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error
	// ServiceUpdate 以乐观令牌推进服务（version 取自先前的 ServiceInspect）。
	ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error
	// ServiceRemove 删除服务（幂等：缺失视为成功）。
	ServiceRemove(ctx context.Context, name string) error
	// SecretInspect 报告 secret 在位与对象 ID（ensureToken 的幂等判据）。
	SecretInspect(ctx context.Context, name string) (SecretView, error)
	// SecretCreate 创建 secret 并返回对象 ID（仅在缺失时调用；data 只进
	// 创建载荷，绝不进日志/错误文本）。
	SecretCreate(ctx context.Context, name string, data []byte, labels map[string]string) (string, error)
	// NetworkName 把服务实况里的网络挂载目标（创建期 "host" 被归一为网络
	// ID）解析回网络名（幂等比对的同锚面——victorialogs/metrics 同款）。
	NetworkName(ctx context.Context, target string) (string, error)
}

// SecretView 是 secret 在位投影。
type SecretView struct {
	Exists bool
	ID     string
}

// dutyDockerClient 是 dutyDocker 的 moby 实现。
type dutyDockerClient struct {
	cli *mobyclient.Client
}

// newDutyDocker 构造真实客户端（host 空 = FromEnv）。
func newDutyDocker(host string) (*dutyDockerClient, error) {
	opts := []mobyclient.Opt{mobyclient.FromEnv}
	if host != "" {
		opts = []mobyclient.Opt{mobyclient.WithHost(host), mobyclient.FromEnv}
	}
	cli, err := mobyclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("execrelay: construct docker client: %w", err)
	}
	return &dutyDockerClient{cli: cli}, nil
}

// Close 释放连接。
func (c *dutyDockerClient) Close() error { return c.cli.Close() }

// Info 实现 dutyDocker（advertise addr = swarm.NodeAddr）。
func (c *dutyDockerClient) Info(ctx context.Context) (DutyInfo, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return DutyInfo{}, fmt.Errorf("execrelay: docker info: %w", err)
	}
	return DutyInfo{
		SwarmActive: res.Info.Swarm.NodeID != "" &&
			res.Info.Swarm.LocalNodeState == swarm.LocalNodeStateActive,
		NodeAddr: res.Info.Swarm.NodeAddr,
	}, nil
}

// ServiceInspect 实现 dutyDocker（投影比对位——spec.go specEqual 消费）。
func (c *dutyDockerClient) ServiceInspect(ctx context.Context, name string) (DutyServiceState, error) {
	res, err := c.cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return DutyServiceState{}, nil
		}
		return DutyServiceState{}, fmt.Errorf("execrelay: service inspect %s: %w", name, err)
	}
	svc := res.Service
	out := DutyServiceState{Exists: true, Version: svc.Version.Index}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		out.Image = cs.Image
		out.Env = append([]string{}, cs.Env...)
		for _, m := range cs.Mounts {
			out.MountSources = append(out.MountSources, m.Source)
			out.MountTargets = append(out.MountTargets, m.Target)
		}
		for _, ref := range cs.Secrets {
			out.SecretIDs = append(out.SecretIDs, ref.SecretID)
			out.SecretNames = append(out.SecretNames, ref.SecretName)
		}
	}
	for _, n := range svc.Spec.TaskTemplate.Networks {
		out.Networks = append(out.Networks, n.Target)
	}
	out.Global = svc.Spec.Mode.Global != nil
	if task := svc.Spec.TaskTemplate; task.Resources != nil && task.Resources.Limits != nil {
		out.MemoryBytes = task.Resources.Limits.MemoryBytes
	}
	return out, nil
}

// ServiceCreate 实现 dutyDocker。
func (c *dutyDockerClient) ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceCreate(ctx, mobyclient.ServiceCreateOptions{Spec: spec}); err != nil {
		return fmt.Errorf("execrelay: service create %s: %w", spec.Name, err)
	}
	return nil
}

// ServiceUpdate 实现 dutyDocker。
func (c *dutyDockerClient) ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
		Version: swarm.Version{Index: version},
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("execrelay: service update %s: %w", name, err)
	}
	return nil
}

// ServiceRemove 实现 dutyDocker（幂等）。
func (c *dutyDockerClient) ServiceRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ServiceRemove(ctx, name, mobyclient.ServiceRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("execrelay: service remove %s: %w", name, err)
	}
	return nil
}

// SecretInspect 实现 dutyDocker。
func (c *dutyDockerClient) SecretInspect(ctx context.Context, name string) (SecretView, error) {
	res, err := c.cli.SecretInspect(ctx, name, mobyclient.SecretInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return SecretView{}, nil
		}
		return SecretView{}, fmt.Errorf("execrelay: secret inspect %s: %w", name, err)
	}
	return SecretView{Exists: true, ID: res.Secret.ID}, nil
}

// SecretCreate 实现 dutyDocker（data 只进创建载荷；并发创建竞态 = 已存在
// 即成功——inspect 兜回 ID，substrate.EnsureSecret 同型）。
func (c *dutyDockerClient) SecretCreate(ctx context.Context, name string, data []byte, labels map[string]string) (string, error) {
	spec := swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: name, Labels: labels},
		Data:        data,
	}
	created, err := c.cli.SecretCreate(ctx, mobyclient.SecretCreateOptions{Spec: spec})
	if err == nil {
		return created.ID, nil
	}
	if errdefs.IsConflict(err) || errdefs.IsAlreadyExists(err) {
		res, ierr := c.cli.SecretInspect(ctx, name, mobyclient.SecretInspectOptions{})
		if ierr == nil {
			return res.Secret.ID, nil
		}
	}
	return "", fmt.Errorf("execrelay: secret create %s: %w", name, err)
}

// NetworkName 实现 dutyDocker（ID/名反查——比对同锚面）。
func (c *dutyDockerClient) NetworkName(ctx context.Context, target string) (string, error) {
	res, err := c.cli.NetworkInspect(ctx, target, mobyclient.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("execrelay: network inspect %s: %w", target, err)
	}
	return res.Network.Name, nil
}
