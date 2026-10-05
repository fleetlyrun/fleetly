package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	// 维护互斥读半边（ADR-0046）：受管 Ensure 引用全部活跃项目网络（Edge
	// 挂网面），网络重建窗内必须排队；锁等待不占步预算（排队语义）。
	// 全步带界（staging 实证：无界的 docker API hang 卡死单写者循环）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
	defer cancel()
	e.reconcileNodes(stepCtx) // 节点对账不依赖 Edge（观测面独立收敛）
	// 受管面按在册 Provider 各自收敛（Edge 缺席不阻断 Registry/Logging——
	// ADR-0040 起 Logging 是独立受管面）。
	e.reconcileManaged(stepCtx)
	if e.edge == nil {
		return // Edge 未装配（可选项）：无 Route 发布面
	}
	e.publishRoutes(stepCtx)
}

// managedGenState 是受管域 Generation 的实例态（C5：原包级全局会让多
// Engine 实例（测试/多 daemon）互相污染指纹）。
type managedGenState struct {
	gen atomic.Uint64
	fp  atomic.Value // string：最近一次已分配 gen 的 spec 指纹
	// adopted 标记首见指纹已沿用（seedFromRuntime 播种后的第一拍不 +1——
	// 播种值本身来自载体现行 spec，是"已下发"事实而非新变更）。
	adopted atomic.Bool
	// seeded 标记本进程已做过载体观测播种（一次性；观测失败也置位——
	// 冷启 gen=1 兜底，与旧行为一致）。
	seeded atomic.Bool
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

// next 幂等分配：指纹同前且已有 gen → 复用；首见 → 沿用现值（seedFromRuntime
// 播种的载体现行 gen，或未播种时 0→1）；变化 → +1（重启续接语义，见
// seedFromRuntime 注释）。
func (m *managedGenState) next(fp string) uint64 {
	prev, hasFP := m.fp.Load().(string)
	if hasFP && prev == fp && m.gen.Load() > 0 {
		return m.gen.Load()
	}
	if !hasFP && m.adopted.CompareAndSwap(false, true) {
		// 首见：指纹即载体现行形态（播种来源或进程冷启的第一拍）——沿用
		// 现值，不制造假变更。
		m.fp.Store(fp)
		if m.gen.Load() == 0 {
			return m.gen.Add(1)
		}
		return m.gen.Load()
	}
	m.fp.Store(fp)
	return m.gen.Add(1)
}

// seedFromRuntime 用载体观测的现行 Generation 播种计数器（重启续接）。
// 缺席（首装/观测失败）不播种——next 冷启取 1。
func (m *managedGenState) seedFromRuntime(gen uint64) {
	if gen > m.gen.Load() {
		m.gen.Store(gen)
	}
}

// managedProviderDecl 是一个受管 Provider 的 reconciler 投影：声明 +
// 材料源子面（FacesOf 探测产物）+ 是否挂活跃项目网（Edge 要跨网触达
// 后端；zot 只需被发布端口可达，附录 B.1）+ per-Project 材料面（zot
// htpasswd/config 随活跃 Project 集再生成，ADR-0036 N2 兑现节 2；nil =
// 无集依赖，走 MaterialsSource）。
type managedProviderDecl struct {
	m                capability.Managed
	materials        capability.MaterialsSource
	projectMaterials capability.ProjectScopedMaterials
	attachNetwork    bool
}

// anyManagedVolume 报告下发集内是否有挂卷的受管 Workload（钉住判定）。
func anyManagedVolume(ws []capability.Workload) bool {
	for _, w := range ws {
		if len(w.Volumes) > 0 {
			return true
		}
	}
	return false
}

// applyManagedVolumePinning 把控制面节点锚合并进挂卷 Workload 的
// Placement（幂等去重；空锚 = 无钉住面——卷外 Workload 保持调度自由）。
func applyManagedVolumePinning(ws []capability.Workload, nodeID string) {
	if nodeID == "" {
		return
	}
	for i := range ws {
		if len(ws[i].Volumes) == 0 {
			continue
		}
		seen := false
		for _, id := range ws[i].Placement.NodeIDs {
			if id == nodeID {
				seen = true
			}
		}
		if !seen {
			ws[i].Placement.NodeIDs = append(ws[i].Placement.NodeIDs, nodeID)
		}
	}
}

// managedProviders 列出在册受管 Provider（注册序稳定：Edge → Registry →
// Logging → Metrics——Route 面优先收敛，观测面殿后）。受管/材料源子面经
// FacesOf 协商点探测。
func (e *Engine) managedProviders() []managedProviderDecl {
	var out []managedProviderDecl
	if e.edge != nil {
		if faces := capability.FacesOf(e.edge); faces.Managed != nil {
			out = append(out, managedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: true})
		} else {
			e.log.Warn("edge provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if e.registry != nil {
		faces := capability.FacesOf(e.registry)
		if faces.Managed != nil {
			out = append(out, managedProviderDecl{
				m:                faces.Managed,
				materials:        faces.MaterialsSource,
				projectMaterials: faces.ProjectScopedMaterials,
				attachNetwork:    false,
			})
		} else {
			e.log.Warn("registry provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if e.logging != nil {
		faces := capability.FacesOf(e.logging)
		if faces.Managed != nil {
			// 不挂项目网（发布端口可达——zot 同款分叉，附录 B.1；采集与
			// 检索都经 mesh 端点，ADR-0040）。
			out = append(out, managedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else {
			e.log.Warn("logging provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if e.metrics != nil {
		faces := capability.FacesOf(e.metrics)
		if faces.Managed != nil {
			// 不挂项目网（VM mesh 端点 + cadvisor host 端点都经节点地址
			// 可达，ADR-0041）。
			out = append(out, managedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else {
			e.log.Warn("metrics provider is not managed-selfhosted; skipping reconciler")
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
	// 活跃 Project 集（zot per-Project 材料的输入，ADR-0036 N2 兑现节 2）。
	// 读失败 = 本拍受管面整组不下发（清签名下拍重试，pinNode 失败同款
	// 取向）：空集喂给材料面会把全部项目用户滚出 htpasswd（在场 workload
	// 拉取 401），比晚一拍收敛更糟。
	needProjects := false
	for _, decl := range decls {
		if decl.projectMaterials != nil {
			needProjects = true
			break
		}
	}
	var projectIDs []string
	if needProjects {
		ids, perr := e.activeProjectIDs(ctx)
		if perr != nil {
			e.log.Error("managed reconciler: list projects for registry materials", "err", perr)
			for _, decl := range decls {
				e.ensureForget(e.managed.ensure, decl.m.ManagedNamespace().String())
			}
			return
		}
		projectIDs = ids
	}
	// 受管域 Placement 钉住（F2.3 收口 F1.15 挂账）：带卷的受管 Workload
	// 钉控制面节点——swarm 无卷感知调度，spec 变更滚动替换可把 task 漂到
	// 无卷节点 preparing 打转（受管卷都是控制面节点的本地卷）。锚解析
	// 失败 = 本拍受管面整组不下发（清签名下拍重试）：漏钉住 = 无钉住调度，
	// 比部署失败更糟（applyVolumePinning 的 Q-8 同款取舍）。
	pinNode := ""
	needPin := false
	for _, decl := range decls {
		if anyManagedVolume(decl.m.ManagedWorkloads()) {
			needPin = true
			break
		}
	}
	if needPin {
		node, err := e.controlPlaneNode(ctx)
		if err != nil {
			e.log.Error("managed reconciler: control-plane node for volume pinning", "err", err)
			for _, decl := range decls {
				e.ensureForget(e.managed.ensure, decl.m.ManagedNamespace().String())
			}
			return
		}
		pinNode = node
	}
	// 逐 Provider 组装下发集（Edge 合并活跃项目网——跨网触达后端；zot
	// 不挂——发布端口可达，附录 B.1）并预计算签名（C16：Ensure 全部输入
	// 的指纹——下发集含网引用集 + 材料；任一输入变化即短路失效，无需
	// 额外 Kick 通道）。
	ensured := make([][]capability.Workload, len(decls))
	sigs := make([]string, len(decls))
	for i, decl := range decls {
		ws := decl.m.ManagedWorkloads()
		if decl.attachNetwork {
			for j := range ws {
				if len(refs) > 0 {
					ws[j].NetworkRefs = append(ws[j].NetworkRefs, refs...)
				}
			}
		}
		applyManagedVolumePinning(ws, pinNode)
		ensured[i] = ws
		materials := e.managedMaterials(decl, projectIDs)
		sigs[i] = managedFingerprint(ws) + "\x00" + materialsFingerprint(materials)
	}
	// per-受管域 Generation（F2.5 修复）：每域独立计数，从载体观测播种
	// （重启续接——gen 标签持久在 spec 而计数器进程内重置，会把全部受管
	// 域滚一遍；traefik stop-first = 路由中断，CI 升级零扰动锚实证）。
	// 指纹 = 本域 Workload 集 + 材料（sig 同源）——任一输入变化才推进本
	// 域 gen；新增受管 Provider 不再放大成全域滚动。
	now := e.clock.Now()
	for i, decl := range decls {
		ws := ensured[i]
		ns := decl.m.ManagedNamespace()
		gst := e.gensFor(ns.String())
		if !gst.seeded.Load() {
			// 载体现行 Generation 播种（重启续接）：Inspector 是可选子面
			// （FacesOf 协商），缺席/失败不播种——冷启 gen=1 兜底（旧行为）。
			if insp := capability.FacesOf(e.runtime).Inspector; insp != nil {
				if obs, oerr := insp.InspectWorkloads(ctx, ns); oerr == nil {
					for _, o := range obs {
						gst.seedFromRuntime(uint64(o.Generation))
					}
				}
			}
			gst.seeded.Store(true)
		}
		gen := gst.next(sigs[i])
		// 签名短路（C16）：上次成功 Ensure 的完整签名未变且未到强制重放
		// 节拍 → 跳过本拍 Ensure（材料解析+下发全套）。环外变更（人工改
		// 载体、载体漂移）由强制重放节拍兜底（Options.ReconcileReplayInterval）。
		if _, fresh := e.ensureFresh(e.managed.ensure, ns.String(), sigs[i], now); fresh {
			continue
		}
		materials := e.managedMaterials(decl, projectIDs)
		if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(gen), materials); err != nil {
			e.log.Error("managed reconciler: ensure", "namespace", ns.String(), "err", err)
			e.ensureForget(e.managed.ensure, ns.String()) // 失败清签名：下拍重试
			continue                                      // 单 Provider 失败不阻断其余受管面收敛
		}
		e.ensureRemember(e.managed.ensure, ns.String(), ensureMemo{sig: sigs[i], gen: gen, at: now})
		for _, w := range ws {
			e.obs.mu.Lock()
			e.obs.workloadApp[w.ID] = systemOwner(w.Process) // 归属登记（观测/drift 面）
			e.obs.ensuredGen[w.ID] = gen
			e.obs.mu.Unlock()
		}
		// 稳态看门狗登记（N0.1 P2-10）：受管域 expected 也落在期望缓存——
		// 受管载体挂掉要报 workload.stopped（受管面是平台自身可用性，失明
		// 不可接受）。键与归属登记同形（fleetly/system/<process>）。
		e.expect.mu.Lock()
		for _, w := range ws {
			e.expect.expected[systemOwner(w.Process)] = gen
		}
		e.expect.mu.Unlock()
	}
}

// gensFor 返回（惰性建）该受管域的 Generation 计数器。
func (e *Engine) gensFor(nsKey string) *managedGenState {
	e.ensureMu.Lock()
	defer e.ensureMu.Unlock()
	if e.managed.gens == nil {
		e.managed.gens = map[string]*managedGenState{}
	}
	st, ok := e.managed.gens[nsKey]
	if !ok {
		st = &managedGenState{}
		e.managed.gens[nsKey] = st
	}
	return st
}

// managedMaterials 解析一个受管 Provider 的本拍材料：per-Project 面在场
// 走活跃集再生成（zot htpasswd/config，ADR-0036 N2 兑现节 2），否则走
// MaterialsSource 子面（现状）。projectIDs 只在 projectMaterials 非 nil
// 时被消费（调用方保证读失败时已整组返回）。
func (e *Engine) managedMaterials(decl managedProviderDecl, projectIDs []string) capability.Materials {
	if decl.projectMaterials != nil {
		return decl.projectMaterials.ManagedMaterialsFor(projectIDs)
	}
	if decl.materials != nil {
		return decl.materials.ManagedMaterials()
	}
	return capability.Materials{}
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
		e.ensureForget(e.edgeMemo.pub, routesPubKey) // 指纹失真：下拍强制全量
		return
	}
	now := e.clock.Now()
	sig := routesFingerprint(routes)
	forced := e.edgeMemo.pubNow.CompareAndSwap(true, false) // API 写路径即时触发绕过短路
	_, fresh := e.ensureFresh(e.edgeMemo.pub, routesPubKey, sig, now)
	if fresh && !forced {
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
		e.ensureForget(e.edgeMemo.pub, routesPubKey) // 发布失败：下拍强制重试全量
		return
	}
	e.ensureRemember(e.edgeMemo.pub, routesPubKey, ensureMemo{sig: sig, at: now})
}

// routesPubKey 是 Route 发布单槽的恒定键（发布面无多键维度——行集指纹
// 即全部输入；单槽经通用 ensureMemo 协议承载，2026-10-03 收编）。
const routesPubKey = "routes"

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
// 匹配；期望集 = 本 App 的 Ensure 投影缓存——载体原生不承载声明端口，
// 端点端口由期望集供给，架构评审第二轮候选 7。缓存冷（重启后基线重放
// 未及）时 Provider 侧匹配不中 → 可重试错误，重放完成下一拍即解）。
// Team 轴从 Project 行实取，ADR-0028。
func (e *Engine) resolveBackend(ctx context.Context, rt route.Route) (capability.NamespaceRef, string, error) {
	team, err := e.projectTeam(ctx, rt.ProjectID)
	if err != nil {
		return capability.NamespaceRef{}, "", err
	}
	ns := capability.NamespaceRef{Team: team, Project: rt.ProjectID, App: rt.AppID}
	eps, err := e.runtime.Addresses(ctx, ns, e.appWorkloadExpectations(rt.AppID))
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

// appWorkloadExpectations 返回 App 名下的期望 Workload 集（最近 Ensure
// 投影缓存快照；Addresses 的端口真源）。
func (e *Engine) appWorkloadExpectations(appID string) []capability.Workload {
	e.obs.mu.RLock()
	defer e.obs.mu.RUnlock()
	var out []capability.Workload
	for wid, owner := range e.obs.workloadApp {
		if owner.domain != ownerApp || owner.id != appID {
			continue
		}
		if w, ok := e.obs.ensuredSpec[wid]; ok {
			out = append(out, w)
		}
	}
	return out
}

// activeProjectIDs 返回全部活跃 Project ID（排序稳定——材料铸造字节稳定
// 的输入序契约）。读失败上抛：调用方（reconcileManaged）按"整组不下发"
// 收口，绝不以空集代偿（空集 = 把全部项目用户滚出 htpasswd）。
func (e *Engine) activeProjectIDs(ctx context.Context) ([]string, error) {
	projects, err := e.projects.List(ctx, e.db.Runner())
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(projects))
	for i := range projects {
		ids = append(ids, projects[i].ID)
	}
	sort.Strings(ids)
	return ids, nil
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
	e.edgeMemo.pubNow.Store(true)
	e.managedLoop.Kick()
}

// KickManagedLoop 触发一次即时受管收敛（API 写路径在 Project 创建/删除后
// Kick；测试直调）。zot per-Project 材料随活跃集再生成（ADR-0036 N2 兑现
// 节 2）——Kick 把项目变更的滚动窗从"下一节拍"缩到即时（staging 实录：
// 新项目首构建可先于节拍到达，推送 401 一次失败）。
func (e *Engine) KickManagedLoop() {
	e.managedLoop.Kick()
}
