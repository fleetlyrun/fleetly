// Package manageds 是受管域应用模块（ADR-0004：全平台唯一一份通用 reconciler
// ——Provider 声明部署形态，reconciler 经 Runtime Ensure 下发，与用户
// Workload 同一条通道）。受管 Provider 注册序：Proxy → Registry → Logging
// → Metrics（Route 面优先收敛，观测面殿后）。
//
// P3 批 5 收编：managedDomain → manageds（受管域实体名词的机械复数，
// ADR-0075 命名法则）；P4 wave-2a 嵌入改形（接收者下域 *manageds）→
// P5 wave-4 拆包（ADR-0088 决策 2 P5 形态，2026-11-01）：嵌入已解开——
// 应用模块独立成包（internal/application/manageds），核经构造注入持有
// （engine 去应用模块 import 第三批，2026-11-10：核只持 contract.Managed
// 接口，具体装配器经 engine.Deps.ManagedFactory 由组合根注入）；循环表 step
// 经 contract.StepOf(e.manageds, "managed")、networkrebuild 的受管网集判定
// 经 ManagedProviders；内核契约共享面（ensure cache 协议/指纹公式/
// ManagedGen）在 internal/application/engine/contract。观测窄接口窄化为
// system 轴签名——workloadOwner 不全域下沉的 P5 wave-4 重裁（systemOwner
// 铸造与域判别留核绑定闭包，browses 会话轴同款）。gensFor/KickManagedLoop
// 留核（P4 wave-2a 裁定：ensureMu 锁与 gens map 的配对；Loop 挂载核持有）。
// 禁回引 *engine.Engine（enginebackref 跨包执法）。
//
// 健康面：Health 把受管 Provider 的载体存活观测投影为 "managed <kind>"
// 行（观测窄接口 ObservationOf 单源；未观测/启动窗不出行），engine.
// HealthComponents 并入 GetStatus components——受管 Proxy 宕机即 DEGRADED。
// Provider.Health() 只报 Provider 自身面（配置就绪/端点可达），不重复
// 声明载体健康。
package manageds

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Manageds 是受管域应用模块实例态（Generation 实例态（C5：多 Engine 实例互不
// 污染）+ Ensure 确保缓存）。P4 wave-2a 接收者下域（域内短名 m，b/d/m 三个应用模块
// 同批裁定）→ P5 wave-4 拆包导出（manageds → Manageds，平级包通则①）；
// 依赖显式逐字段 + 内核端口注入（construct.go 装配）。ensure cache 表 ensure 随域
// 持有（map 本体在组合根铸造注入——metastatewrite D3）。禁挂 *Engine 回引
// （enginebackref 契约测试执法）。
type Manageds struct {
	ensure map[string]contract.Memo // namespace → 上次成功 Ensure（managedFingerprint 全量保守口径）

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：proxy/registry/
	// logging 是受管 Provider 三面（capability 副本双持裁定——P5 过渡
	// 形态，通则二：三面的核消费占优（routepublish 路由发布真源/
	// componentrestart providers 面/builder 构建日志回读），随域迁入会
	// 强制核改写为访问器——metrics 应用模块的"capability 随域"是类型撞名的
	// 强制先例，本域无撞名，按消费面裁定副本）；runtime 是观测播种
	//（Inspector）与 Ensure 消费面（核副本双持）；log/clock 域内日志与
	// 节拍。
	proxy    capability.Proxy
	registry capability.Registry
	logging  capability.Logging
	runtime  capability.Runtime
	log      *slog.Logger
	clock    state.Clock

	// 内核端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（受管 Ensure 引用全部活跃
	// 项目网络，与网络重建串行化，ADR-0046；端口名对齐锁入口方法名集
	// ——maintenanceguard 判据继续命中）；boundedStep 是步带界端口
	//（ManagedStepTimeout 事实面）；ensureFresh/ensureRemember/ensureForget
	// 是 ensure cache 端口（签名单源 engine/contract——ensureMu 单锁留核、ensure cache map 随域，
	// ADR-0088 决策 4）；activeProjectNetworks/activeProjectIDs/
	// controlPlaneNode 是 resolver 端口（Proxy 挂网/材料/钉住三面的事实输入）；
	// gensFor 是 Generation 计数器的惰性建入口（留核 *Engine——ensureMu 锁
	// 与 gens map 的配对裁定，P4 wave-2a 裁定维持：map 本体随核，应用模块经
	// 端口读写）；reconcileNodes/publishRoutes 是设计稿 §4 例外边的端口
	// 归位（节点对账与路由发布真源留核，Step 是唯一编排消费面）；
	// metricsProvider 是 metrics 应用模块 MetricsProvider 访问器的闭包消费
	//（窄事实端口，通则五）；recordOwners/recordExpected 是观测窄接口的
	// system 轴窄化签名（P4 wave-2a 设计点 + P5 wave-4 拆包窄化：
	// systemOwner 铸造与域判别留核绑定闭包——受管域归属 = w.Process 的
	// 固定映射，应用模块无需传轴）。
	lockMaintenance       func() func()
	boundedStep           func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh           contract.EnsureFresh
	ensureRemember        contract.EnsureRemember
	ensureForget          contract.EnsureForget
	activeProjectNetworks func(ctx context.Context) ([]capability.NetworkRef, error)
	activeProjectIDs      func(ctx context.Context) ([]string, error)
	controlPlaneNode      func(ctx context.Context) (string, error)
	gensFor               func(nsKey string) *contract.ManagedGen
	reconcileNodes        func(ctx context.Context)
	publishRoutes         func(ctx context.Context)
	metricsProvider       func() capability.Metrics
	recordOwners          func(gen uint64, ws []capability.Workload)
	recordExpected        func(process string, gen uint64)
	// observationOf 是观测窄接口的 managed 轴读面（workloadID → 最新观测）：
	// Health 的载体存活面唯一输入（进程内观测缓存，不触载体）。nil = 无观测
	// 面（手工装配），Health 不出行。
	observationOf func(workloadID string) (capability.WorkloadEvent, bool)
}

