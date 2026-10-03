package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/route"
)

// managedStep 是受管 Provider reconciler 的收敛步（ADR-0004：全平台唯一
// 一份通用 reconciler——Provider 声明部署形态，reconciler 经 Runtime
// Ensure 下发，与用户 Workload 同一条通道）：
//
//  1. Ensure 受管 Workload（Managed 声明；MaterialsSource 子面的材料随
//     Ensure 注入（zot config/htpasswd，F1.11）；活跃 Project 网络合并仅
//     Edge（跨网触达后端）——zot 靠发布端口可达，不挂项目网；受管域
//     Generation 进程内单调——重启重新 Ensure 幂等收敛）；
//  2. Route 发布：routes 表全量 → Runtime.Addresses 解析后端 → Edge.
//     PublishRoutes（强制全量；解析不到的 Route 跳过并记日志——存量路由
//     继续服务的降级语义）。
func (e *Engine) managedStep(ctx context.Context) {
	// 全步带界（staging 实证：无界的 docker API hang 卡死单写者循环）。
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
	defer cancel()
	e.reconcileNodes(stepCtx) // 节点对账不依赖 Edge（观测面独立收敛）
	if e.edge == nil {
		return // Edge 未装配（可选项）：无受管面
	}
	e.reconcileManaged(stepCtx)
	e.publishRoutes(stepCtx)
}

// managedGenState 是受管域 Generation 的实例态（C5：原包级全局会让多
// Engine 实例（测试/多 daemon）互相污染指纹）。
type managedGenState struct {
	gen atomic.Uint64
	fp  atomic.Value // string：最近一次已分配 gen 的 spec 指纹
}

// managedFingerprint 返回受管 Workload 集的稳定指纹（json.Marshal 对 map
// 键排序，切片序取 ManagedWorkloads 稳定返回序；序列化失败退化为逐次
// 唯一值——保守推进 gen，宁替换不漏变更）。
func managedFingerprint(ws []capability.Workload) string {
	b, err := json.Marshal(ws)
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &ws)
	}
	return string(b)
}

// next 幂等分配：指纹同前且已有 gen → 复用；变化或首次 → +1。
func (m *managedGenState) next(fp string) uint64 {
	if prev, ok := m.fp.Load().(string); ok && prev == fp && m.gen.Load() > 0 {
		return m.gen.Load()
	}
	gen := m.gen.Add(1)
	m.fp.Store(fp)
	return gen
}

// managedDomainKeyPrefix 是受管域在归属/期望缓存中的键前缀
// （fleetly/system/<process>——非 App 行键，App 表解析面据此跳过）。
const managedDomainKeyPrefix = "fleetly/system/"

// managedProviderDecl 是一个受管 Provider 的 reconciler 投影：声明 +
// 是否挂活跃项目网（Edge 要跨网触达后端；zot 只需被发布端口可达，附录
// B.1）。
type managedProviderDecl struct {
	m             capability.Managed
	attachNetwork bool
}

