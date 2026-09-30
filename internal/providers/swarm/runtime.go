package swarm

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// anchoringPollInterval 是 Watch 循环里节点锚定扫描的节拍（节点加入检出
// 上限 ≈ 该间隔；事件流覆盖即时路径）。
const anchoringPollInterval = 10 * time.Second

// Ensure 幂等下发期望状态（架构 §5：唯一写动词）。
//
// 域内收敛语义：以 ns 选择器列出 fleetly 管辖的现存服务，逐 Workload
// create-or-update（update 以 spec 全量替换 + 版本号），不在期望集内的
// 域内服务移除——同 Generation 重放安全（领域模型场景 1：发布中途被杀，
// 重启后按 Generation 幂等重下发）。
func (p *Provider) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) error {
	existing, err := p.listNsServices(ctx, ns)
	if err != nil {
		return fmt.Errorf("swarm ensure %s: list existing: %w", ns, err)
	}

	desired := make(map[string]struct{}, len(ws))
	for _, w := range ws {
		spec := toServiceSpec(ns, w, gen)
		desired[spec.Name] = struct{}{}

		auth, err := p.registryAuthFor(ctx, w.Image, m)
		if err != nil {
			return fmt.Errorf("swarm ensure %s: %w", ns, err)
		}

		inspect, err := p.cli.ServiceInspect(ctx, spec.Name, client.ServiceInspectOptions{})
		if err != nil {
			// 视 404 与其余错误：NotFound → create；其他错误直接上抛。
			if !isNotFound(err) {
				return fmt.Errorf("swarm ensure %s: inspect %s: %w", ns, spec.Name, err)
			}
			if _, err := p.cli.ServiceCreate(ctx, client.ServiceCreateOptions{
				Spec:                spec,
				EncodedRegistryAuth: auth,
			}); err != nil {
				return fmt.Errorf("swarm ensure %s: create %s: %w", ns, spec.Name, err)
			}
			continue
		}
		svc := inspect.Service
		if _, err := p.cli.ServiceUpdate(ctx, svc.ID, client.ServiceUpdateOptions{
			Version:             svc.Version,
			Spec:                spec,
			EncodedRegistryAuth: auth,
			QueryRegistry:       false,
			Rollback:            "",
		}); err != nil {
			return fmt.Errorf("swarm ensure %s: update %s: %w", ns, spec.Name, err)
		}
	}

	// 域内收敛：期望集之外的 fleetly 管辖服务移除。
	for _, svc := range existing {
		if _, ok := desired[svc.Spec.Name]; !ok {
			if _, err := p.cli.ServiceRemove(ctx, svc.ID, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
				return fmt.Errorf("swarm ensure %s: remove stale %s: %w", ns, svc.Spec.Name, err)
			}
		}
	}
	return nil
}

// Remove 拆除隔离域内全部载体（幂等）。
func (p *Provider) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return fmt.Errorf("swarm remove %s: %w", ns, err)
	}
	for _, svc := range services {
		if _, err := p.cli.ServiceRemove(ctx, svc.ID, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
			return fmt.Errorf("swarm remove %s: %w", ns, err)
		}
	}
	return nil
}

// Watch 返回全集群状态流：service/node 事件映射为 WorkloadEvent（按
// fleetly.* 标记搬运还原平台 ID），节点加入经锚定扫描上报 node.joined。
// 事件流断开自动重连（provider 内自愈；消费方只感知 channel 关闭 =
// ctx 取消）。
func (p *Provider) Watch(ctx context.Context) (<-chan capability.WorkloadEvent, error) {
	// 启动即做一次锚定扫描：新节点即刻上报 node.joined。
	out := make(chan capability.WorkloadEvent, 64)
	if err := p.anchorNodes(ctx, out); err != nil {
		return nil, fmt.Errorf("swarm watch: initial node anchoring: %w", err)
	}
	go p.watchLoop(ctx, out)
	return out, nil
}

