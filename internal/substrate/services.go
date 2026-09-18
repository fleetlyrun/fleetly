package substrate

// engine.Substrate 端口的 moby/client 实现（T2-5a 服务/任务面）：Swarm
// service 与 task 的读、幂等写与 swarm 就绪检查。第三方类型只在本包内部，
// 出口一律 engine 核心类型；错误归一为端口哨兵（engine.ErrServiceNotFound /
// engine.ErrNotSwarmReady / state.ErrObjectNotFound / state.ErrVersionConflict）。
//
// 受管字段纪律（release-semantics §2.8 / architecture §2.5，逐条固定）：
//   - UpdateConfig.FailureAction = pause（D-REL-1：Swarm 侧固定 pause，
//     平台是唯一回滚决策者）；
//   - UpdateConfig.Monitor = 5s（平台固定，只判定启动期失败，不放大）；
//   - ForceUpdate 恒不递增（归位重放零任务替换的前提，Spike B2；
//     --force 禁用是平台纪律）。
// 其余字段逐字来自 engine.ServiceSpec（order/parallelism/delay 的组合裁决
// 在引擎规划层，适配器不裁剪）。

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 平台受管常量（不暴露配置面；architecture §2.5 默认参数表）。
const (
	managedFailureAction = swarm.UpdateFailureActionPause
	managedMonitor       = 5 * time.Second
	// defaultRestartCondition / defaultRestartDelay 是重启策略缺省
	//（architecture §2.5 运行期语义：restart-condition=any、delay 5s）。
	defaultRestartCondition = swarm.RestartPolicyConditionAny
	defaultRestartDelay     = 5 * time.Second
)

// 编译期断言：Client 隐式实现 engine.Substrate 与 engine.ImageChecker 端口
// （第三方类型不出包的结构性证明）。
var (
	_ engine.Substrate    = (*Client)(nil)
	_ engine.ImageChecker = (*Client)(nil)
)

// ImageDigest 实现 engine.ImageChecker：返回用于 digest 钉定的不可变摘要。
// 优先取 RepoDigests 的清单摘要（swarm 分发解析只接受 manifest digest——
// 实测钉配置 ID 会得到 "manifest schema unsupported" 任务拒绝）；本机构建
// 镜像（buildkit tar 装载、免 registry）无清单摘要，返回空串由引擎按引用
// 直用（tag 形态；镜像不被平台自动清理，builds 行留有 digest 台账）。
// 缺失归一为 engine.ErrImageMissing。
func (c *Client) ImageDigest(ctx context.Context, ref string) (string, error) {
	res, err := c.cli.ImageInspect(ctx, ref)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return "", fmt.Errorf("%w: %s", engine.ErrImageMissing, ref)
		}
		return "", fmt.Errorf("substrate: image inspect %s: %w", ref, err)
	}
	for _, rd := range res.RepoDigests {
		if _, digest, ok := strings.Cut(rd, "@"); ok && strings.HasPrefix(digest, "sha256:") {
			return digest, nil
		}
	}
	return "", nil
}

// SwarmReady 确认本机为 active swarm manager：未 init / 非 manager 返回
// errors.Is(err, engine.ErrNotSwarmReady)（engine 据此映射
// E_RUNTIME_UNAVAILABLE + swarm init 建议）。
func (c *Client) SwarmReady(ctx context.Context) error {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return fmt.Errorf("substrate: info: %w", err)
	}
	if res.Info.Swarm.NodeID == "" || res.Info.Swarm.LocalNodeState != swarm.LocalNodeStateActive {
		return fmt.Errorf("%w: swarm 未初始化（安装器负责 docker swarm init；手动环境先执行 docker swarm init）", engine.ErrNotSwarmReady)
	}
	return nil
}

// NetworkEnsure 确认 overlay 网络存在（幂等：已有即 no-op；缺失创建——
// 每应用专属 overlay 网络，architecture §2.4 服务命名与网络行）。
func (c *Client) NetworkEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("substrate: network inspect %s: %w", name, err)
	}
	if _, err := c.cli.NetworkCreate(ctx, name, mobyclient.NetworkCreateOptions{
		Driver: "overlay",
		Labels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
		},
	}); err != nil {
		// 并发创建竞态：已存在即成功。
		if _, ierr := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); ierr == nil {
			return nil
		}
		return fmt.Errorf("substrate: network create %s: %w", name, err)
	}
	return nil
}