// managedProviders 列出在册受管 Provider（注册序稳定：Edge 先于 Registry
// ——Route 面优先收敛）。
func (e *Engine) managedProviders() []managedProviderDecl {
	var out []managedProviderDecl
	if e.edge != nil {
		if m, ok := e.edge.(capability.Managed); ok {
			out = append(out, managedProviderDecl{m: m, attachNetwork: true})
		} else {
			e.log.Warn("edge provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if e.registry != nil {
		if m, ok := e.registry.(capability.Managed); ok {
			out = append(out, managedProviderDecl{m: m, attachNetwork: false})
		} else {
			e.log.Warn("registry provider is not managed-selfhosted; skipping reconciler")
		}
	}
	return out
}

func (e *Engine) reconcileManaged(ctx context.Context) {
	decls := e.managedProviders()
	if len(decls) == 0 {
		return
	}
	refs := e.activeProjectNetworks(ctx)
	// 逐 Provider 组装下发集（Edge 合并活跃项目网——跨网触达后端；zot
	// 不挂——发布端口可达，附录 B.1）并预计算签名（C16：Ensure 全部输入
	// 的指纹——下发集含网引用集 + 材料；任一输入变化即短路失效，无需
	// 额外 Kick 通道）。
	ensured := make([][]capability.Workload, len(decls))
	sigs := make([]string, len(decls))
	var all []capability.Workload
	for i, decl := range decls {
		ws := decl.m.ManagedWorkloads()
		if decl.attachNetwork {
			for j := range ws {
				if len(refs) > 0 {
					ws[j].NetworkRefs = append(ws[j].NetworkRefs, refs...)
				}
			}
		}
		ensured[i] = ws
		all = append(all, ws...)
		materials := capability.Materials{}
		if src, ok := decl.m.(capability.MaterialsSource); ok {
			materials = src.ManagedMaterials()
		}
		sigs[i] = managedFingerprint(ws) + "\x00" + materialsFingerprint(materials)
	}
	// 指纹覆盖全部受管域的完整下发集（Generation=已下发 Spec 的单调编号，
	// CONTEXT.md——网引用集变化也推进 gen，一次性收敛不逐 tick 滚动）。
	gen := e.managedGen.next(managedFingerprint(all))
	now := e.clock.Now()
	for i, decl := range decls {
		ws := ensured[i]
		ns := decl.m.ManagedNamespace()
		// 签名短路（C16）：上次成功 Ensure 的完整签名未变且未到强制重放
		// 节拍 → 跳过本拍 Ensure（材料解析+下发全套）。环外变更（人工改
		// 载体、载体漂移）由强制重放节拍兜底（Options.ReconcileReplayInterval）。
		if _, fresh := e.ensureFresh(e.managedEnsure, ns.String(), sigs[i], now); fresh {
			continue
		}
		materials := capability.Materials{}
		if src, ok := decl.m.(capability.MaterialsSource); ok {
			materials = src.ManagedMaterials()
		}
		if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(gen), materials); err != nil {
			e.log.Error("managed reconciler: ensure", "namespace", ns.String(), "err", err)
			e.ensureForget(e.managedEnsure, ns.String()) // 失败清签名：下拍重试
			continue                                     // 单 Provider 失败不阻断其余受管面收敛
		}
		e.ensureRemember(e.managedEnsure, ns.String(), ensureMemo{sig: sigs[i], gen: gen, at: now})
		for _, w := range ws {
			e.obsMu.Lock()
			e.workloadApp[w.ID] = managedDomainKeyPrefix + w.Process // 归属登记（观测/drift 面）
			e.ensuredGen[w.ID] = gen
			e.obsMu.Unlock()
		}
		// 稳态看门狗登记（N0.1 P2-10）：受管域 expected 也落在期望缓存——
		// 受管载体挂掉要报 workload.stopped（受管面是平台自身可用性，失明
		// 不可接受）。键与归属登记同形（fleetly/system/<process>）。
		e.expectMu.Lock()
		for _, w := range ws {
			e.expected[managedDomainKeyPrefix+w.Process] = gen
		}
		e.expectMu.Unlock()
	}
}

// materialsFingerprint 返回材料的稳定指纹（json.Marshal 对 map 键排序；
// []byte 走 base64——确定性序列化。序列化失败退化为逐次唯一值——保守
// 推进，宁重下发不漏变更，managedFingerprint 同款取舍）。
func materialsFingerprint(m capability.Materials) string {
	b, err := json.Marshal(struct {
		RegistryAuth map[string]capability.RegistryCredential `json:"registry_auth,omitempty"`
		SecretFiles  map[string][]byte                        `json:"secret_files,omitempty"`
	}{m.RegistryAuth, m.SecretFiles})
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &m)
	}
	return string(b)
}

// publishRoutes 全量发布 Route（后端地址经 Runtime.Addresses 解析；解析
// 不到的跳过——诚实降级，不阻断其余 Route）。
//
// 签名短路（N1 C16b）：行集指纹未变且未到强制重放节拍 → 跳过解析与发布
// （每秒全量 Routes.List + per-Route Addresses + Publish 的稳态面收敛为
// 一次轻量指纹比对）。后端地址不是行集的一部分——载体漂移/Dead backend
// 撤流由 PublishRoutesNow 即时触发与强制重放节拍兜底（滞后有界，终态同）。
func (e *Engine) publishRoutes(ctx context.Context) {
	routes, err := e.routes.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("route publish: list", "err", err)
		e.forgetPublishedRoutes() // 指纹失真：下拍强制全量
		return
	}
	now := e.clock.Now()
	sig := routesFingerprint(routes)
	forced := e.routesPubNow.CompareAndSwap(true, false) // API 写路径即时触发绕过短路
	e.ensureMu.Lock()
	skip := !forced && sig == e.routesPubSig && now.Before(e.routesPubAt.Add(e.opts.ReconcileReplayInterval))
	e.ensureMu.Unlock()
	if skip {
		return
	}
	publish := make([]capability.Route, 0, len(routes))
	for _, rt := range routes {
		cr := capability.Route{
			Host: rt.Host, Path: rt.Path,
			Process: rt.Process, Port: rt.Port,
			Protocol: rt.Protocol, TLS: rt.TLSMode,
		}
		cr.Target, cr.BackendAddr, err = e.resolveBackend(ctx, rt)
		if err != nil {
			e.log.Warn("route publish: backend unresolved, skipping route",
				"route", rt.ID, "host", rt.Host, "err", err)
			continue
		}
		publish = append(publish, cr)
	}
	if err := e.edge.PublishRoutes(ctx, publish); err != nil {
		e.log.Error("route publish: edge rejected config", "err", err)
		e.forgetPublishedRoutes() // 发布失败：下拍强制重试全量
		return
	}
	e.ensureMu.Lock()
	e.routesPubSig, e.routesPubAt = sig, now
	e.ensureMu.Unlock()
}

