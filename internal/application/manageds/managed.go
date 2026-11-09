// Package manageds 是受管域卫星（ADR-0004：全平台唯一一份通用 reconciler
// ——Provider 声明部署形态，reconciler 经 Runtime Ensure 下发，与用户
// Workload 同一条通道）。受管 Provider 注册序：Proxy → Registry → Logging
// → Metrics（Route 面优先收敛，观测面殿后）。
//
// P3 批 5 收编：managedDomain → manageds（受管域实体名词的机械复数，
// ADR-0075 命名法则）；P4 wave-2a 嵌入改形（接收者下域 *manageds）→
// P5 wave-4 拆包（ADR-0088 决策 2 P5 形态，2026-11-01）：嵌入已解开——
// 卫星独立成包（internal/application/manageds），核经构造注入持有
// （construct.go 装配卫星 New）；环表 step 经卫星方法值（e.manageds.Step）、
// networkrebuild 的受管网集判定经卫星 ManagedProviders（消费点零改）；
// 机制契约共享面（memo 协议/指纹公式）在 internal/application/engine/contract。
// 观测窄接口窄化为 system 轴签名——workloadOwner 不全域下沉的 P5 wave-4
// 重裁（systemOwner 铸造与域判别留核绑定闭包，browses 会话轴同款）。
// gensFor/KickManagedLoop 留核（P4 wave-2a 裁定：ensureMu 锁与 gens map
// 的配对，managedGen 注；Loop 挂载核持有）。禁回引 *engine.Engine
// （enginebackref 跨包执法）。
package manageds

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Manageds 是受管域卫星实例态（Generation 实例态（C5：多 Engine 实例互不
// 污染）+ Ensure 备忘）。P4 wave-2a 接收者下域（域内短名 m，b/d/m 三星
// 同批裁定）→ P5 wave-4 拆包导出（manageds → Manageds，平级包通则①）；
// 依赖显式逐字段 + 机制端口注入（construct.go 装配）。memo 表 ensure 随域
// 持有（map 本体在组合根铸造注入——metastatewrite D3）。禁挂 *Engine 回引
// （enginebackref 守卫执法）。
type Manageds struct {
	ensure map[string]contract.Memo // namespace → 上次成功 Ensure（managedFingerprint 全量保守口径）

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：proxy/registry/
	// logging 是受管 Provider 三面（capability 副本双持裁定——P5 过渡
	// 形态，通则二：三面的核消费占优（routepublish 路由发布真源/
	// componentrestart providers 面/builder 构建日志回读），随域迁入会
	// 强制核改写为访问器——metrics 卫星的"capability 随域"是类型撞名的
	// 强制先例，本域无撞名，按消费面裁定副本）；runtime 是观测播种
	//（Inspector）与 Ensure 消费面（核副本双持）；log/clock 域内日志与
	// 节拍。
	proxy    capability.Proxy
	registry capability.Registry
	logging  capability.Logging
	runtime  capability.Runtime
	log      *slog.Logger
	clock    state.Clock

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（受管 Ensure 引用全部活跃
	// 项目网络，与网络重建串行化，ADR-0046；端口名对齐锁入口方法名集
	// ——maintenanceguard 判据继续命中）；boundedStep 是步带界端口
	//（ManagedStepTimeout 事实面）；ensureFresh/ensureRemember/ensureForget
	// 是 memo 端口（签名单源 engine/contract——ensureMu 单锁留核、memo map 随域，
	// ADR-0088 决策 4）；activeProjectNetworks/activeProjectIDs/
	// controlPlaneNode 是 facts 端口（Proxy 挂网/材料/钉住三面的事实输入）；
	// gensFor 是 Generation 计数器的惰性建入口（留核 *Engine——ensureMu 锁
	// 与 gens map 的配对裁定，P4 wave-2a 裁定维持：map 本体随核，卫星经
	// 端口读写）；reconcileNodes/publishRoutes 是设计稿 §4 例外边的端口
	// 归位（节点对账与路由发布真源留核，Step 是唯一编排消费面）；
	// metricsProvider 是 metrics 卫星 MetricsProvider 访问器的闭包消费
	//（窄事实端口，通则五）；recordOwners/recordExpected 是观测窄接口的
	// system 轴窄化签名（P4 wave-2a 设计点 + P5 wave-4 拆包窄化：
	// systemOwner 铸造与域判别留核绑定闭包——受管域归属 = w.Process 的
	// 固定映射，卫星无需传轴）。
	lockMaintenance       func() func()
	boundedStep           func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh           contract.EnsureFresh
	ensureRemember        contract.EnsureRemember
	ensureForget          contract.EnsureForget
	activeProjectNetworks func(ctx context.Context) ([]capability.NetworkRef, error)
	activeProjectIDs      func(ctx context.Context) ([]string, error)
	controlPlaneNode      func(ctx context.Context) (string, error)
	gensFor               func(nsKey string) *ManagedGen
	reconcileNodes        func(ctx context.Context)
	publishRoutes         func(ctx context.Context)
	metricsProvider       func() capability.Metrics
	recordOwners          func(gen uint64, ws []capability.Workload)
	recordExpected        func(process string, gen uint64)
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值/闭包；EnsureMemo 是随域 memo 表
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
	GensFor               func(nsKey string) *ManagedGen
	ReconcileNodes        func(ctx context.Context)
	PublishRoutes         func(ctx context.Context)
	MetricsProvider       func() capability.Metrics
	// RecordOwners/RecordExpected 是观测窄接口的 system 域窄化端口（核
	// 绑定闭包内铸造 systemOwner(w.Process)——域判别留核）。
	RecordOwners   func(gen uint64, ws []capability.Workload)
	RecordExpected func(process string, gen uint64)

	// EnsureMemo 是 per-namespace Ensure 备忘表本体（组合根 make 后注入）。
	EnsureMemo map[string]contract.Memo
}