// buildSwarmSpec 把引擎核心 ServiceSpec 翻译为 swarm.ServiceSpec（第三方
// 类型不越过本函数）。
func buildSwarmSpec(spec engine.ServiceSpec) swarm.ServiceSpec {
	container := &swarm.ContainerSpec{
		Image:  spec.Image,
		Labels: spec.ContainerLabels,
		Env:    spec.Env,
	}
	if len(spec.Command) > 0 {
		container.Command = spec.Command
	}
	if spec.StopSignal != "" {
		container.StopSignal = spec.StopSignal
	}
	if spec.StopGracePeriod > 0 {
		grace := spec.StopGracePeriod
		container.StopGracePeriod = &grace
	}
	if spec.Healthcheck != nil {
		container.Healthcheck = swarmHealthcheck(*spec.Healthcheck)
	}
	for _, m := range spec.Mounts {
		container.Mounts = append(container.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   m.VolumeName,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	for _, s := range spec.Secrets {
		container.Secrets = append(container.Secrets, &swarm.SecretReference{
			SecretName: s.SecretName,
			File:       &swarm.SecretReferenceFileTarget{Name: s.Target},
		})
	}

	task := swarm.TaskSpec{
		ContainerSpec: container,
	}
	if len(spec.Networks) > 0 {
		for _, n := range spec.Networks {
			task.Networks = append(task.Networks, swarm.NetworkAttachmentConfig{
				Target:  n.Name,
				Aliases: n.Aliases,
			})
		}
	}
	if len(spec.Constraints) > 0 {
		task.Placement = &swarm.Placement{Constraints: spec.Constraints}
	}
	if spec.Resources != nil {
		task.Resources = &swarm.ResourceRequirements{
			Limits: &swarm.Limit{
				NanoCPUs:    spec.Resources.NanoCPUs,
				MemoryBytes: spec.Resources.MemoryBytes,
			},
		}
	}
	rp := spec.RestartPolicy
	if rp == nil {
		rp = &engine.RestartPolicySpec{Condition: string(defaultRestartCondition), Delay: defaultRestartDelay}
	}
	policy := &swarm.RestartPolicy{Condition: swarm.RestartPolicyCondition(rp.Condition)}
	if rp.Delay > 0 {
		d := rp.Delay
		policy.Delay = &d
	}
	if rp.MaxAttempts > 0 {
		policy.MaxAttempts = &rp.MaxAttempts
	}
	if rp.Window > 0 {
		w := rp.Window
		policy.Window = &w
	}
	task.RestartPolicy = policy

	serviceSpec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name:   spec.Name,
			Labels: spec.ServiceLabels,
		},
		TaskTemplate: task,
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   spec.UpdateParallelism,
			Delay:         spec.UpdateDelay,
			FailureAction: managedFailureAction,
			Monitor:       managedMonitor,
			Order:         swarm.UpdateOrder(spec.UpdateOrder),
		},
	}
	if spec.Global {
		serviceSpec.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	} else {
		replicas := spec.Replicas
		serviceSpec.Mode = swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}
	}
	return serviceSpec
}

// swarmHealthcheck 翻译健康检查（平台缺省已由引擎规划层补齐）。
func swarmHealthcheck(hc engine.HealthcheckSpec) *container.HealthConfig {
	out := &container.HealthConfig{
		Test:     hc.Test,
		Interval: hc.Interval,
		Timeout:  hc.Timeout,
		Retries:  int(min(hc.Retries, math.MaxInt32)),
	}
	if hc.StartPeriod > 0 {
		out.StartPeriod = hc.StartPeriod
	}
	return out
}

// ServiceCreate 实现 engine.Substrate：创建服务（调用方对账保证仅缺失时
// 调用；已存在不覆盖——按 ErrObjectConflict 语义失败暴露竞态）。
func (c *Client) ServiceCreate(ctx context.Context, spec engine.ServiceSpec) error {
	sw := buildSwarmSpec(spec)
	if _, err := c.cli.ServiceCreate(ctx, mobyclient.ServiceCreateOptions{Spec: sw}); err != nil {
		return fmt.Errorf("substrate: service create %s: %w", spec.Name, err)
	}
	return nil
}

// serviceUpdateRetry 是乐观令牌冲突的短重试预算（适配器内部取版本 +
// 更新；引擎不做跨调用令牌传递）。
const serviceUpdateRetry = 3

