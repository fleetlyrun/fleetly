package engine

// Route 全量发布面（P3 批 5 收编自 managed.go，F0.15）：受管 Proxy 载体 +
// 活跃 Project 网络全量挂网；发布单槽 proxyMemo，签名短路
// routesFingerprint。受管收敛面见 managed.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
)

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
		e.ensureForget(e.proxyMemo.pub, routesPubKey) // 指纹失真：下拍强制全量
		return
	}
	now := e.clock.Now()
	// 签名必须覆盖 browse 会话集（ADR-0051 决策 5）：ephemeral 路由不经
	// route 行，会话增删不改行集指纹——不并入则短路吞掉会话路由变更。
	sig := routesFingerprint(routes) + "\x00" + e.browseRoutesFingerprint()
	forced := e.proxyMemo.pubNow.CompareAndSwap(true, false) // API 写路径即时触发绕过短路
	_, fresh := e.ensureFresh(e.proxyMemo.pub, routesPubKey, sig, now)
	if fresh && !forced {
		return
	}
	publish := make([]capability.Route, 0, len(routes))
	// 在服代推导（ADR-0048 决策 1.2 切换步的服务面）：一次 ListDriving
	// 全量推导（observing = 已切换本代；其余在途 = 基线代）——resolveBackend
	// 据此圈定期望集的服务代（蓝绿双代窗消歧；rolling 无在途不过滤）。
	serving := e.servingGenerations(ctx)
	for _, rt := range routes {
		cr := capability.Route{
			Host: rt.Host, Path: rt.Path,
			Process: rt.Process, Port: rt.Port,
			Protocol: rt.Protocol, TLS: rt.TLSMode,
		}
		cr.Target, cr.BackendAddr, err = e.resolveBackend(ctx, rt, serving[rt.AppID])
		if err != nil {
			e.log.Warn("route publish: backend unresolved, skipping route",
				"route", rt.ID, "host", rt.Host, "err", err)
			continue
		}
		publish = append(publish, cr)
	}
	// Browse 会话的 ephemeral 双路由合并（ADR-0051 决策 5：entry 兑换入口
	// + ForwardAuth 门禁的工具路由；非 route 行——会话拓扑是平台内置）。
	publish = append(publish, e.browseCapabilityRoutes(ctx)...)
	// Console 公网发布双路由合并（ADR-0061 决策 2：Host-only 主路由 +
	// 登录限速路由；非 route 行——面板发布是平台内置拓扑，进程生命周期内
	// 恒定，签名不必覆盖）。
	publish = append(publish, e.consoleCapabilityRoutes()...)
	if err := e.proxy.PublishRoutes(ctx, publish); err != nil {
		e.log.Error("route publish: proxy rejected config", "err", err)
		e.ensureForget(e.proxyMemo.pub, routesPubKey) // 发布失败：下拍强制重试全量
		return
	}
	e.ensureRemember(e.proxyMemo.pub, routesPubKey, ensureMemo{sig: sig, at: now})
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

// resolveBackend 解析 Route 后端地址（Runtime.Addresses 按 workload ID
// 匹配期望集；期望集 = 本 App 的 Ensure 投影缓存快照——载体原生不承载
// 声明端口，端点端口由期望集供给，架构评审第二轮候选 7。缓存冷（重启后
// 基线重放未及）时 Provider 侧匹配不中 → 可重试错误，重放完成下一拍即
// 解）。servingGen 是蓝绿代次解析的入参（observ 域 routeExpectations
// 显式接口，ADR-0048 决策 4：0 = 无在途不过滤）。Team 轴从 Project 行
// 实取，ADR-0028。
func (e *Engine) resolveBackend(ctx context.Context, rt route.Route, servingGen uint64) (capability.NamespaceRef, string, error) {
	team, err := e.projectTeam(ctx, rt.ProjectID)
	if err != nil {
		return capability.NamespaceRef{}, "", err
	}
	ns := capability.NamespaceRef{Team: team, Project: rt.ProjectID, App: rt.AppID}
	eps, err := e.runtime.Addresses(ctx, ns, e.obs.routeExpectations(rt.AppID, servingGen))
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

// activeProjectNetworks 返回全部活跃 Project 网络的引用列表（受管 Proxy
// 挂全部项目网以达后端；N0 修复批 B1 实装）。返回跨域引用形态——载体名
// 是 Provider 私有公式，engine 不拼接。Team 轴从 Project 行实取（ADR-0028；
// 批量读 Project 行，避免 per-network 点查）。读失败上抛（P1-5 根修：
// 折成"无网络"会让 Proxy 下发挖掉全部项目网引用的 spec——指纹一变一还
// 两轮假滚；调用方对挂网域跳拍），绝不以空集代偿。
func (e *Engine) activeProjectNetworks(ctx context.Context) ([]capability.NetworkRef, error) {
	rows, err := e.networks.List(ctx, e.db.Runner())
	if err != nil {
		return nil, err
	}
	projects, err := e.projects.List(ctx, e.db.Runner())
	if err != nil {
		return nil, err
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
	return refs, nil
}

// PublishRoutesNow 触发一次即时 Route 发布（API 写路径在 Route 变更后
// Kick；测试直调）。置即时信号消费于下一拍——短路对本次发布失效（C16b
// 的强制绕过通道），随后恢复签名节律。
func (e *Engine) PublishRoutesNow() {
	e.proxyMemo.pubNow.Store(true)
	e.managedLoop.Kick()
}