// New 构造受管域卫星（不启动——环步由 engine 核驱动：环表 step 经卫星
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
		ensure:                deps.EnsureMemo,
	}
	return m
}

// ManagedGen 是受管域 Generation 的实例态（C5：原包级全局会让多 Engine
// 实例（测试/多 daemon）互相污染指纹）。P3 批 5 收编：managedGenState →
// managedGen（剥 State 后缀，ADR-0075 命名法则）；P5 wave-4 拆包导出
// （gensFor 留核持 *ManagedGen map——类型跨包可见是留核裁定的机械面，
// 方法零 I/O 仍域内私有）。字段零值可用（gensFor 造零值计数器）。
type ManagedGen struct {
	gen atomic.Uint64
	fp  atomic.Value // string：最近一次已分配 gen 的 spec 指纹
	// adopted 标记首见指纹已沿用（seedFromRuntime 播种后的第一拍不 +1——
	// 播种值本身来自载体现行 spec，是"已下发"事实而非新变更）。
	adopted atomic.Bool
	// seeded 标记本进程已做过载体观测播种（一次性）。P1-5 根修
	//（2026-10-05 评审，staging 记录·五 #2）：观测【失败】不再置位——
	// 冷启 gen=1 与载体持久的 fleetly.generation 标签差值就是假滚动
	//（I/O 风暴窗重启五受管域连滚、traefik 路由中断级）；失败 = 跳过
	// 本域本拍 Ensure/gen 推进，下拍重试播种。子面缺席（无从观测）仍
	// 冷启 gen=1 兜底（旧行为）。
	seeded atomic.Bool
	// obsSkipLogged 是观测失败跳拍告警的去抖位：连续失败沿只告警一次，
	// 观测门恢复即清（告警状态机"沿才通知"同款取向——风暴窗不逐拍刷屏）。
	obsSkipLogged atomic.Bool
}