// Deps 是应用模块依赖与内核端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有应用模块；依赖显式
// 逐字段注入 + 内核端口绑定为核方法值/闭包；EnsureMemo 是随域 ensure cache 表
// 本体——组合根铸造注入（metastatewrite D3：表只在组合根/meta init 出生））。
type Deps struct {
	Proxy    capability.Proxy
	Registry capability.Registry
	Logging  capability.Logging
	Runtime  capability.Runtime
	Log      *slog.Logger
	Clock    state.Clock

	LockMaintenance       func() func()
	BoundedStep           func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh           contract.EnsureFresh
	EnsureRemember        contract.EnsureRemember
	EnsureForget          contract.EnsureForget
	ActiveProjectNetworks func(ctx context.Context) ([]capability.NetworkRef, error)
	ActiveProjectIDs      func(ctx context.Context) ([]string, error)
	ControlPlaneNode      func(ctx context.Context) (string, error)
	GensFor               func(nsKey string) *contract.ManagedGen
	ReconcileNodes        func(ctx context.Context)
	PublishRoutes         func(ctx context.Context)
	MetricsProvider       func() capability.Metrics
	// RecordOwners/RecordExpected 是观测窄接口的 system 域窄化端口（核
	// 绑定闭包内铸造 systemOwner(w.Process)——域判别留核）。
	RecordOwners   func(gen uint64, ws []capability.Workload)
	RecordExpected func(process string, gen uint64)
	// ObservationOf 是观测窄接口的 managed 轴读面（workloadID → 最新观测；
	// 受管健康行的载体存活面输入）。组合根绑核 observationOf。
	ObservationOf func(workloadID string) (capability.WorkloadEvent, bool)

	// EnsureMemo 是 per-namespace Ensure 确保缓存表本体（组合根 make 后注入）。
	EnsureMemo map[string]contract.Memo
}

// New 构造受管域应用模块（不启动——循环步由 engine 核驱动：循环表 step 经应用模块
// 方法值 e.manageds.Step）。
func New(deps Deps) *Manageds {
	m := &Manageds{
		proxy:                 deps.Proxy,
		registry:              deps.Registry,
		logging:               deps.Logging,
		runtime:               deps.Runtime,
		log:                   deps.Log,
		clock:                 deps.Clock,
		lockMaintenance:       deps.LockMaintenance,
		boundedStep:           deps.BoundedStep,
		ensureFresh:           deps.EnsureFresh,
		ensureRemember:        deps.EnsureRemember,
		ensureForget:          deps.EnsureForget,
		activeProjectNetworks: deps.ActiveProjectNetworks,
		activeProjectIDs:      deps.ActiveProjectIDs,
		controlPlaneNode:      deps.ControlPlaneNode,
		gensFor:               deps.GensFor,
		reconcileNodes:        deps.ReconcileNodes,
		publishRoutes:         deps.PublishRoutes,
		metricsProvider:       deps.MetricsProvider,
		recordOwners:          deps.RecordOwners,
		recordExpected:        deps.RecordExpected,
		observationOf:         deps.ObservationOf,
		ensure:                deps.EnsureMemo,
	}
	return m
}

