package swarm

import (
	"context"
	"fmt"
	"log/slog"
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
	// 材料先行（ADR-0014）：网络 create-or-get + Secret 载体落盘，再翻译
	// 载体 spec（引用载体名）。
	if err := p.ensureNetworks(ctx, ns, ws); err != nil {
		return fmt.Errorf("swarm ensure %s: %w", ns, err)
	}
	secretCarriers, err := p.ensureSecrets(ctx, m)
	if err != nil {
		return fmt.Errorf("swarm ensure %s: %w", ns, err)
	}

	existing, err := p.listNsServices(ctx, ns)
	if err != nil {
		return fmt.Errorf("swarm ensure %s: list existing: %w", ns, err)
	}

	desired := make(map[string]struct{}, len(ws))
	for _, w := range ws {
		spec := toServiceSpec(ns, w, gen, secretCarriers)
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
		if err := p.updateServiceCAS(ctx, ns, spec, svc, auth); err != nil {
			return err
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

// updateServiceCAS 以版本号 CAS 更新服务，冲突时有界重试。引擎事件驱动
// Kick 在滚动替换期会把幂等重 Ensure 压到近零间隔连拍，inspect 读到的
// 版本可能落后 store 可见性（docker 29 真机实证："update out of
// sequence"，发布中重部署即触发）——重取版本再试；同 spec 重放本身幂等，
// 重试即正确（服务端拒绝的更新未生效）。
func (p *Provider) updateServiceCAS(ctx context.Context, ns capability.NamespaceRef, spec swarm.ServiceSpec, current swarm.Service, auth string) error {
	const maxAttempts = 3
	svc := current
	for attempt := 1; ; attempt++ {
		_, err := p.cli.ServiceUpdate(ctx, svc.ID, client.ServiceUpdateOptions{
			Version:             svc.Version,
			Spec:                spec,
			EncodedRegistryAuth: auth,
			QueryRegistry:       false,
			Rollback:            "",
		})
		if err == nil {
			return nil
		}
		if !isUpdateOutOfSequence(err) || attempt >= maxAttempts {
			return fmt.Errorf("swarm ensure %s: update %s: %w", ns, spec.Name, err)
		}
		inspect, ierr := p.cli.ServiceInspect(ctx, spec.Name, client.ServiceInspectOptions{})
		if ierr != nil {
			return fmt.Errorf("swarm ensure %s: re-inspect %s: %w", ns, spec.Name, ierr)
		}
		svc = inspect.Service
	}
}

// isUpdateOutOfSequence 识别 swarmkit 版本冲突：swarmkit 以 code=Unknown
// 返回，跨 API 边界无类型化哨兵，按其稳定文案匹配。
func isUpdateOutOfSequence(err error) bool {
	return err != nil && strings.Contains(err.Error(), "update out of sequence")
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
			// ctx 感知退避（Q-19）：time.Sleep 不看 ctx，停机/取消期间
			// 会白等一秒才退出。
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
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
			// 锚定扫描同时充当节点存活观测（node leave 事件经 mapEvent）；
			// 任务轮询补充 service 事件缺 labels 时的观测权威（L1 数据源）。
			// 失败不致命（下拍重试）但绝不静默（Q-19）：pollTasks 是 L1
			// 就绪的权威数据源，静默失败 = 部署无诊断卡到超时。
			if err := p.anchorNodes(ctx, out); err != nil {
				slog.Warn("swarm watch: node anchoring scan failed", "error", err)
			}
			if err := p.pollTasks(ctx, out); err != nil {
				slog.Warn("swarm watch: task poll failed; readiness observation degraded until next tick", "error", err)
			}
		}
	}
}

// pollTasks 用 TaskList 快照生成 workload 观测：swarm 的 service 事件
// Actor.Attributes 不携带 spec labels（Generation 观测缺锚），任务快照
// 从 Service.Spec.Labels 还原平台标记——10s 轮询是 L1 的权威路径，事件
// 流提供即时唤醒。
func (p *Provider) pollTasks(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	tasks, err := p.cli.TaskList(ctx, client.TaskListOptions{})
	if err != nil {
		return fmt.Errorf("task list: %w", err)
	}
	svcLabels := map[string]map[string]string{}
	for _, t := range tasks.Items {
		sid := t.ServiceID
		if _, ok := svcLabels[sid]; ok {
			continue
		}
		svc, err := p.cli.ServiceInspect(ctx, sid, client.ServiceInspectOptions{})
		if err != nil {
			continue
		}
		svcLabels[sid] = svc.Service.Spec.Labels
	}
	for _, t := range tasks.Items {
		labels := svcLabels[t.ServiceID]
		if labels[labelManaged] != "true" {
			continue
		}
		// 历史任务（已被替换/关闭：desired 不是 running）不计观测——
		// App 域只看活槽位，避免滚动替换期的旧 task 状态污染 last-write-wins
		// 槽。Task 域例外（ADR-0025 决策 2/8）：one-shot Run 的终态任务
		//（complete/failed）与排空缩零的 shutdown 任务必须可见，否则已完成
		// 的 Run 整个不可见。
		terminalVisible := labels[labelTask] != "" && terminalTaskState(t.Status.State)
		if t.DesiredState != swarm.TaskStateRunning && !terminalVisible {
			continue
		}
		gen, _ := strconv.ParseUint(labels[labelGeneration], 10, 64)
		ev := capability.WorkloadEvent{
			WorkloadID: labels[labelWorkload],
			Generation: capability.Generation(gen),
			State:      taskEventState(t.Status.State),
			Node:       labels[labelNodeID],
			Message:    taskStatusMessage(t),
			Reason:     t.Status.Err,
			Instance:   t.ID,
		}
		// 终态观测携带退出码（complete=exit 0 形态；failed=非 0）。
		if terminalTaskState(t.Status.State) {
			code := t.Status.ContainerStatus.ExitCode
			ev.ExitCode = &code
		}
		if ev.WorkloadID == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- ev:
		}
	}
	return nil
}

