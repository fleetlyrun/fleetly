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

func (e *Engine) reconcileManaged(ctx context.Context) {
	m, ok := e.edge.(capability.Managed)
	if !ok {
		e.log.Warn("edge provider is not managed-selfhosted; skipping reconciler")
		return
	}
	ws := m.ManagedWorkloads()
	refs := e.activeProjectNetworks(ctx)
	for i := range ws {
		if len(refs) > 0 {
			ws[i].NetworkRefs = append(ws[i].NetworkRefs, refs...)
		}
	}
	gen := e.managedGen.next(managedFingerprint(ws))
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

// activeProjectNetworks 返回全部活跃 Project 网络的引用列表（受管 Edge
// 挂全部项目网以达后端；N0 修复批 B1 实装）。返回跨域引用形态——载体名
// 是 Provider 私有公式，engine 不拼接。
func (e *Engine) activeProjectNetworks(ctx context.Context) []capability.NetworkRef {
	rows, err := e.networks.List(ctx, e.db.Runner())
	if err != nil {
		// 读面失败按"无网络"处理：受管 Ensure 照常（不挂新网），下一拍
		// 重试——挂网是增量收敛，不是阻断条件。
		e.log.Error("managed reconciler: list project networks", "err", err)
		return nil
	}
	refs := make([]capability.NetworkRef, 0, len(rows))
	for _, n := range rows {
		refs = append(refs, capability.NetworkRef{
			Namespace: capability.NamespaceRef{Team: "default", Project: n.ProjectID},
			Name:      n.Name,
		})
	}
	return refs
}

// PublishRoutesNow 触发一次即时 Route 发布（API 写路径在 Route 变更后
// Kick；测试直调）。
func (e *Engine) PublishRoutesNow() { e.managedLoop.Kick() }