func (p *Provider) watchLoop(ctx context.Context, out chan<- capability.WorkloadEvent) {
	defer close(out)
	eventsRes := p.cli.Events(ctx, client.EventsListOptions{
		Filters: client.Filters{}.Add("type", "service", "node", "container"),
	})
	anchorTicker := time.NewTicker(anchoringPollInterval)
	defer anchorTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-eventsRes.Err:
			if !ok {
				return
			}
			// 事件流错误（含 EOF）：退避后重开（daemon 重启等场景）。
			if ctx.Err() != nil {
				return
			}
			time.Sleep(time.Second)
			eventsRes = p.cli.Events(ctx, client.EventsListOptions{
				Filters: client.Filters{}.Add("type", "service", "node", "container"),
			})
			continue
		case msg, ok := <-eventsRes.Messages:
			if !ok {
				continue
			}
			if ev, ok := p.mapEvent(msg); ok {
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		case <-anchorTicker.C:
			// 锚定扫描同时充当节点存活观测（node leave 事件经 mapEvent）。
			_ = p.anchorNodes(ctx, out)
		}
	}
}

// mapEvent 把 swarm 事件翻译为 WorkloadEvent（读不到平台标记的载体忽略
// ——非 fleetly 管辖）。
func (p *Provider) mapEvent(msg events.Message) (capability.WorkloadEvent, bool) {
	switch msg.Type {
	case "service":
		labels := msg.Actor.Attributes
		if labels[labelManaged] != "true" {
			return capability.WorkloadEvent{}, false
		}
		gen, _ := strconv.ParseUint(labels[labelGeneration], 10, 64)
		return capability.WorkloadEvent{
			WorkloadID: labels[labelWorkload],
			Generation: capability.Generation(gen),
			State:      serviceEventState(string(msg.Action)),
			Message:    string(msg.Action),
		}, labels[labelWorkload] != ""
	case "node":
		return capability.WorkloadEvent{
			NodeJoined: &capability.NodeJoined{
				NodeID:    msg.Actor.Attributes[labelNodeID],
				CarrierID: msg.Actor.ID,
			},
		}, msg.Actor.Attributes[labelNodeID] != ""
	default:
		return capability.WorkloadEvent{}, false
	}
}

// serviceEventState 把 swarm service 事件动作映射为观测状态。
func serviceEventState(action string) capability.WorkloadState {
	switch action {
	case "remove":
		return capability.WorkloadStopped
	default:
		return capability.WorkloadRunning
	}
}

// anchorNodes 扫描全部节点：无平台 ID 标记者铸造 ULID、写回节点 label、
// 上报 node.joined（架构 §5 节点身份锚定契约义务；D-MN-8 节点 ID 永不
// 复用——锚定后平台权威表持有映射）。
func (p *Provider) anchorNodes(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	list, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return fmt.Errorf("node list: %w", err)
	}
	for _, node := range list.Items {
		if id := node.Spec.Labels[labelNodeID]; id != "" {
			continue
		}
		minted := ulid.Make().String()
		spec := node.Spec
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		// 复制标记集：NodeUpdate 全量替换 Spec.Labels，直接改会踩共享 map。
		labels := make(map[string]string, len(spec.Labels)+1)
		for k, v := range spec.Labels {
			labels[k] = v
		}
		labels[labelNodeID] = minted
		spec.Labels = labels
		if _, err := p.cli.NodeUpdate(ctx, node.ID, client.NodeUpdateOptions{
			Version: node.Version,
			Spec:    spec,
		}); err != nil {
			return fmt.Errorf("mint node id on %s: %w", node.ID, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- capability.WorkloadEvent{NodeJoined: &capability.NodeJoined{
			NodeID:    minted,
			CarrierID: node.ID,
			Minted:    true,
		}}:
		}
	}
	return nil
}

// Addresses 返回隔离域可达地址（overlay VIP / 服务 DNS 名；平台无关形态）。
func (p *Provider) Addresses(ctx context.Context, ns capability.NamespaceRef) ([]capability.Endpoint, error) {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("swarm addresses %s: %w", ns, err)
	}
	var endpoints []capability.Endpoint
	for _, svc := range services {
		addr := svc.Spec.Name // overlay DNS 名（网络内可解析）
		for _, vip := range svc.Endpoint.VirtualIPs {
			if vip.Addr.IsValid() {
				addr = vip.Addr.Addr().String()
				break
			}
		}
		for _, port := range parsePortsLabel(svc.Spec.Labels[labelPorts]) {
			endpoints = append(endpoints, capability.Endpoint{
				Addr:    addr,
				Process: svc.Spec.Labels[labelProcess],
				Port:    port.Port,
			})
		}
	}
	return endpoints, nil
}

