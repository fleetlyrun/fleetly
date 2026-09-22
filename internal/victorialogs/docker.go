package victorialogs

// 托管 VictoriaLogs duty 的 Docker API 消费面（internal/rustfs docker.go
// 同款形态——端口在本包定义、moby 实现在本文件、假实现注入单测；端口面
// 按 VL 需要裁剪：无凭据 secret、无网络 ensure/ID 解析（任务挂 host 网络
// ——见 spec.go 头注记）、无任务 IP 直达——健康检查走宿主回环）。第三方
// （moby/swarm）类型不出本包的端口消费面——swarm.ServiceSpec 是部署器
// 构造载荷，只进不出（出口只有投影与 error）。
//
// 服务写幂等语义由 duty 收敛层保证（inspect → 比对 → create/update）。
// swarm 未就绪返回哨兵 ErrNotSwarmReady（duty 退避重试——与
// rustfs.ErrNotSwarmReady 同语义不共享类型）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// ErrNotSwarmReady 表示本机不是 active swarm manager（duty 可重试态）。
var ErrNotSwarmReady = errors.New("docker engine is not an active swarm manager (victorialogs duty)")

// dockerPort 是 duty 对 Docker API 的最小消费面。ServiceState 是本包的
// 实况投影（幂等比对 + 就绪判定 + status 面）。
type dockerPort interface {
	// Info 报告 swarm 是否 active。
	Info(ctx context.Context) (bool, error)
	// ServiceInspect 按名取服务实况；缺失返回 Exists=false（不是错误——
	// 「不存在」是收敛的正常输入）。
	ServiceInspect(ctx context.Context, name string) (ServiceState, error)
	// ServiceCreate 创建服务（duty 保证仅缺失时调用）。
	ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error
	// ServiceUpdate 以乐观令牌推进服务（version 取自先前的 ServiceInspect）。
	ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error
	// ServiceRemove 删除服务（幂等：缺失视为成功）。
	ServiceRemove(ctx context.Context, name string) error
	// VolumeEnsure 确认命名卷存在（幂等；缺失创建）。
	VolumeEnsure(ctx context.Context, name string) error
}

// ServiceState 是托管服务的实况投影（本包收敛比对的实况侧）。
type ServiceState struct {
	Exists bool
	// Version 是底座对象版本（乐观令牌）。
	Version uint64
	Image   string
	Args    []string
	// Networks 是任务网络挂载目标（host 网络任务 = ["host"]）。
	Networks []string
	// Mounts 是卷挂载（source→target 形态对）。
	MountSources []string
	MountTargets []string
	// Constraints 是放置约束。
	Constraints []string
	// Replicas 是期望副本数。
	Replicas uint64
	// MemoryBytes 是内存限额（0 = 未设）。
	MemoryBytes int64
}

// realDockerClient 是 dockerPort 的 moby 实现（rustfs 同款连接形态：
// DOCKER_HOST/本机套接字）。
type realDockerClient struct {
	cli *mobyclient.Client
}

// newRealDockerClient 构造真实客户端（host 空 = FromEnv）。
func newRealDockerClient(host string) (*realDockerClient, error) {
	opts := []mobyclient.Opt{mobyclient.FromEnv}
	if host != "" {
		opts = []mobyclient.Opt{mobyclient.WithHost(host), mobyclient.FromEnv}
	}
	cli, err := mobyclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("victorialogs: construct docker client: %w", err)
	}
	return &realDockerClient{cli: cli}, nil
}

func (c *realDockerClient) Close() error { return c.cli.Close() }

func (c *realDockerClient) Info(ctx context.Context) (bool, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return false, fmt.Errorf("victorialogs: docker info: %w", err)
	}
	return res.Info.Swarm.NodeID != "" &&
		res.Info.Swarm.LocalNodeState == swarm.LocalNodeStateActive, nil
}

func (c *realDockerClient) ServiceInspect(ctx context.Context, name string) (ServiceState, error) {
	res, err := c.cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return ServiceState{}, nil
		}
		return ServiceState{}, fmt.Errorf("victorialogs: service inspect %s: %w", name, err)
	}
	svc := res.Service
	out := ServiceState{Exists: true, Version: svc.Version.Index}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		out.Image = cs.Image
		out.Args = append([]string{}, cs.Args...)
		for _, m := range cs.Mounts {
			out.MountSources = append(out.MountSources, m.Source)
			out.MountTargets = append(out.MountTargets, m.Target)
		}
	}
	if pl := svc.Spec.TaskTemplate.Placement; pl != nil {
		out.Constraints = append([]string{}, pl.Constraints...)
	}
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		out.Replicas = *svc.Spec.Mode.Replicated.Replicas
	}
	if task := svc.Spec.TaskTemplate; task.Resources != nil && task.Resources.Limits != nil {
		out.MemoryBytes = task.Resources.Limits.MemoryBytes
	}
	for _, n := range svc.Spec.TaskTemplate.Networks {
		out.Networks = append(out.Networks, n.Target)
	}
	return out, nil
}

func (c *realDockerClient) ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceCreate(ctx, mobyclient.ServiceCreateOptions{Spec: spec}); err != nil {
		return fmt.Errorf("victorialogs: service create %s: %w", spec.Name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
		Version: swarm.Version{Index: version},
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("victorialogs: service update %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ServiceRemove(ctx, name, mobyclient.ServiceRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("victorialogs: service remove %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) VolumeEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("victorialogs: volume inspect %s: %w", name, err)
	}
	if _, err := c.cli.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Driver: "local",
		Name:   name,
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); ierr == nil {
			return nil // 并发创建竞态：已存在即成功
		}
		return fmt.Errorf("victorialogs: volume create %s: %w", name, err)
	}
	return nil
}