// forgetPublishedRoutes 作废 Route 发布签名（列表/发布失败与 API 触发面
// 消费：下一拍无条件全量重发布）。
func (e *Engine) forgetPublishedRoutes() {
	e.ensureMu.Lock()
	e.routesPubSig = ""
	e.ensureMu.Unlock()
}

// routesFingerprint 返回活跃 Route 行集的轻量聚合指纹（行内容 +
// updated_at；List 返回 id 升序——序列化序稳定。C16b：routes 表无版本列，
// 行集内容指纹是变更号的最小等价物）。
func routesFingerprint(rows []route.Route) string {
	type fpRow struct {
		ID        string `json:"id"`
		Host      string `json:"host"`
		Path      string `json:"path"`
		AppID     string `json:"app"`
		Process   string `json:"process"`
		Port      int32  `json:"port"`
		Protocol  string `json:"protocol"`
		TLSMode   string `json:"tls"`
		UpdatedAt string `json:"updated_at"`
	}
	out := make([]fpRow, len(rows))
	for i := range rows {
		out[i] = fpRow{
			ID: rows[i].ID, Host: rows[i].Host, Path: rows[i].Path,
			AppID: rows[i].AppID, Process: rows[i].Process, Port: rows[i].Port,
			Protocol: string(rows[i].Protocol), TLSMode: rows[i].TLSMode,
			UpdatedAt: rows[i].UpdatedAt,
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &rows) // 保守推进：宁重发布不漏变更
	}
	return string(b)
}

// resolveBackend 解析 Route 后端地址（Runtime.Addresses 按 process+port
// 匹配；Team 轴从 Project 行实取，ADR-0028）。
func (e *Engine) resolveBackend(ctx context.Context, rt route.Route) (capability.NamespaceRef, string, error) {
	team, err := e.projectTeam(ctx, rt.ProjectID)
	if err != nil {
		return capability.NamespaceRef{}, "", err
	}
	ns := capability.NamespaceRef{Team: team, Project: rt.ProjectID, App: rt.AppID}
	eps, err := e.runtime.Addresses(ctx, ns)
	if err != nil {
		return ns, "", fmt.Errorf("addresses %s: %w", ns, err)
	}
	for _, ep := range eps {
		if ep.Process == rt.Process && ep.Port == rt.Port && ep.Addr != "" {
			return ns, fmt.Sprintf("%s:%d", ep.Addr, ep.Port), nil
		}
	}
	return ns, "", fmt.Errorf("no endpoint for process %q port %d", rt.Process, rt.Port)
}

// activeProjectNetworks 返回全部活跃 Project 网络的引用列表（受管 Edge
// 挂全部项目网以达后端；N0 修复批 B1 实装）。返回跨域引用形态——载体名
// 是 Provider 私有公式，engine 不拼接。Team 轴从 Project 行实取（ADR-0028；
// 批量读 Project 行，避免 per-network 点查）。
func (e *Engine) activeProjectNetworks(ctx context.Context) []capability.NetworkRef {
	rows, err := e.networks.List(ctx, e.db.Runner())
	if err != nil {
		// 读面失败按"无网络"处理：受管 Ensure 照常（不挂新网），下一拍
		// 重试——挂网是增量收敛，不是阻断条件。
		e.log.Error("managed reconciler: list project networks", "err", err)
		return nil
	}
	projects, err := e.projects.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("managed reconciler: list projects", "err", err)
		return nil
	}
	teams := make(map[string]string, len(projects))
	for i := range projects {
		teams[projects[i].ID] = projects[i].TeamID
	}
	refs := make([]capability.NetworkRef, 0, len(rows))
	for _, n := range rows {
		// 已删 Project 的残留网络行无团队可解析：跳过（挂靠以 Project
		// 存活为前提——材料不随 Project 删除级联是 ADR 口径，受管挂网面
		// 只对活跃 Project 负责）。
		team, ok := teams[n.ProjectID]
		if !ok {
			continue
		}
		refs = append(refs, capability.NetworkRef{
			Namespace: capability.NamespaceRef{Team: team, Project: n.ProjectID},
			Name:      n.Name,
		})
	}
	return refs
}

// PublishRoutesNow 触发一次即时 Route 发布（API 写路径在 Route 变更后
// Kick；测试直调）。置即时信号消费于下一拍——短路对本次发布失效（C16b
// 的强制绕过通道），随后恢复签名节律。
func (e *Engine) PublishRoutesNow() {
	e.routesPubNow.Store(true)
	e.managedLoop.Kick()
}