// DescribeCluster 返回集群观测视图（节点缓存；权威归属判定永远查平台表）。
func (p *Provider) DescribeCluster(ctx context.Context) (capability.ClusterView, error) {
	var view capability.ClusterView
	if err := p.anchorNodes(ctx, nil); err != nil {
		// 锚定失败不阻断观测（下次扫描重试）；错误如实带出。
		return view, err
	}
	list, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return view, fmt.Errorf("swarm describe cluster: %w", err)
	}
	for _, node := range list.Items {
		view.Nodes = append(view.Nodes, capability.NodeView{
			NodeID:    node.Spec.Labels[labelNodeID],
			CarrierID: node.ID,
			Hostname:  node.Description.Hostname,
			Role:      string(node.Spec.Role),
			Available: node.Status.State == swarm.NodeStateReady,
			Labels:    node.Spec.Labels,
		})
	}
	return view, nil
}

// Enrollment 生成节点加入材料（worker 加入命令；节点零平台安装物）。
// 管理面地址取可达 manager 的广播地址（N1 HA 扩容再扩展 manager 命令）。
func (p *Provider) Enrollment(ctx context.Context) (capability.EnrollKit, error) {
	inspect, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return capability.EnrollKit{}, fmt.Errorf("swarm enrollment: %w", err)
	}
	managerAddr := ""
	list, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err == nil {
		for _, node := range list.Items {
			if node.ManagerStatus != nil && node.ManagerStatus.Addr != "" {
				managerAddr = node.ManagerStatus.Addr
				break
			}
		}
	}
	if managerAddr == "" {
		return capability.EnrollKit{}, fmt.Errorf("swarm enrollment: no reachable manager advertise address")
	}
	workerToken := inspect.Swarm.JoinTokens.Worker
	managerToken := inspect.Swarm.JoinTokens.Manager
	return capability.EnrollKit{
		Command:        fmt.Sprintf("docker swarm join --token %s %s", workerToken, managerAddr),
		ManagerCommand: fmt.Sprintf("docker swarm join --token %s %s", managerToken, managerAddr),
	}, nil
}

// listNsServices 列出隔离域内 fleetly 管辖的服务。
func (p *Provider) listNsServices(ctx context.Context, ns capability.NamespaceRef) ([]swarm.Service, error) {
	filters := client.Filters{}.Add("label", labelSelectorArgs(ns)...)
	res, err := p.cli.ServiceList(ctx, client.ServiceListOptions{Filters: filters})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// labelSelectorArgs 生成 label 全等过滤参数（label=k=v 形态）。
func labelSelectorArgs(ns capability.NamespaceRef) []string {
	args := make([]string, 0, 4)
	for k, v := range nsSelector(ns) {
		args = append(args, k+"="+v)
	}
	sort.Strings(args)
	return args
}

// registryAuthFor 解析镜像引用对应仓库的拉取凭证并编码（无匹配凭证返回
// 空串 = 匿名拉取；凭证不落载体，ADR-0014）。
func (p *Provider) registryAuthFor(ctx context.Context, image string, m capability.Materials) (string, error) {
	if len(m.RegistryAuth) == 0 {
		return "", nil
	}
	host := imageRegistryHost(image)
	cred, ok := m.RegistryAuth[host]
	if !ok {
		return "", nil
	}
	return encodeRegistryAuth(cred)
}

// imageRegistryHost 提取镜像引用的仓库主机（含默认 docker.io 归一）。
func imageRegistryHost(image string) string {
	if i := strings.IndexByte(image, '/'); i >= 0 {
		first := image[:i]
		// 含 . 或 : 或 == localhost 视为 registry 主机，否则为默认仓库
		// 的官方镜像命名空间。
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first
		}
	}
	return "docker.io"
}

// stripCIDRPrefix 已随 netip.Prefix 迁移退役（VIP 直接取 Addr().String()）。

// isNotFound 报告错误是否对象不存在（幂等 Remove/收敛用）。
func isNotFound(err error) bool {
	return errdefs.IsNotFound(err)
}