// next 幂等分配：指纹同前且已有 gen → 复用；首见 → 沿用现值（seedFromRuntime
// 播种的载体现行 gen，或未播种时 0→1）；变化 → +1（重启续接语义，见
// seedFromRuntime 注释）。
func (m *ManagedGen) next(fp string) uint64 {
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
func (m *ManagedGen) seedFromRuntime(gen uint64) {
	if gen > m.gen.Load() {
		m.gen.Store(gen)
	}
}

// Step 是受管 Provider reconciler 的收敛步（ADR-0004：全平台唯一一份通用
// reconciler——Provider 声明部署形态，reconciler 经 Runtime Ensure 下发，
// 与用户 Workload 同一条通道）：
//
//  1. Ensure 受管 Workload（Managed 声明；MaterialsSource 子面的材料随
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
// 导出供核环表经方法值挂载（construct.go 的 e.rings step = e.manageds.Step，
// logpipes/metrics/backups/browses 同款）。
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
// 材料源子面（FacesOf 探测产物）+ 是否挂活跃项目网（Proxy 要跨网触达
// 后端；zot 只需被发布端口可达，附录 B.1）+ per-Project 材料面（zot
// htpasswd/config 随活跃 Project 集再生成，ADR-0036 N2 兑现节 2；nil =
// 无集依赖，走 MaterialsSource）。P5 wave-4 拆包导出（核 networkrebuild
// 的受管网集判定消费 Managed() 窄读面）。
type ManagedProviderDecl struct {
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
// Logging → Metrics——Route 面优先收敛，观测面殿后）。受管/材料源子面经
// FacesOf 协商点探测。P5 wave-4 拆包导出（原 managedProviders——核
// networkrebuild 的受管网集判定经卫星指针，消费点零改）。
func (m *Manageds) ManagedProviders() []ManagedProviderDecl {
	var out []ManagedProviderDecl
	if m.proxy != nil {
		if faces := capability.FacesOf(m.proxy); faces.Managed != nil {
			out = append(out, ManagedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: true})
		} else {
			m.log.Warn("proxy provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if m.registry != nil {
		faces := capability.FacesOf(m.registry)
		if faces.Managed != nil {
			out = append(out, ManagedProviderDecl{
				m:                faces.Managed,
				materials:        faces.MaterialsSource,
				projectMaterials: faces.ProjectScopedMaterials,
				attachNetwork:    false,
			})
		} else {
			m.log.Warn("registry provider is not managed-selfhosted; skipping reconciler")
		}
	}
	if m.logging != nil {
		faces := capability.FacesOf(m.logging)
		if faces.Managed != nil {
			// 不挂项目网（发布端口可达——zot 同款分叉，附录 B.1；采集与
			// 检索都经 mesh 端点，ADR-0040）。
			out = append(out, ManagedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else {
			m.log.Warn("logging provider is not managed-selfhosted; skipping reconciler")
		}
	}
	// P4 wave-1b：Metrics capability 持有权随 metrics 卫星（卫星 struct
	// 类型名与 capability 字段同名，裸字段提升不可用）——本域消费经
	// metricsProvider 窄事实端口闭包（wave-2a，访问器即 metrics 卫星的
	// 提升方法）。
	if p := m.metricsProvider(); p != nil {
		faces := capability.FacesOf(p)
		if faces.Managed != nil {
			// 不挂项目网（VM mesh 端点 + cadvisor host 端点都经节点地址
			// 可达，ADR-0041）。
			out = append(out, ManagedProviderDecl{m: faces.Managed, materials: faces.MaterialsSource, attachNetwork: false})
		} else {
			m.log.Warn("metrics provider is not managed-selfhosted; skipping reconciler")
		}
	}
	return out
}

func (m *Manageds) reconcileManaged(ctx context.Context) {
	decls := m.ManagedProviders()
	if len(decls) == 0 {
		return
	}
	// 活跃 Project 网引用（Proxy 挂网面）。P1-5 根修：list 失败不再折成
	// "无网络"喂给 spec——那会把 Proxy 的全部项目网引用从下发集挖掉，
	// 指纹一变一还 = traefik 摘/挂全网两轮假滚（staging 记录·五 #2 的
	// "list 失败被当作 spec 失配"分支）；上抛，挂网域按跳拍收口（见
	// 循环内 netObsErr 分支）。
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
		if !gst.seeded.Load() {
			// 载体现行 Generation 播种（重启续接）：Inspector 是可选子面
			//（FacesOf 协商）。观测【错误】= 跳过本域本拍（P1-5 根修，
			// 2026-10-05 评审批 + staging 记录·五 #2 裁决："观测错误不得
			// 触发 spec 对照判 drift"）：观测失败不是"载体不存在"的证据，
			// 置位走冷启 gen=1 会与载体持久标签形成差值 = 假滚动——下拍
			// 重试播种。子面缺席（nil，无从观测）仍冷启 gen=1 兜底（旧行为）。
			if insp := capability.FacesOf(m.runtime).Inspector; insp != nil {
				obs, oerr := insp.InspectWorkloads(ctx, ns)
				if oerr != nil {
					m.managedObsSkip(gst, ns, "inspect workloads", oerr)
					continue
				}
				for _, o := range obs {
					gst.seedFromRuntime(uint64(o.Generation))
				}
			}
			gst.seeded.Store(true)
		}
		// 挂网域的网/项目 list 失败 → 同款跳拍（P1-5）：refs 不可信时
		// Ensure 会下发挖掉全部项目网引用的 spec。非挂网域（zot/VL/VM）
		// 不消费 refs，照常收敛。
		if netObsErr != nil && decl.attachNetwork {
			m.managedObsSkip(gst, ns, "list project networks", netObsErr)
			continue
		}
		gst.obsSkipLogged.Store(false) // 观测门全过：清告警去抖位（再失败可再告）
		gen := gst.next(sigs[i])
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
		// wave-2a 经观测窄接口端口，卫星不直写 expect 内部；P5 wave-4
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
func (m *Manageds) managedObsSkip(gst *ManagedGen, ns capability.NamespaceRef, what string, err error) {
	if gst.obsSkipLogged.CompareAndSwap(false, true) {
		m.log.Warn("managed reconciler: observation failed; skipping domain this tick (no drift verdict)",
			"namespace", ns.String(), "observation", what, "err", err)
	}
}

// managedMaterials 解析一个受管 Provider 的本拍材料：per-Project 面在场
// 走活跃集再生成（zot htpasswd/config，ADR-0036 N2 兑现节 2），否则走
// MaterialsSource 子面（现状）。projectIDs 只在 projectMaterials 非 nil
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