// Step 是受管 Provider reconciler 的收敛步（ADR-0004：全平台唯一一份通用
// reconciler——Provider 声明部署形态，reconciler 经 Runtime Ensure 下发，
// 与用户 Workload 同一条通道）：
//
//  1. Ensure 受管 Workload（Managed 声明；MaterialsSource 扩展接口的材料随
//     Ensure 注入（zot config/htpasswd，F1.11）；活跃 Project 网络合并仅
//     Proxy（跨网触达后端）——zot 靠发布端口可达，不挂项目网；受管域
//     Generation 进程内单调——重启重新 Ensure 幂等收敛）。观测前置读
//     （gen 播种 InspectWorkloads / Proxy 挂网 list）失败 = 跳过该域本拍
//     + 告警（P1-5 根修：观测错误不判 drift、不重置 gen——staging
//     记录·五 #2 观测风暴假滚动的事故裁决）；
//  2. Route 发布：routes 表全量 → Runtime.Addresses 解析后端 → Proxy.
//     PublishRoutes（强制全量；解析不到的 Route 跳过并记日志——存量路由
//     继续服务的降级语义）。
//
// 经 contract.Module.Loops 供核循环表挂载（construct.go 的 e.rings step =
// contract.StepOf(e.manageds, "managed")，logpipes/metrics/backups/browses
// 同款）。
func (m *Manageds) Step(ctx context.Context) {
	// 维护互斥读半边（ADR-0046）：受管 Ensure 引用全部活跃项目网络（Proxy
	// 挂网面），网络重建窗内必须排队；锁等待不占步预算（排队语义）。
	// 全步带界（staging 实证：无界的 docker API hang 卡死单写者循环）。
	unlockMaintenance := m.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := m.boundedStep(ctx)
	defer cancel()
	m.reconcileNodes(stepCtx) // 节点对账不依赖 Proxy（观测面独立收敛）
	// 受管面按在册 Provider 各自收敛（Proxy 缺席不阻断 Registry/Logging——
	// ADR-0040 起 Logging 是独立受管面）。
	m.reconcileManaged(stepCtx)
	if m.proxy == nil {
		return // Proxy 未装配（可选项）：无 Route 发布面
	}
	m.publishRoutes(stepCtx)
}

// ManagedProviderDecl 是一个受管 Provider 的 reconciler 投影：声明 +
// 材料源扩展接口（ExtensionsOf 探测产物）+ 是否挂活跃项目网（Proxy 要跨网触达
// 后端；zot 只需被发布端口可达，附录 B.1）+ per-Project 材料面（zot
// htpasswd/config 随活跃 Project 集再生成，ADR-0036 N2 兑现节 2；nil =
// 无集依赖，走 MaterialsSource）。P5 wave-4 拆包导出（核 networkrebuild
// 的受管网集判定消费 Managed() 窄读面）。kind 是该受管面的 Capability 种类，
// 只服务健康行命名（"managed proxy" 等稳定名，Health 消费）。
type ManagedProviderDecl struct {
	kind             capability.Kind
	m                capability.Managed
	materials        capability.MaterialsSource
	projectMaterials capability.ProjectScopedMaterials
	attachNetwork    bool
}

// Managed 返回该 Provider 的受管声明面（核 networkrebuild 的受管域 ns 集
// 判定消费——拆包导出的窄读面，字段本体不外泄）。
func (d ManagedProviderDecl) Managed() capability.Managed { return d.m }

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

