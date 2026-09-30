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
//  1. Ensure 受管 Workload（Managed 声明 + 活跃 Project 网络合并；受管域
//     Generation 进程内单调——重启重新 Ensure 幂等收敛）；
//  2. Route 发布：routes 表全量 → Runtime.Addresses 解析后端 → Edge.
//     PublishRoutes（强制全量；解析不到的 Route 跳过并记日志——存量路由
//     继续服务的降级语义）。
func (e *Engine) managedStep(ctx context.Context) {
	e.reconcileNodes(ctx) // 节点对账不依赖 Edge（观测面独立收敛）
	if e.edge == nil {
		return // Edge 未装配（可选项）：无受管面
	}
	e.reconcileManaged(ctx)
	e.publishRoutes(ctx)
}

// 受管域 Generation 分配（进程内）：**指纹未变则 gen 不推进**。staging 真机
// 教训（2026-09-30）：gen 进 ContainerSpec label，逐 tick +1 会让 swarm 每
// tick 滚动替换 task——受管 traefik 繁殖到 115 实例把 4GB 主机内存打穿。
// 语义修正：gen 是"已下发 Spec 的编号"，spec 未变复用当前 gen（同 gen 同
// spec 的 Ensure 对载体是 no-op）；spec 变化（traefik 参数/网络挂靠变化）
// 才 +1。重启后进程内态清零，首次重新分配：载体 gen 相同则完全 no-op，
// 更高则一次性 update 收敛（重启维护窗语义，与用户域重放一致）。
var (
	managedGen atomic.Uint64
	managedFP  atomic.Value // string：最近一次已分配 gen 的 spec 指纹
)

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

// nextManagedGen 幂等分配：指纹同前且已有 gen → 复用；变化或首次 → +1。
func nextManagedGen(fp string) uint64 {
	if prev, ok := managedFP.Load().(string); ok && prev == fp && managedGen.Load() > 0 {
		return managedGen.Load()
	}
	gen := managedGen.Add(1)
	managedFP.Store(fp)
	return gen
}

func (e *Engine) reconcileManaged(ctx context.Context) {
	m, ok := e.edge.(capability.Managed)
	if !ok {
		e.log.Warn("edge provider is not managed-selfhosted; skipping reconciler")
		return
	}
	ws := m.ManagedWorkloads()
	nets := e.activeProjectNetworks(ctx)
	for i := range ws {
		if len(nets) > 0 {
			ws[i].Networks = append(ws[i].Networks, nets...)
		}
	}
	gen := nextManagedGen(managedFingerprint(ws))
	ns := m.ManagedNamespace()
	if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(gen), capability.Materials{}); err != nil {
		e.log.Error("managed reconciler: ensure", "namespace", ns.String(), "err", err)
		return
	}
	for _, w := range ws {
		e.obsMu.Lock()
		e.workloadApp[w.ID] = "fleetly/system/" + w.Process // 归属登记（观测/drift 面）
		e.ensuredGen[w.ID] = gen
		e.obsMu.Unlock()
	}
}

// publishRoutes 全量发布 Route（后端地址经 Runtime.Addresses 解析；解析
// 不到的跳过——诚实降级，不阻断其余 Route）。
func (e *Engine) publishRoutes(ctx context.Context) {
	routes, err := e.routes.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("route publish: list", "err", err)
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
	}
}

// resolveBackend 解析 Route 后端地址（Runtime.Addresses 按 process+port
// 匹配；Project 团队轴当前单团队默认）。
func (e *Engine) resolveBackend(ctx context.Context, rt route.Route) (capability.NamespaceRef, string, error) {
	ns := capability.NamespaceRef{Team: "default", Project: rt.ProjectID, App: rt.AppID}
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

// activeProjectNetworks 返回活跃 Project 网络名列表（受管 Edge 挂全部
// 项目网以达后端；C5 overlay 批次落网络实体后实装，当前返回空——单
// swarm 网络形态由 e2e 验证）。
func (e *Engine) activeProjectNetworks(context.Context) []string {
	return nil
}

// PublishRoutesNow 触发一次即时 Route 发布（API 写路径在 Route 变更后
// Kick；测试直调）。
func (e *Engine) PublishRoutesNow() { e.managedLoop.Kick() }
