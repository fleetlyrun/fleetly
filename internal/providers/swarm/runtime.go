package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
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
//
// 稳态短路两道闸（受管域每拍重放，写面必须收敛到零）：
//   - no-op 断路器（内存账本，lastIssued）：期望 canonical 与最近一次确认
//     服务端持有的完全一致即跳过 update 判定链——daemon 版本漂移新增物化
//     字段时（inspect 回读多出平台不管理的字段）serviceSpecEqual 恒不等，
//     无此闸会每拍重发 update 自激复燃（update 产事件 → Kick → 再
//     update）。已知边界：平台侧手工改动载体在期望不变时不再自愈（spec
//     对照 drift 经 InspectWorkloads 观测面仍可见，ADR-0022）。
//   - serviceSpecEqual（inspect 回读比对）：修复前的既有闸，兜住账本作废
//     （失败重置）后的第一拍。
func (p *Provider) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (err error) {
	// 期望集载体名 + 碰撞前置拒绝（N1 收尾批 B9）：不同 Workload 折叠成同
	// 载体名（进程名仅差特殊字符经 sanitize 同形，如 web.1 / web-1）会让
	// 后者静默覆盖前者——一进程无声丢失。spec 侧字符集白名单是主防线，此
	// 处对存量 Revision 与绕过路径 fail-closed；纯检先于任何材料/服务副
	// 作用（同 Generation 重放安全不变：同 Workload 重复出现不算碰撞）。
	desired := make(map[string]string, len(ws))
	for _, w := range ws {
		name := workloadServiceName(ns, w)
		if prev, ok := desired[name]; ok && prev != w.ID {
			return fmt.Errorf("swarm ensure %s: workloads %s and %s both resolve to service name %q (carrier name collision; process names must be distinct DNS labels)", ns, prev, w.ID, name)
		}
		desired[name] = w.ID
	}

	// 断路器账本整拍作废（C19-1）：Ensure 失败 = 拍内收敛未完成，本拍期
	// 望集的落账条目可信度作废，下拍保守重发（重发先过 serviceSpecEqual
	// 门，代价一次 inspect 读，无风暴面）。
	defer func() {
		if err != nil {
			p.forgetLastIssued(sortedKeys(desired)...)
		}
	}()

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

	// 网络目标解析的 per-Ensure 备忘（C19-3）：拍内多 Workload 引用同名
	// 网络只探一次 daemon；Ensure 序结束随局部变量废弃。
	resolvedNets := map[string]netResolve{}
	for _, w := range ws {
		spec := toServiceSpec(ns, w, gen, secretCarriers)
		// 网络引用名→ID 先行解析（服务端对名字输入会改写为 ID——发送
		// ID 使回读形态与发送形态一致，no-op 比对的前提）。
		spec, nerr := p.resolveNetworkTargets(ctx, spec, resolvedNets)
		if nerr != nil {
			return fmt.Errorf("swarm ensure %s: %w", ns, nerr)
		}

		auth, err := p.registryAuthFor(ctx, w.Image, m)
		if err != nil {
			return fmt.Errorf("swarm ensure %s: %w", ns, err)
		}

		// 期望 spec canonical 只算一次（C19-4）：断路器比对、等价判定、
		// 落账三处共用（此前等价判定内部双算 desired 侧）。
		desiredJSON := canonicalSpecJSON(spec)

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
			p.recordLastIssued(spec.Name, desiredJSON) // create 即入账：服务端已持有该 spec
			continue
		}
		svc := inspect.Service
		if desiredJSON != "" && desiredJSON == p.lastIssuedOf(spec.Name) {
			// no-op 断路器（C19-1）：期望与最近一次确认下发一致 → update
			// 判定链整体短路。服务级 inspect 已完成，服务意外缺失仍会在
			// 下一拍由 404 分支重建（账本只在写确认时落，不豁免存在性检查）。
			continue
		}
		if serviceSpecEqualJSON(desiredJSON, svc.Spec) {
			// 语义等价即跳过 update——受管域收敛环每拍重放 Ensure，
			// 无条件 update 会让 identical spec 也滚替任务（update 产
			// 事件 → Kick → 再 update 的自激环；staging 真机实证：
			// 受管库载体 20s 内 49 次版本推进、postgres 反复优雅退出
			// 永不停稳，2026-10-02）。
			p.recordLastIssued(spec.Name, desiredJSON) // 服务端现态即期望：回填账本（作废后重建）
			continue
		}
		if uerr := p.updateServiceCAS(ctx, ns, spec, svc, auth); uerr != nil {
			return uerr // update 失败：defer 整拍作废账本，下拍重发（C19-1）
		}
		p.recordLastIssued(spec.Name, desiredJSON)
	}

	// 域内收敛：期望集之外的 fleetly 管辖服务移除。
	for _, svc := range existing {
		if _, ok := desired[svc.Spec.Name]; !ok {
			if _, err := p.cli.ServiceRemove(ctx, svc.ID, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
				return fmt.Errorf("swarm ensure %s: remove stale %s: %w", ns, svc.Spec.Name, err)
			}
			p.forgetLastIssued(svc.Spec.Name) // 服务删除即清账（不复活、不泄漏）
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

// serviceSpecEqualJSON 判定期望 spec（canonical JSON，调用方单算传入，
// C19-4：与断路器/落账共用同一份）与现存服务 spec 语义等价：JSON 投影
// canonical 化（map 键排序 + 服务端归一化产物剥离）后逐字节比对。等价 =
// update 是 no-op，调用方据此跳过。比对失败（desiredJSON 为空 = marshal
// 异常）一律视为不等（保守：多一次 update 无害，漏 update 才有害）。
func serviceSpecEqualJSON(desiredJSON string, current swarm.ServiceSpec) bool {
	return desiredJSON != "" && desiredJSON == canonicalSpecJSON(current)
}

// no-op 断路器账本（C19-1）：服务名 → 最近一次确认服务端持有的期望
// canonical spec JSON。账本写入只发生在服务端确认持有该 spec 的时刻
// （create 成功 / update 成功 / inspect 等价回读），清账三时点：update 或
// Ensure 失败（整拍作废，下拍保守重发）、服务删除（域内收敛与 Remove）。
// 键是全局服务名（命名公式内嵌 ns 四元组，跨 ns 天然不撞）；账本单进程
// 内存态，重启即冷（首轮由 serviceSpecEqual 门兜住，无正确性依赖）。
// Ensure 可来自不同部署单写者环并发执行，账本互斥保护。

// recordLastIssued 落账。
func (p *Provider) recordLastIssued(name, canonical string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	if p.lastIssued == nil {
		p.lastIssued = map[string]string{}
	}
	p.lastIssued[name] = canonical
}

// lastIssuedOf 读账。
func (p *Provider) lastIssuedOf(name string) string {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	return p.lastIssued[name]
}

// forgetLastIssued 批量清账。
func (p *Provider) forgetLastIssued(names ...string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	for _, n := range names {
		delete(p.lastIssued, n)
	}
}

// canonicalSpecJSON 把 spec 归一为可比较的 JSON 字节串。
//
// 服务端归一化产物（staging 真机 inspect 对照实录，2026-10-02）在两侧一致剥离——
// 只比较平台受管字段：
//   - TaskTemplate.ContainerSpec.DNSConfig（空对象服务端物化）
//   - TaskTemplate.ContainerSpec.Isolation（"default" 缺省）
//   - TaskTemplate.Resources（零值对象服务端物化）
//   - TaskTemplate.RestartPolicy 的 Delay/MaxAttempts/Window（服务端缺省
//     节律；平台只管 Condition）
//   - TaskTemplate.Runtime（"container" 缺省）
//   - TaskTemplate.ForceUpdate（服务端回滚自增）
//   - RollbackConfig 整体（服务端缺省物化，平台不管理）
//   - UpdateConfig 的 Monitor/MaxFailureRatio（服务端缺省，平台不管理）
//
// Networks[].Target 的名字→ID 服务端解析在 Ensure 侧先行解析（发 ID 而非
// 名，服务端不再改写）。
func canonicalSpecJSON(spec swarm.ServiceSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	tt, _ := m["TaskTemplate"].(map[string]any)
	if tt != nil {
		tt["ForceUpdate"] = 0
		if rt, ok := tt["Runtime"].(string); ok && (rt == "" || rt == "container") {
			delete(tt, "Runtime")
		}
		if rp, ok := tt["RestartPolicy"].(map[string]any); ok {
			delete(rp, "Delay")
			delete(rp, "MaxAttempts")
			delete(rp, "Window")
		}
		if cs, ok := tt["ContainerSpec"].(map[string]any); ok {
			if d, ok := cs["DNSConfig"].(map[string]any); ok && len(d) == 0 {
				delete(cs, "DNSConfig")
			}
			if iso, ok := cs["Isolation"].(string); ok && (iso == "" || iso == "default") {
				delete(cs, "Isolation")
			}
		}
		if res, ok := tt["Resources"].(map[string]any); ok && len(res) == 0 {
			delete(tt, "Resources")
		}
	}
	delete(m, "RollbackConfig")
	if uc, ok := m["UpdateConfig"].(map[string]any); ok {
		delete(uc, "Monitor")
		delete(uc, "MaxFailureRatio")
	}
	// EndpointSpec.Mode：服务端对空物化为 vip（防御性双侧剥离——endpointSpec
	// 已显式 vip，此处兜住历史行与未来方言差）。
	if es, ok := m["EndpointSpec"].(map[string]any); ok {
		if mode, _ := es["Mode"].(string); mode == "" || mode == "vip" {
			delete(es, "Mode")
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

// netResolve 是网络引用的 per-Ensure 解析备忘值（C19-3）。
type netResolve struct {
	id      string // 解析到的 swarm 网络 ID
	missing bool   // 404 哨兵：载体不存在，名字原样保留
}

// resolveNetworkTargets 把 TaskTemplate.Networks[].Target 从平台网络载体
// 名解析为 swarm 网络 ID——服务端对 create/update 的名字输入会改写为 ID，
// 先行解析使发送形态与回读形态一致（no-op 比对的前提）。
//
// resolved 是 per-Ensure 备忘（键=载体名）：同拍内多 Workload 引用同名网
// 络只探一次 daemon，Ensure 序结束随局部变量废弃。解析分诊（Q-20，
// network.go ensureNetworks 同款先例）：NotFound → 载体缺失，名字原样保
// 留交给服务端名字级错误如实暴露（ensureNetworks 已 create-or-get，404 =
// 引用与载体脱节的 bug 形态）；其余错误上抛带原因——inspect 失败 ≠ 不存
// 在，吞掉会把权限/连接类故障伪装成"无需解析"，推迟到服务端误导性报错
// （修复前任何错误都 continue 吞掉）。
func (p *Provider) resolveNetworkTargets(ctx context.Context, spec swarm.ServiceSpec, resolved map[string]netResolve) (swarm.ServiceSpec, error) {
	nets := spec.TaskTemplate.Networks
	for i := range nets {
		name := nets[i].Target
		res, ok := resolved[name]
		if !ok {
			n, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
			switch {
			case err == nil:
				res = netResolve{id: n.Network.ID}
			case isNotFound(err):
				res = netResolve{missing: true}
			default:
				return swarm.ServiceSpec{}, fmt.Errorf("resolve network %s: %w", name, err)
			}
			resolved[name] = res
		}
		if res.missing {
			continue // 保留名字原样：服务端给出名字级错误（404 语义不变）
		}
		nets[i].Target = res.id
	}
	return spec, nil
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
		p.forgetLastIssued(svc.Spec.Name) // 服务删除即清账（断路器账本不复活）
	}
	return nil
}

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
// 单服务任务列表失败不中断整轮（C19-2 哨兵）：记 Warn（带服务名）继续其
// 余服务——修复前 ServiceInspect 失败 continue 但不入 memo，同服务的每个
// 任务都重复撞一次失败调用（N+1 放大），且失败静默无诊断线索。
func (p *Provider) pollTasks(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	services, err := p.cli.ServiceList(ctx, client.ServiceListOptions{
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return fmt.Errorf("managed service list: %w", err)
	}
	for i := range services.Items {
		svc := services.Items[i]
		tasks, err := p.cli.TaskList(ctx, client.TaskListOptions{
			Filters: client.Filters{}.Add("service", svc.ID),
		})
		if err != nil {
			// 哨兵：单服务失败只降级该服务的本轮观测，其余服务照常。
			slog.Warn("swarm watch: task poll skipped a service until next tick", "service", svc.Spec.Name, "error", err)
			continue
		}
		for _, t := range tasks.Items {
			ev, ok := taskEvent(t, taskLabels(t, svc.Spec.Labels))
			if !ok {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- ev:
			}
		}
	}
	return nil
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
// 缝契约：覆盖 Watch 初始锚定降级的 hermetic 测试面）。
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
