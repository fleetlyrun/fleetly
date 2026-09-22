package metrics

// 托管 metrics duty 的 Docker API 消费面（internal/victorialogs docker.go
// 同款形态——端口在本包定义、moby 实现在本文件、假实现注入单测）。相对
// victorialogs 的增面：swarm **config 对象**四面（抓取配置的内容寻址分发，
// 见 spec.go 头注记）与 ServiceState 的 global/Configs 投影。第三方
// （moby/swarm）类型不出本包的端口消费面——swarm.ServiceSpec 是部署器
// 构造载荷，只进不出（出口只有投影与 error）。
//
// 服务写幂等语义由 duty 收敛层保证（inspect → 比对 → create/update）。
// swarm 未就绪返回哨兵 ErrNotSwarmReady（duty 退避重试——与
// victorialogs.ErrNotSwarmReady 同语义不共享类型）。

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
var ErrNotSwarmReady = errors.New("docker engine is not an active swarm manager (metrics duty)")

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
	// ConfigEnsure 确认抓取配置对象存在（幂等；缺失创建——内容寻址命名，
	// 同名即同内容）。返回底座对象 ID（服务 spec 的 ConfigReference 需要
	// ID+名双写——只写名会被 swarm 以 "malformed config reference" 拒绝，
	// W3 secret-ID 同族真机教训，2026-09-22 dind 实证）。
	ConfigEnsure(ctx context.Context, name string, spec swarm.ConfigSpec) (string, error)
	// NetworkName 把服务实况里的网络目标（创建期被 engine 归一为网络 ID）
	// 解析回网络名——幂等比对的同锚面（"host" 是 local-scope 网络，其
	// swarm 侧对象 ID 与本地 ID 不同，正向查名不可行；反向按 ID 解析返回
	// swarm scope 对象名，2026-09-22 dind 实证）。解析失败返回错误，duty
	// 退避重试。
	NetworkName(ctx context.Context, target string) (string, error)
	// ConfigListNames 列出带本包自描述 label 的 config 对象名（GC 面）。
	ConfigListNames(ctx context.Context) ([]string, error)
	// ConfigRemove 删除 config 对象（幂等：缺失视为成功）。
	ConfigRemove(ctx context.Context, name string) error
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
	// Mounts 是挂载（source→target 形态对；volume/bind 统一投影）。
	MountSources []string
	MountTargets []string
	// ConfigNames 是任务引用的 swarm config 对象名。
	ConfigNames []string
	// HealthTest 是容器健康检查的 Test 序列（nil = 未设——健康检查是执行
	// 面，漂移必被比对捕获）。
	HealthTest []string
	// Constraints 是放置约束。
	Constraints []string
	// Global 报告服务是否 global 形态（cAdvisor/node_exporter = true）。
	Global bool
	// Replicas 是期望副本数（replicated 形态；global 恒 0）。
	Replicas uint64
	// MemoryBytes 是内存限额（0 = 未设）。
	MemoryBytes int64
}

// realDockerClient 是 dockerPort 的 moby 实现（victorialogs 同款连接形态：
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
		return nil, fmt.Errorf("metrics: construct docker client: %w", err)
	}
	return &realDockerClient{cli: cli}, nil
}

func (c *realDockerClient) Close() error { return c.cli.Close() }

func (c *realDockerClient) Info(ctx context.Context) (bool, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return false, fmt.Errorf("metrics: docker info: %w", err)
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
		return ServiceState{}, fmt.Errorf("metrics: service inspect %s: %w", name, err)
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
		for _, c := range cs.Configs {
			out.ConfigNames = append(out.ConfigNames, c.ConfigName)
		}
		if cs.Healthcheck != nil {
			out.HealthTest = append([]string{}, cs.Healthcheck.Test...)
		}
	}
	if pl := svc.Spec.TaskTemplate.Placement; pl != nil {
		out.Constraints = append([]string{}, pl.Constraints...)
	}
	if svc.Spec.Mode.Global != nil {
		out.Global = true
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
		return fmt.Errorf("metrics: service create %s: %w", spec.Name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
		Version: swarm.Version{Index: version},
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("metrics: service update %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ServiceRemove(ctx, name, mobyclient.ServiceRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("metrics: service remove %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) VolumeEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("metrics: volume inspect %s: %w", name, err)
	}
	if _, err := c.cli.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Driver: "local",
		Name:   name,
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); ierr == nil {
			return nil // 并发创建竞态：已存在即成功
		}
		return fmt.Errorf("metrics: volume create %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) ConfigEnsure(ctx context.Context, name string, spec swarm.ConfigSpec) (string, error) {
	if res, err := c.cli.ConfigInspect(ctx, name, mobyclient.ConfigInspectOptions{}); err == nil {
		return res.Config.ID, nil
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("metrics: config inspect %s: %w", name, err)
	}
	created, err := c.cli.ConfigCreate(ctx, mobyclient.ConfigCreateOptions{Spec: spec})
	if err != nil {
		if res, ierr := c.cli.ConfigInspect(ctx, name, mobyclient.ConfigInspectOptions{}); ierr == nil {
			return res.Config.ID, nil // 并发创建竞态：已存在即成功
		}
		return "", fmt.Errorf("metrics: config create %s: %w", name, err)
	}
	return created.ID, nil
}

func (c *realDockerClient) NetworkName(ctx context.Context, target string) (string, error) {
	res, err := c.cli.NetworkInspect(ctx, target, mobyclient.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("metrics: network inspect %s: %w", target, err)
	}
	return res.Network.Name, nil
}

func (c *realDockerClient) ConfigListNames(ctx context.Context) ([]string, error) {
	res, err := c.cli.ConfigList(ctx, mobyclient.ConfigListOptions{})
	if err != nil {
		return nil, fmt.Errorf("metrics: config list: %w", err)
	}
	var out []string
	for _, cfg := range res.Items {
		if cfg.Spec.Labels[scrapeLabel] != "true" {
			continue
		}
		out = append(out, cfg.Spec.Name)
	}
	return out, nil
}

func (c *realDockerClient) ConfigRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ConfigRemove(ctx, name, mobyclient.ConfigRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("metrics: config remove %s: %w", name, err)
	}
	return nil
}