// terminalTaskState 报告 swarm task 状态是否一次性终态（ADR-0025 决策 2：
// 完成/失败/关闭/rejected——终态观测携带退出码与实例身份）。
func terminalTaskState(s swarm.TaskState) bool {
	switch s {
	case swarm.TaskStateComplete, swarm.TaskStateFailed, swarm.TaskStateShutdown, swarm.TaskStateRejected:
		return true
	default:
		return false
	}
}

// taskStatusMessage 是观测的人读补充（终态带编排器原因原文）。
func taskStatusMessage(t swarm.Task) string {
	if t.Status.Err != "" {
		return string(t.Status.State) + ": " + t.Status.Err
	}
	return string(t.Status.State)
}

// taskEventState 把 swarm task 状态映射为观测状态（L1 数据源，N0 修复批
// A2 收紧 + ADR-0025 决策 2）：仅 running 计 running——placement 落空
// （new/allocated/assigned/preparing/pending/starting 族）计 pending，让 L1
// 门保持关闭直至真就绪或超时失败；complete/failed/rejected 是一次性终态
// （completed/failed，不再被 degraded 吞并）；shutdown 计 stopped（Task 域
// 排空缩零路径）。
func taskEventState(s swarm.TaskState) capability.WorkloadState {
	switch s {
	case swarm.TaskStateRunning:
		return capability.WorkloadRunning
	case swarm.TaskStateComplete:
		return capability.WorkloadCompleted
	case swarm.TaskStateFailed, swarm.TaskStateRejected:
		return capability.WorkloadFailed
	case swarm.TaskStateShutdown:
		return capability.WorkloadStopped
	default:
		return capability.WorkloadPending
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

// serviceEventState 把 swarm service 事件动作映射为观测状态（N0 修复批
// A2 收紧）：create/update 只代表 spec 变化、不代表载体就绪——计 pending
// （就绪以 10s 任务轮询为权威），remove 计 stopped。
func serviceEventState(action string) capability.WorkloadState {
	switch action {
	case "remove":
		return capability.WorkloadStopped
	default:
		return capability.WorkloadPending
	}
}

// anchorNodes 扫描全部节点：无平台 ID 标记者铸造 ULID、写回节点 label、
// 上报 node.joined（架构 §5 节点身份锚定契约义务；D-MN-8 节点 ID 永不
// 复用——锚定后平台权威表持有映射）。
//
// 并发锚定竞态（Watch 初始扫描与 DescribeCluster 对账同拍运行）：节点
// 版本被另一路径推进时 NodeUpdate 报 out of sequence——重取版本重试；
// 发现他方已完成锚定（label 已在）即复用，不二次铸造。
func (p *Provider) anchorNodes(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	list, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return fmt.Errorf("node list: %w", err)
	}
	for _, node := range list.Items {
		if id := node.Spec.Labels[labelNodeID]; id != "" {
			continue
		}
		minted, err := p.mintNodeID(ctx, node)
		if err != nil {
			return fmt.Errorf("mint node id on %s: %w", node.ID, err)
		}
		if minted == "" {
			continue // 他方已完成锚定（复用其 label，不重复上报 joined）
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

// mintNodeID 铸造并写回单节点锚定标记；返回铸造的平台 ID（空串 = 他方
// 已完成）。版本竞态最多重试 3 次（每次重新 inspect 取新版本）。
func (p *Provider) mintNodeID(ctx context.Context, node swarm.Node) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if id := node.Spec.Labels[labelNodeID]; id != "" {
			return "", nil // 他方已完成
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
		_, err := p.cli.NodeUpdate(ctx, node.ID, client.NodeUpdateOptions{
			Version: node.Version,
			Spec:    spec,
		})
		if err == nil {
			return minted, nil
		}
		// 版本竞态：重取节点（另一锚定路径已推进版本）。
		inspect, ierr := p.cli.NodeInspect(ctx, node.ID, client.NodeInspectOptions{})
		if ierr != nil {
			return "", ierr
		}
		node = inspect.Node
	}
	return "", fmt.Errorf("node version raced 3 times")
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
// rotate=true 先作废全部现有材料（SwarmUpdate 带 token 轮换旗标）再取
// 新材料（C3：泄漏处置路径）。管理面地址取可达 manager 的广播地址
// （N1 HA 扩容再扩展 manager 命令）。
func (p *Provider) Enrollment(ctx context.Context, rotate bool) (capability.EnrollKit, error) {
	if rotate {
		cur, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
		if err != nil {
			return capability.EnrollKit{}, fmt.Errorf("swarm rotate: inspect: %w", err)
		}
		if _, err := p.cli.SwarmUpdate(ctx, client.SwarmUpdateOptions{
			Version:            cur.Swarm.Version,
			Spec:               cur.Swarm.Spec,
			RotateWorkerToken:  true,
			RotateManagerToken: true,
		}); err != nil {
			return capability.EnrollKit{}, fmt.Errorf("swarm rotate: update: %w", err)
		}
	}
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

// InspectWorkloads 实现 RuntimeInspector 子面（ADR-0022 spec 对照 drift）：
// ServiceList 快照 + 标记还原平台身份 + spec 读取（镜像/副本）。状态取
// 期望副本面（观测状态以 Watch 流/任务轮询为权威，此处仅 spec 对照用）。
func (p *Provider) InspectWorkloads(ctx context.Context, ns capability.NamespaceRef) ([]capability.WorkloadObservation, error) {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("swarm inspect %s: %w", ns, err)
	}
	out := make([]capability.WorkloadObservation, 0, len(services))
	for _, svc := range services {
		labels := svc.Spec.Labels
		if labels[labelManaged] != "true" {
			continue
		}
		gen, _ := strconv.ParseUint(labels[labelGeneration], 10, 64)
		obs := capability.WorkloadObservation{
			WorkloadID: labels[labelWorkload],
			Generation: capability.Generation(gen),
			Image:      svc.Spec.TaskTemplate.ContainerSpec.Image,
			// 入口覆盖命令回读（ADR-0022 spec 对照：人工 docker service
			// update --command 改载体须出 drift）。
			Command:  svc.Spec.TaskTemplate.ContainerSpec.Command,
			Replicas: 1,
			State:    capability.WorkloadRunning,
		}
		if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
			obs.Replicas = int64(*svc.Spec.Mode.Replicated.Replicas) //nolint:gosec // 副本计数域内（swarm 上限远小于 2^63）
		}
		if obs.WorkloadID != "" {
			out = append(out, obs)
		}
	}
	return out, nil
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
