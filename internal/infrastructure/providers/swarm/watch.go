package swarm

// 观测面（Watch 流 + 任务轮询 + 节点锚定 + 事件映射）：Watch 自 runtime.go
// 拆出（sizeguard 文件预算）——Ensure/账本/CAS 是写面，本文件是读面，缝
// 即读写分离；函数体逐字随迁零行为变更。

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Watch 返回全集群状态流：service/node 事件映射为 WorkloadEvent（按
// fleetly.* 标记搬运还原平台 ID），节点加入经锚定扫描上报 node.joined。
// 事件流断开自动重连（provider 内自愈；消费方只感知 channel 关闭 =
// ctx 取消）。
func (p *Provider) Watch(ctx context.Context) (<-chan capability.WorkloadEvent, error) {
	// 启动即做一次锚定扫描：新节点即刻上报 node.joined。失败降级不关闭
	// Watch 整门（B14-2）：daemon 短暂不可达（启动竞态/重启窗口）曾让
	// Watch 直接报错，观测流整体开天窗直至重开成功；锚定扫描本就有 10s
	// 节拍兜底（anchoringPollInterval），降级为记日志、循环内重试——事件
	// 流与任务轮询不被初始锚定牵连，节点加入检出最多延迟一个节拍。
	out := make(chan capability.WorkloadEvent, 64)
	if err := p.anchorNodes(ctx, out); err != nil {
		slog.Warn("swarm watch: initial node anchoring failed; degraded to periodic rescan", "error", err)
	}
	go p.watchLoop(ctx, out)
	return out, nil
}

