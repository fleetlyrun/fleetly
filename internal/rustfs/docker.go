package rustfs

// 托管 RustFS duty 的 Docker API 消费面（E3-5，zot 部署器同款形态——
// internal/ingress/traefik.go 的 dockerClient 端口 + realDockerClient 适配
// 同构）：端口在本包定义、moby 实现在本文件、假实现注入单测。第三方
// （moby/swarm）类型不出本包的端口消费面——swarm.ServiceSpec 是部署器
// 构造载荷，只进不出（出口只有投影与 error）。
//
// 服务写幂等语义由 duty 收敛层保证（inspect → 比对 → create/update）；
// 适配器做忠实的翻译与错误包装。swarm 未就绪返回哨兵 ErrNotSwarmReady
//（duty 退避重试——与 ingress.ErrNotSwarmReady 同语义不共享类型）。

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
var ErrNotSwarmReady = errors.New("docker engine is not an active swarm manager (rustfs duty)")

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
	// NetworkEnsure 确认 overlay 网络存在（幂等；缺失创建——**attachable**
	// 形态：restic 上传轨的一次性容器经它入网，非 attachable overlay 拒绝
	// 独立容器挂接）。
	NetworkEnsure(ctx context.Context, name string) error
	// NetworkID 解析网络名 → 底座 ID（服务网络挂载与任务投影都以 ID 表达）。
	NetworkID(ctx context.Context, name string) (string, error)
	// SecretInspect 按名取 swarm secret（凭据 secret 的幂等创建判据；
	// exists=true 时返回对象 ID——服务 spec 的 secret 引用必须携带 ID，
	// 仅名字是 malformed reference；value 不可读——Docker API 从不回吐
	// secret 数据）。
	SecretInspect(ctx context.Context, name string) (id string, exists bool, err error)
	// SecretCreate 创建 swarm secret 并返回其 ID（duty 保证仅缺失时调用；
	// data 只进创建载荷，绝不进日志/错误）。
	SecretCreate(ctx context.Context, spec swarm.SecretSpec) (string, error)
	// SecretList 按 label 选择器返回 secret 名（清场路径：mode 离开
	// rustfs 后移除凭据 secret，凭据是运行时配置不残留）。
	SecretList(ctx context.Context, labels map[string]string) ([]string, error)
	// SecretRemove 删除 secret（幂等：缺失视为成功；in-use 返回错误由
	// duty 退避重试——服务删除到 secret 引用释放有传播延迟）。
	SecretRemove(ctx context.Context, name string) error
	// TaskAddress 返回服务当前 running 任务在指定网络上的 IP（平台侧
	// S3 消费面——EnsureBucket/探针——的可达拨号地址；overlay VIP 只在
	// 网络内可解析，fleetlyd 经同网任务 IP 直达）。ok=false = 无 running
	// 任务或未挂目标网络（收敛未完成，不是错误）。
	TaskAddress(ctx context.Context, service, networkID string) (ip string, ok bool, err error)
}

// ServiceState 是托管服务的实况投影（本包收敛比对的实况侧）。
type ServiceState struct {
	Exists bool
	// Version 是底座对象版本（乐观令牌）。
	Version uint64
	Image   string
	Env     []string
	// Networks 是任务网络挂载目标（ID 形态，alias 一并列出）。
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
	// SecretNames 是容器 secret 引用名（比对凭据轮换的判据）。
	SecretNames []string
}

// realDockerClient 是 dockerPort 的 moby 实现（连接形态与 ingress 部署器
// 同款：DOCKER_HOST/本机套接字）。
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
		return nil, fmt.Errorf("rustfs: construct docker client: %w", err)
	}
	return &realDockerClient{cli: cli}, nil
}

func (c *realDockerClient) Close() error { return c.cli.Close() }