// ManagedProviders 列出在册受管 Provider（注册序稳定：Proxy → Registry →
// Logging → Metrics——Route 面优先收敛，观测面殿后）。受管/材料源扩展接口经
// ExtensionsOf 协商点探测。engine 去应用模块 import 第三批（2026-11-10）：
// 核消费面窄化为 contract.ManagedProvider（只暴露 Managed() 读面）。
//
// 非受管 Provider 的跳过告警只走本入口；健康读径（Health）用静默形态——
// GetStatus 的 TTL 探活不应逐轮重复告警。
func (m *Manageds) ManagedProviders() []contract.ManagedProvider {
	decls := m.providerDecls(true)
	out := make([]contract.ManagedProvider, len(decls))
	for i := range decls {
		out[i] = decls[i]
	}
	return out
}

// providerDecls 是受管 Provider 声明的域内实现体（reconcileManaged 与
// ManagedProviders 共用）；warn=false 是健康读径的静默形态（行集与顺序
// 不变）。
func (m *Manageds) providerDecls(warn bool) []ManagedProviderDecl {
	var out []ManagedProviderDecl
	if m.proxy != nil {
		if faces := capability.FacesOf(m.proxy); faces.Managed != nil {
			out = append(out, ManagedProviderDecl{kind: capability.KindProxy, m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: true})
		} else if warn {
			m.log.Warn("proxy provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if m.registry != nil {
		faces := capability.FacesOf(m.registry)
		if faces.Managed != nil {
			out = append(out, ManagedProviderDecl{
				kind:             capability.KindRegistry,
				m:                faces.Managed,
				materials:        faces.MaterialsSource,
				projectMaterials: faces.ProjectScopedMaterials,
				attachNetwork:    false,
			})
		} else if warn {
			m.log.Warn("registry provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if m.logging != nil {
		faces := capability.FacesOf(m.logging)
		if faces.Managed != nil {
			// 不挂项目网（发布端口可达——zot 同款分叉，附录 B.1；采集与
			// 检索都经 mesh 端点，ADR-0040）。
			out = append(out, ManagedProviderDecl{kind: capability.KindLogging, m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else if warn {
			m.log.Warn("logging provider is not managed-selfhosted; skipping reconciler")
		}
	}
	// P4 wave-1b：Metrics capability 持有权随 metrics 应用模块（应用模块 struct
	// 类型名与 capability 字段同名，裸字段提升不可用）——本域消费经
	// metricsProvider 窄事实端口闭包（wave-2a，访问器即 metrics 应用模块的
	// 提升方法）。
	if p := m.metricsProvider(); p != nil {
		faces := capability.FacesOf(p)
		if faces.Managed != nil {
			// 不挂项目网（VM mesh 端点 + cadvisor host 端点都经节点地址
			// 可达，ADR-0041）。
			out = append(out, ManagedProviderDecl{kind: capability.KindMetrics, m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else if warn {
			m.log.Warn("metrics provider is not managed-selfhosted; skipping reconciler")
		}
	}
	return out
}

// managedHealthRowPrefix 是受管健康行名前缀（"managed proxy" / "managed
// registry" / "managed logging" / "managed metrics"；stable contract——
// GetStatus components 与 doctor 按名寻址）。
const managedHealthRowPrefix = "managed "

// Health 返回受管 Provider 的载体存活行（Proxy → Registry → Logging →
// Metrics，与 ManagedProviders 同序；每个在册 Provider 至多一行）。
//
// 观测语义（观测窄接口，进程内缓存；不触载体）：
//   - 逐 Provider 的 ManagedWorkloads，经 ObservationOf 查最新观测；
//   - 任一观测为 stopped/failed/degraded/orphaned/completed → 一行不健康
//     （受管面是长运行形态，completed 同样意味着面不可用；Details 汇总
//     全部失联 Workload，逐条 "workload=<id> state=<state>: <message>"）；
//   - 全部已观测 Workload 为 running → 一行健康；
//   - 未观测（watch 首事件前）或仅 pending（启动窗）→ 不出行：未知不等于
//     不健康，启动窗不制造假降级。
//
// Provider 自身的 Health() 只报 Provider 面（配置就绪/端点可达）；受管载体
// 的存活面单源在此（受管 Proxy 宕机不再出现 "status 全绿"）。
func (m *Manageds) Health() []contract.ManagedHealthRow {
	if m.observationOf == nil {
		return nil
	}
	decls := m.providerDecls(false)
	var out []contract.ManagedHealthRow
	for _, decl := range decls {
		var bad, running []string
		observed := 0
		for _, w := range decl.m.ManagedWorkloads() {
			ev, ok := m.observationOf(w.ID)
			if !ok {
				continue
			}
			observed++
			switch {
			case ev.State == capability.WorkloadRunning:
				running = append(running, "workload="+w.ID+" state=running")
			case unhealthyManagedState(ev.State):
				bad = append(bad, managedWorkloadDetail(w.ID, ev))
			}
		}
		name := managedHealthRowPrefix + string(decl.kind)
		switch {
		case len(bad) > 0:
			out = append(out, contract.ManagedHealthRow{Name: name, Healthy: false, Details: strings.Join(bad, "; ")})
		case observed > 0 && len(running) == observed:
			out = append(out, contract.ManagedHealthRow{Name: name, Healthy: true, Details: strings.Join(running, "; ")})
		}
	}
	return out
}

// unhealthyManagedState 报告观测态是否意味着受管面不可用。受管 Provider 是
// 长运行形态：completed（退出码 0）同样不在服务，按不健康计；pending 是
// 启动窗中间态，不在此列（未知/未就绪不等于不健康）。
func unhealthyManagedState(s capability.WorkloadState) bool {
	switch s {
	case capability.WorkloadStopped, capability.WorkloadDegraded,
		capability.WorkloadFailed, capability.WorkloadOrphaned,
		capability.WorkloadCompleted:
		return true
	}
	return false
}

// managedWorkloadDetail 渲染一条失联 Workload 的健康详情（Message 优先，
// 空则回退编排器侧 Reason 原文）。
func managedWorkloadDetail(id string, ev capability.WorkloadEvent) string {
	detail := "workload=" + id + " state=" + string(ev.State)
	switch {
	case ev.Message != "":
		return detail + ": " + ev.Message
	case ev.Reason != "":
		return detail + ": " + ev.Reason
	}
	return detail
}

func (m *Manageds) reconcileManaged(ctx context.Context) {
	decls := m.providerDecls(true)
	if len(decls) == 0 {
		return
	}
	// 活跃 Project 网引用（Proxy 挂网面）。P1-5 根修：list 失败不再折成
	// "无网络"喂给 spec——那会把 Proxy 的全部项目网引用从下发集挖掉，
	// 指纹一变一还 = traefik 摘/挂全网两轮假滚（staging 记录·五 #2 的
	// "list 失败被当作 spec 失配"分支）；上抛，挂网域按跳拍收口（见
	// 循层内 netObsErr 分支）。
	refs, netObsErr := m.activeProjectNetworks(ctx)
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
		ids, perr := m.activeProjectIDs(ctx)
		if perr != nil {
			m.log.Error("managed reconciler: list projects for registry materials", "err", perr)
			for _, decl := range decls {
				m.ensureForget(m.ensure, decl.m.ManagedNamespace().String())
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
		node, err := m.controlPlaneNode(ctx)
		if err != nil {
			m.log.Error("managed reconciler: control-plane node for volume pinning", "err", err)
			for _, decl := range decls {
				m.ensureForget(m.ensure, decl.m.ManagedNamespace().String())
			}
			return
		}
		pinNode = node
	}
	// 逐 Provider 组装下发集（Proxy 合并活跃项目网——跨网触达后端；zot
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
		materials := m.managedMaterials(decl, projectIDs)
		sigs[i] = contract.ManagedFingerprint(ws) + "\x00" + contract.MaterialsFingerprint(materials)
	}
	// per-受管域 Generation（F2.5 修复）：每域独立计数，从载体观测播种
	//（重启续接——gen 标签持久在 spec 而计数器进程内重置，会把全部受管
	// 域滚一遍；traefik stop-first = 路由中断，CI 升级零扰动锚实证）。
	// 指纹 = 本域 Workload 集 + 材料（sig 同源）——任一输入变化才推进本
	// 域 gen；新增受管 Provider 不再放大成全域滚动。
	now := m.clock.Now()
	for i, decl := range decls {
		ws := ensured[i]
		ns := decl.m.ManagedNamespace()
		gst := m.gensFor(ns.String())
		if !gst.Seeded() {
			// 载体现行 Generation 播种（重启续接）：Inspector 是可选扩展接口
			//（ExtensionsOf 协商）。观测【错误】= 跳过本域本拍（P1-5 根修，
			// 2026-10-05 评审批 + staging 记录·五 #2 裁决："观测错误不得
			// 触发 spec 对照判 drift"）：观测失败不是"载体不存在"的证据，
			// 置位走冷启 gen=1 会与载体持久标签形成差值 = 假滚动——下拍
			// 重试播种。扩展接口缺席（nil，无从观测）仍冷启 gen=1 兜底（旧行为）。
			if insp := capability.FacesOf(m.runtime).Inspector; insp != nil {
				obs, oerr := insp.InspectWorkloads(ctx, ns)
				if oerr != nil {
					m.managedObsSkip(gst, ns, "inspect workloads", oerr)
					continue
				}
				for _, o := range obs {
					gst.SeedFromRuntime(uint64(o.Generation))
				}
			}
			gst.MarkSeeded()
		}
		// 挂网域的网/项目 list 失败 → 同款跳拍（P1-5）：refs 不可信时
		// Ensure 会下发挖掉全部项目网引用的 spec。非挂网域（zot/VL/VM）
		// 不消费 refs，照常收敛。
		if netObsErr != nil && decl.attachNetwork {
			m.managedObsSkip(gst, ns, "list project networks", netObsErr)
			continue
		}
		gst.ResetObsSkipLogged() // 观测门全过：清告警去抖位（再失败可再告）
		gen := gst.Next(sigs[i])
		// 签名短路（C16）：上次成功 Ensure 的完整签名未变且未到强制重放
		// 节拍 → 跳过本拍 Ensure（材料解析+下发全套）。环外变更（人工改
		// 载体、载体漂移）由强制重放节拍兜底（Options.ReconcileReplayInterval）。
		if _, fresh := m.ensureFresh(m.ensure, ns.String(), sigs[i], now); fresh {
			continue
		}
		materials := m.managedMaterials(decl, projectIDs)
		if _, err := m.runtime.Ensure(ctx, ns, ws, capability.Generation(gen), materials); err != nil {
			m.log.Error("managed reconciler: ensure", "namespace", ns.String(), "err", err)
			m.ensureForget(m.ensure, ns.String()) // 失败清签名：下拍重试
			continue                              // 单 Provider 失败不阻断其余受管面收敛
		}
		m.ensureRemember(m.ensure, ns.String(), contract.Memo{Sig: sigs[i], Gen: gen, At: now})
		m.recordOwners(gen, ws)
		// 稳态看门狗登记（N0.1 P2-10）：受管域 expected 也落在期望缓存——
		// 受管载体挂掉要报 workload.stopped（受管面是平台自身可用性，失明
		// 不可接受）。键与归属登记同形（fleetly/system/<process>）。P4
		// wave-2a 经观测窄接口端口，应用模块不直写 expect 内部；P5 wave-4
		// 拆包起窄化为 system 轴签名（systemOwner 铸造留核绑定闭包）。
		for _, w := range ws {
			m.recordExpected(w.Process, gen)
		}
	}
}

// managedObsSkip 记一次观测失败跳拍（P1-5 根修）：观测错误不得触发 spec
// 对照判 drift、不得推进 gen、不得 Ensure——跳过本域本拍，下拍重试。
// warn 带 namespace 与错误；连续失败沿只告警一次（obsSkipLogged 去抖，
// 观测门恢复即清——告警状态机"沿才通知"同款取向，风暴窗不逐拍刷屏）。
func (m *Manageds) managedObsSkip(gst *contract.ManagedGen, ns capability.NamespaceRef, what string, err error) {
	if gst.MarkObsSkipLogged() {
		m.log.Warn("managed reconciler: observation failed; skipping domain this tick (no drift verdict)",
			"namespace", ns.String(), "observation", what, "err", err)
	}
}

// managedMaterials 解析一个受管 Provider 的本拍材料：per-Project 面在场
// 走活跃集再生成（zot htpasswd/config，ADR-0036 N2 兑现节 2），否则走
// MaterialsSource 扩展接口（现状）。projectIDs 只在 projectMaterials 非 nil
// 时被消费（调用方保证读失败时已整组返回）。
func (m *Manageds) managedMaterials(decl ManagedProviderDecl, projectIDs []string) capability.Materials {
	if decl.projectMaterials != nil {
		return decl.projectMaterials.ManagedMaterialsFor(projectIDs)
	}
	if decl.materials != nil {
		return decl.materials.ManagedMaterials()
	}
	return capability.Materials{}
}