// ServiceUpdate 实现 engine.Substrate：读当前版本 → 以目标 spec 推进。
// ForceUpdate 恒不递增（归位零成本，Spike B2）。并发冲突短重试。
func (c *Client) ServiceUpdate(ctx context.Context, name string, spec engine.ServiceSpec) error {
	sw := buildSwarmSpec(spec)
	var lastErr error
	for i := 0; i < serviceUpdateRetry; i++ {
		res, err := c.cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
		if err != nil {
			return mapSubstrateErr(fmt.Errorf("substrate: service inspect %s: %w", name, err))
		}
		if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
			Version: swarm.Version{Index: res.Service.Version.Index},
			Spec:    sw,
		}); err != nil {
			lastErr = err
			if errdefs.IsConflict(err) {
				continue // 版本令牌被并发写推进：重读重试
			}
			return mapSubstrateErr(fmt.Errorf("substrate: service update %s: %w", name, err))
		}
		return nil
	}
	return mapSubstrateErr(fmt.Errorf("substrate: service update %s: %w", name, lastErr))
}

// ServiceRemove 实现 engine.Substrate：删除服务（省略=删除；幂等——缺失
// 视为成功，对账重放的常见形态）。
func (c *Client) ServiceRemove(ctx context.Context, name string) error {
	if _, err := c.cli.ServiceRemove(ctx, name, mobyclient.ServiceRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("substrate: service remove %s: %w", name, err)
	}
	return nil
}

// ServiceInspect 实现 engine.Substrate：按名取服务实况投影。
func (c *Client) ServiceInspect(ctx context.Context, name string) (engine.ServiceState, error) {
	res, err := c.cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return engine.ServiceState{}, fmt.Errorf("%w: %s", engine.ErrServiceNotFound, name)
		}
		return engine.ServiceState{}, fmt.Errorf("substrate: service inspect %s: %w", name, err)
	}
	return serviceToState(res.Service), nil
}

// ServiceList 实现 engine.Substrate：按 label 选择器返回服务投影。
func (c *Client) ServiceList(ctx context.Context, labels map[string]string) ([]engine.ServiceState, error) {
	filters := mobyclient.Filters{}
	for k, v := range labels {
		filters = filters.Add("label", k+"="+v)
	}
	res, err := c.cli.ServiceList(ctx, mobyclient.ServiceListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("substrate: service list: %w", err)
	}
	out := make([]engine.ServiceState, 0, len(res.Items))
	for _, svc := range res.Items {
		out = append(out, serviceToState(svc))
	}
	return out, nil
}

// TaskList 实现 engine.Substrate：返回服务的全部任务（service ps 语义）。
func (c *Client) TaskList(ctx context.Context, serviceName string) ([]engine.TaskState, error) {
	res, err := c.cli.TaskList(ctx, mobyclient.TaskListOptions{
		Filters: mobyclient.Filters{}.Add("service", serviceName),
	})
	if err != nil {
		return nil, fmt.Errorf("substrate: task list %s: %w", serviceName, err)
	}
	out := make([]engine.TaskState, 0, len(res.Items))
	for _, t := range res.Items {
		item := engine.TaskState{
			ID:           t.ID,
			Slot:         t.Slot,
			State:        string(t.Status.State),
			DesiredState: string(t.DesiredState),
			Err:          t.Status.Err,
			Timestamp:    t.Status.Timestamp,
		}
		if t.Spec.ContainerSpec != nil {
			item.Image = t.Spec.ContainerSpec.Image
		}
		out = append(out, item)
	}
	return out, nil
}

// serviceToState 把 swarm.Service 投影为核心类型（UpdateStatus 逐字镜像：
// paused + Message 是更新失败的平台判定来源，Spike B）。
func serviceToState(svc swarm.Service) engine.ServiceState {
	out := engine.ServiceState{
		Name:    svc.Spec.Name,
		Version: svc.Version.Index,
		Labels:  svc.Spec.Labels,
	}
	if svc.Spec.TaskTemplate.ContainerSpec != nil {
		out.Image = svc.Spec.TaskTemplate.ContainerSpec.Image
	}
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		out.Replicas = *svc.Spec.Mode.Replicated.Replicas
	}
	if svc.UpdateStatus != nil {
		out.UpdateState = string(svc.UpdateStatus.State)
		out.UpdateMessage = svc.UpdateStatus.Message
	}
	if v, ok := out.Labels[state.LabelDesiredHash]; ok {
		out.DesiredHash = v
	}
	return out
}