func (p *Provider) watchLoop(ctx context.Context, out chan<- capability.WorkloadEvent) {
	defer close(out)
	// 事件水位锚（C20-2）：跨轮保存最后已见事件的时间戳，事件流断开重开
	// 时从锚后续传（Events API 的 since 形态）——从"现在"重放会漏掉断开
	// 窗口内的事件（daemon 重启窗口、网络抖动）。panic 击穿重开同样保留
	// 水位（watchRound 的 named return 在 recover 后仍持有最后赋值）。
	since := ""
	for {
		// panic 护栏（安全批 P0）：任务翻译/轮询路径的任何 panic 都会击穿
		// 整个 fleetlyd（进程崩溃循环）——watchRound 内层 recover 接住后
		// 重开循环（观测流保命，不永久消失）；ctx 已取消则正常收口。
		anchor, normal := p.watchRound(ctx, out, since)
		since = anchor
		if normal {
			return
		}
		if ctx.Err() != nil {
			return
		}
		// panic 后退避再重开（避免错误形态零间隔自激）；重开会重建事件流
		// 与锚定节拍器（断流自愈的既有语义）。
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// watchRound 跑一轮事件循环（事件流 + 节点锚定 + 任务轮询）。since 是进
// 入本轮的水位锚（空 = 只看现在）；返回 anchor = 本轮最后已见事件的时间
// 戳（调用方作下轮续传锚），normal = 正常收口（ctx 取消或事件流关闭），
// false = panic 击穿（recover 接住，由调用方决定重开）。
func (p *Provider) watchRound(ctx context.Context, out chan<- capability.WorkloadEvent, since string) (anchor string, normal bool) {
	anchor = since
	defer func() {
		if r := recover(); r != nil {
			slog.Error("swarm watch: recovered from panic; restarting the watch loop",
				"panic", r, "stack", string(debug.Stack()))
			normal = false
		}
	}()
	eventsRes := p.eventsStream(ctx, client.EventsListOptions{
		Since:   anchor,
		Filters: watchEventFilters(),
	})
	anchorTicker := time.NewTicker(anchoringPollInterval)
	defer anchorTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return anchor, true
		case _, ok := <-eventsRes.Err:
			if !ok {
				return anchor, true
			}
			// 事件流错误（含 EOF）：退避后重开（daemon 重启等场景），
			// 从水位锚续传（C20-2：断开窗口事件不丢）。
			if ctx.Err() != nil {
				return anchor, true
			}
			// ctx 感知退避（Q-19）：time.Sleep 不看 ctx，停机/取消期间
			// 会白等一秒才退出。
			select {
			case <-ctx.Done():
				return anchor, true
			case <-time.After(time.Second):
			}
			eventsRes = p.eventsStream(ctx, client.EventsListOptions{
				Since:   anchor,
				Filters: watchEventFilters(),
			})
			continue
		case msg, ok := <-eventsRes.Messages:
			if !ok {
				continue
			}
			// 水位推进先于映射：未映射事件同样标记时间事实（下一轮锚
			// 覆盖全部已见事件，不重放）。
			if a := eventAnchor(msg); a != "" {
				anchor = a
			}
			if ev, ok := p.mapEvent(msg); ok {
				select {
				case out <- ev:
				case <-ctx.Done():
					return anchor, true
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

// watchEventFilters 是 Watch 事件订阅的过滤器：只订阅 mapEvent 有映射的
// 类型。container 不订阅（C20-1）：mapEvent 无 container 分支，订阅即无
// 消费者——daemon 把全集群最高频的事件流（start/die/oom/health…）灌进
// 无缓冲 Messages，每条白占一次循环调度再被丢弃；任务状态观测的权威是
// pollTasks 轮询，不依赖容器事件。
func watchEventFilters() client.Filters {
	return client.Filters{}.Add("type", "service", "node")
}

// eventAnchor 把事件时间戳折算为 Events 重开的 since 锚（docker Events
// API 的 "<sec>.<nano>" unix 形态，客户端 GetTimestamp 原样透传）。锚按
// daemon 语义含端点（>= since），重开可能重放锚点事件一条——观测是
// last-write-wins 幂等槽位，重复无害；漏事件才是害。零时间事件返回空串
// （不推进水位：异常/无时间戳消息不产生假锚）。
func eventAnchor(msg events.Message) string {
	switch {
	case msg.TimeNano > 0:
		return fmt.Sprintf("%d.%09d", msg.TimeNano/int64(time.Second), msg.TimeNano%int64(time.Second))
	case msg.Time > 0:
		return strconv.FormatInt(msg.Time, 10)
	default:
		return ""
	}
}

// pollTasks 生成 workload 观测：先按 managed 标记列出受管服务（选择器
// 真源 = translate.go 的 labelManaged，与 Ensure/listNsServices 同一标记），
// 再逐服务按 service 过滤列任务——调用量级 = 受管服务数 + 1，而非修复前
// 的全集群 TaskList（每条任务×每服务一次 inspect 的 N+1）。服务级
// Spec.Labels 由列表直接随行（修复前要逐服务 ServiceInspect 才能拿到），
// 任务级 ContainerSpec.Labels 优先（B14-1）经 taskLabels 兜底合并——
// swarm 的 service 事件 Actor.Attributes 不携带 spec labels（Generation
// 观测缺锚），10s 轮询是 L1 的权威路径，事件流提供即时唤醒。
//
// 非 Task 域逐 workload 聚合单发（aggregateWorkloadEvents）：引擎观测槽
// 按 workload ID last-write-wins，单进程多 task（多副本 / 滚动窗口两代
// 并存 / paused start-first 旧代残留）逐 task 直发会在同一槽位跨代互覆
// ——旧代 running@N-1 盖掉新代的真实状态，L2 看门狗失明、drift 去抖签名
// 被打穿成 10s 风暴（staging 2026-10-16 实证：232+ 条 workload.drift_
// detected 刷屏 + 部署对崩溃重启环的新代假通过）。Task 域不聚合（one-
// shot 终态可见性，ADR-0025 决策 2：每个 Run 任务的终态独立可见）。
//
// 单服务任务列表失败不中断整轮（C19-2 哨兵）：记 Warn（带服务名）继续其
// 余服务——修复前 ServiceInspect 失败 continue 但不入 memo，同服务的每个
// 任务都重复撞一次失败调用（N+1 放大），且失败静默无诊断线索。
func (p *Provider) pollTasks(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	services, err := p.listManagedServices(ctx)
	if err != nil {
		return fmt.Errorf("managed service list: %w", err)
	}
	for i := range services {
		svc := services[i]
		tasks, err := p.listServiceTasks(ctx, svc.ID)
		if err != nil {
			// 哨兵：单服务失败只降级该服务的本轮观测，其余服务照常。
			slog.Warn("swarm watch: task poll skipped a service until next tick", "service", svc.Spec.Name, "error", err)
			continue
		}
		var perTask, aggregated []capability.WorkloadEvent
		for _, t := range tasks.Items {
			labels := taskLabels(t, svc.Spec.Labels)
			ev, ok := taskEvent(t, labels)
			if !ok {
				continue
			}
			if labels[labelTask] != "" {
				perTask = append(perTask, ev) // Task 域：逐 task 终态可见
			} else {
				aggregated = append(aggregated, ev)
			}
		}
		aggregated = aggregateWorkloadEvents(aggregated, serviceWireGen(svc))
		for _, ev := range append(perTask, aggregated...) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- ev:
			}
		}
	}
	return nil
}

// aggregateWorkloadEvents 把同一服务的逐 task 观测收敛为逐 workload 单发
// （纯函数，pollTasks 与单测共用）。圈定口径：优先 spec 侧代次子集
// （serviceWireGen = 服务已被受理的期望代——滚动中/停摆时新代 task 的
// 真实状态是 L1/L2 的判定对象，旧代残留不得入槽）；spec 代次为 0（历史
// 行）或该代暂无 task（刚受理未建）回退全量中的最高代次子集。聚合状态
// = 子集全 running 才 running；任一非 running 即按最重非 running 态上报
// （stopped/终态优先于 pending——诊断价值），多 task 时消息带 n/m 计数。
func aggregateWorkloadEvents(evs []capability.WorkloadEvent, specGen uint64) []capability.WorkloadEvent {
	byWID := map[string][]capability.WorkloadEvent{}
	var order []string
	for _, ev := range evs {
		if _, seen := byWID[ev.WorkloadID]; !seen {
			order = append(order, ev.WorkloadID)
		}
		byWID[ev.WorkloadID] = append(byWID[ev.WorkloadID], ev)
	}
	out := make([]capability.WorkloadEvent, 0, len(order))
	for _, wid := range order {
		group := byWID[wid]
		pick := genSubset(group, specGen)
		if pick == nil {
			var max uint64
			for _, ev := range group {
				if uint64(ev.Generation) > max {
					max = uint64(ev.Generation)
				}
			}
			pick = genSubset(group, max)
		}
		out = append(out, aggregateOne(pick))
	}
	return out
}

// genSubset 圈定某代次的观测子集（gen=0 或无命中返回 nil——调用方回退）。
func genSubset(group []capability.WorkloadEvent, gen uint64) []capability.WorkloadEvent {
	if gen == 0 {
		return nil
	}
	var sub []capability.WorkloadEvent
	for _, ev := range group {
		if uint64(ev.Generation) == gen {
			sub = append(sub, ev)
		}
	}
	return sub
}

// aggregateOne 把同代次子集聚成单条观测：全 running 直取代表；否则取
// 最重非 running 态作代表（stopped/终态 > 其余），多 task 时消息带 n/m
// 计数（单 task 保真原消息——停摆/崩溃的第一现场文案不稀释）。
func aggregateOne(sub []capability.WorkloadEvent) capability.WorkloadEvent {
	rep := sub[0]
	running := 0
	var worst *capability.WorkloadEvent
	for i := range sub {
		ev := &sub[i]
		if ev.State == capability.WorkloadRunning {
			running++
			continue
		}
		if worst == nil || stateSeverity(*ev) > stateSeverity(*worst) {
			worst = ev
		}
	}
	if running == len(sub) {
		return rep
	}
	out := *worst
	if len(sub) > 1 {
		out.Message = fmt.Sprintf("%d/%d running; %s", running, len(sub), out.Message)
	}
	return out
}

// stateSeverity 是聚合代表的非 running 态排序（诊断价值降序）：终态与
// stopped 携带失败现场（退出码/编排器原因），优先于 pending/degraded。
func stateSeverity(ev capability.WorkloadEvent) int {
	switch ev.State {
	case capability.WorkloadStopped, capability.WorkloadFailed, capability.WorkloadCompleted:
		return 2
	case capability.WorkloadDegraded:
		return 1
	default:
		return 0
	}
}

// taskLabels 返回任务观测的身份标记集：任务级 ContainerSpec.Labels 优先
// （B14-1：任务创建时冻结的 spec——滚动窗口内旧任务的 Generation 归属锚
// 定在创建时的 gen；服务级 Spec.Labels 恒是当前值，滚动窗口内旧任务会被
// 误归因到新 gen）；任务级缺失（Spec 未回填形态）回退服务级——修复前的
// 既有取法，兜底不回退。
func taskLabels(t swarm.Task, serviceLabels map[string]string) map[string]string {
	if t.Spec.ContainerSpec != nil && len(t.Spec.ContainerSpec.Labels) > 0 {
		return t.Spec.ContainerSpec.Labels
	}
	return serviceLabels
}

// taskEvent 由单条 Task 快照构造观测事件（纯函数，pollTasks 与单测共用；
// ok=false 表示该任务不计观测）。抽成纯函数是因为 pollTasks 依赖真
// daemon 客户端——终态 nil ContainerStatus 的回归面在纯函数上钉（安全批
// P0）。
func taskEvent(t swarm.Task, labels map[string]string) (capability.WorkloadEvent, bool) {
	if labels[labelManaged] != "true" {
		return capability.WorkloadEvent{}, false
	}
	// 历史任务（已被替换/关闭：desired 不是 running）不计观测——
	// App 域只看活槽位，避免滚动替换期的旧 task 状态污染 last-write-wins
	// 槽。Task 域例外（ADR-0025 决策 2）：one-shot Run 的终态任务
	//（complete/failed）与排空缩零的 shutdown 任务必须可见，否则已完成
	// 的 Run 整个不可见。
	terminalVisible := labels[labelTask] != "" && terminalTaskState(t.Status.State)
	if t.DesiredState != swarm.TaskStateRunning && !terminalVisible {
		return capability.WorkloadEvent{}, false
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
	// 终态观测携带退出码（complete=exit 0 形态；failed=非 0）。ContainerStatus
	// 是指针：rejected/shutdown 等未建容器的终态为 nil——解引用会击穿
	// Watch goroutine（P0）。nil 时退出码保持缺失（下游对 failed+nil 显示
	// unknown 的既有语义）。
	if terminalTaskState(t.Status.State) {
		if cs := t.Status.ContainerStatus; cs != nil {
			code := cs.ExitCode
			ev.ExitCode = &code
		}
	}
	if ev.WorkloadID == "" {
		return capability.WorkloadEvent{}, false
	}
	return ev, true
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
// 复用——锚定后平台权威表持有映射）。out 可为 nil：对账模式
// （DescribeCluster）只铸造写回锚定标记，不上报——向 nil channel 发送会
// 永久阻塞到 ctx 取消（观测对账没有事件消费方，joined 上报是 Watch 锚定
// 扫描的职责）。
//
// 并发锚定竞态（Watch 初始扫描与 DescribeCluster 对账同拍运行）：节点
// 版本被另一路径推进时 NodeUpdate 报 out of sequence——重取版本重试；
// 发现他方已完成锚定（label 已在）即复用，不二次铸造。
func (p *Provider) anchorNodes(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	list, err := p.nodes(ctx)
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
		if out == nil {
			continue // 对账模式：只铸造写回，不上报（joined 归 Watch 锚定扫描）
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

// nodes 列出集群节点（可注入缝；nil = 真 daemon 直连——provider.go 的
// 缝契约：覆盖 Watch 初始锚定降级、admin 节点定位与节点观测的 hermetic
// 测试面）。
func (p *Provider) nodes(ctx context.Context) (client.NodeListResult, error) {
	if p.nodeList != nil {
		return p.nodeList(ctx)
	}
	return p.cli.NodeList(ctx, client.NodeListOptions{})
}

// eventsStream 订阅 daemon 事件流（可注入缝；nil = 真 daemon 直连——
// 真客户端在 Events 内部 goroutine 里跑流，nil cli 的 panic 跨 goroutine，
// recover 护栏接不住，缝是 Watch 降级 hermetic 测试的唯一通路）。
func (p *Provider) eventsStream(ctx context.Context, opts client.EventsListOptions) client.EventsResult {
	if p.events != nil {
		return p.events(ctx, opts)
	}
	return p.cli.Events(ctx, opts)
}

// mintNodeID 铸造并写回单节点锚定标记；返回铸造的平台 ID（空串 = 他方
// 已完成）。版本竞态最多重试 3 次（每次重新 inspect 取新版本）。
func (p *Provider) mintNodeID(ctx context.Context, node swarm.Node) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if id := node.Spec.Labels[labelNodeID]; id != "" {
			return "", nil // 他方已完成
		}
		if p.mintNodePlatformID == nil {
			return "", fmt.Errorf("node id minter not wired (assembly must inject SetNodeIDMinter)")
		}
		minted, err := p.mintNodePlatformID(ctx)
		if err != nil {
			return "", err
		}
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
		_, err = p.cli.NodeUpdate(ctx, node.ID, client.NodeUpdateOptions{
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