func (c *realDockerClient) Info(ctx context.Context) (bool, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return false, fmt.Errorf("rustfs: docker info: %w", err)
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
		return ServiceState{}, fmt.Errorf("rustfs: service inspect %s: %w", name, err)
	}
	svc := res.Service
	out := ServiceState{Exists: true, Version: svc.Version.Index}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		out.Image = cs.Image
		out.Env = append([]string{}, cs.Env...)
		for _, m := range cs.Mounts {
			out.MountSources = append(out.MountSources, m.Source)
			out.MountTargets = append(out.MountTargets, m.Target)
		}
		for _, s := range cs.Secrets {
			out.SecretNames = append(out.SecretNames, s.SecretName)
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
		return fmt.Errorf("rustfs: service create %s: %w", spec.Name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
		Version: swarm.Version{Index: version},
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("rustfs: service update %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ServiceRemove(ctx, name, mobyclient.ServiceRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("rustfs: service remove %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) VolumeEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("rustfs: volume inspect %s: %w", name, err)
	}
	if _, err := c.cli.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Driver: "local",
		Name:   name,
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); ierr == nil {
			return nil // 并发创建竞态：已存在即成功
		}
		return fmt.Errorf("rustfs: volume create %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) NetworkEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("rustfs: network inspect %s: %w", name, err)
	}
	if _, err := c.cli.NetworkCreate(ctx, name, mobyclient.NetworkCreateOptions{
		Driver: "overlay",
		// attachable：restic 上传轨的一次性容器（独立容器形态，E3-3）
		// 挂接本网络——非 attachable overlay 拒绝独立容器。
		Attachable: true,
		Labels:     map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); ierr == nil {
			return nil // 并发创建竞态：已存在即成功
		}
		return fmt.Errorf("rustfs: network create %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) NetworkID(ctx context.Context, name string) (string, error) {
	res, err := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("rustfs: network inspect %s: %w", name, err)
	}
	return res.Network.ID, nil
}

func (c *realDockerClient) SecretInspect(ctx context.Context, name string) (string, bool, error) {
	res, err := c.cli.SecretInspect(ctx, name, mobyclient.SecretInspectOptions{})
	if err == nil {
		return res.Secret.ID, true, nil
	}
	if errdefs.IsNotFound(err) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("rustfs: secret inspect %s: %w", name, err)
}

func (c *realDockerClient) SecretCreate(ctx context.Context, spec swarm.SecretSpec) (string, error) {
	res, err := c.cli.SecretCreate(ctx, mobyclient.SecretCreateOptions{Spec: spec})
	if err != nil {
		return "", fmt.Errorf("rustfs: secret create %s: %w", spec.Name, err)
	}
	return res.ID, nil
}

func (c *realDockerClient) SecretList(ctx context.Context, labels map[string]string) ([]string, error) {
	filters := mobyclient.Filters{}
	for k, v := range labels {
		filters = filters.Add("label", k+"="+v)
	}
	res, err := c.cli.SecretList(ctx, mobyclient.SecretListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("rustfs: secret list: %w", err)
	}
	out := make([]string, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, s.Spec.Name)
	}
	return out, nil
}

func (c *realDockerClient) SecretRemove(ctx context.Context, name string) error {
	if _, err := c.cli.SecretRemove(ctx, name, mobyclient.SecretRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("rustfs: secret remove %s: %w", name, err)
	}
	return nil
}

func (c *realDockerClient) TaskAddress(ctx context.Context, service, networkID string) (string, bool, error) {
	res, err := c.cli.TaskList(ctx, mobyclient.TaskListOptions{
		Filters: mobyclient.Filters{}.Add("service", service),
	})
	if err != nil {
		return "", false, fmt.Errorf("rustfs: task list %s: %w", service, err)
	}
	for _, t := range res.Items {
		if t.Status.State != swarm.TaskStateRunning || t.DesiredState != swarm.TaskStateRunning {
			continue
		}
		for _, nats := range t.NetworksAttachments {
			if nats.Network.ID != networkID || len(nats.Addresses) == 0 {
				continue
			}
			// 地址是 ipam 网段形态（netip.Prefix，如 10.0.0.3/24）——取 IP 段。
			if ip := nats.Addresses[0].Addr(); ip.IsValid() && !ip.IsUnspecified() {
				return ip.String(), true, nil
			}
		}
	}
	return "", false, nil
}
