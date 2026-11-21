package engine

// functionRoutes.go 是 Function 公网合成路由的发布面合并段（设计 v3 §6：
// public = 合成受管路由，无 Route 行——路由存在性纯派生自函数行〔不变量
// I-2〕，结构性无孤儿）。与 console/api 双路由同族的"非 route 行合并"形态，
// 差异两点：① 合并集从函数行动态派生（exposure/host/换轨变更即改合并集），
// 函数公网集指纹必须并入发布签名（漏并 = 短路吞掉 exposure 变更，§6
// mandatory①）；② 后端解析经 Function ns + 函数期望集（workload ID =
// 函数行 id——函数收敛环 recordOwners 落账，owner.function 域）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// functionCapabilityRoutes 返回全部 active ∧ exposure=public 的合成路由
// （Host = public_host、Process 恒 "handler"、Port/Protocol 取冻结 spec、
// TLS = auto；后端经 Runtime.Addresses 解析，解析不到的跳过——诚实降级
// 与 Route 行同口径）。
func (e *Engine) functionCapabilityRoutes(ctx context.Context) []capability.Route {
	rows, err := e.repos.Functions.List(ctx)
	if err != nil {
		// 读失败若静默回空集，发布签名会把空集当稳态记住（下拍同值零重发）
		//——与 Routes.List 同口径：清签名强制下拍全量，本拍不合并函数面。
		e.log.Error("route publish: list functions", "err", err)
		e.ensureForget(e.proxyMemo.pub, routesPubKey)
		return nil
	}
	out := make([]capability.Route, 0, 4)
	for i := range rows {
		row := rows[i]
		if row.Exposure != "public" || row.PublicHost == "" || row.CurrentRevisionID == "" {
			continue
		}
		rev, rerr := e.repos.FunctionRevisions.Get(ctx, row.CurrentRevisionID)
		if rerr != nil {
			continue // 悬空锚（终局窗竞态）跳过；指纹含行本拍不卡短路
		}
		port, proto, perr := functionEndpoint(rev.Spec)
		if perr != nil {
			e.log.Warn("route publish: function endpoint unusable", "function", row.ID, "err", perr)
			continue
		}
		cr := capability.Route{
			Host: row.PublicHost, Process: "handler", Port: port,
			Protocol: proto, TLS: functionRouteTLS(row.PublicHost),
		}
		if row.Replicas == 0 {
			// parked 分支（P2-1a，设计 v2 §1.5）：后端指唤醒组件（系统域
			// 单载体、挂全部项目网）——合成路由保持在场（parked 不撤流），
			// 请求经唤醒组件扣留并唤醒函数。
			addr, aerr := e.activatorBackend(ctx)
			if aerr != nil {
				e.log.Warn("route publish: parked function has no activator endpoint, skipping",
					"function", row.ID, "err", aerr)
				continue
			}
			cr.BackendAddr = addr
			out = append(out, cr)
			continue
		}
		cr.Target, cr.BackendAddr, err = e.resolveFunctionBackend(ctx, &row, port)
		if err != nil {
			e.log.Warn("route publish: function backend unresolved, skipping",
				"function", row.ID, "host", row.PublicHost, "err", err)
			continue
		}
		out = append(out, cr)
	}
	return out
}

const (
	// activatorPort 是唤醒组件的请求面监听口（投影声明口——Workload
	// Ports 与 cmd/activator ACTIVATOR_LISTEN 缺省同源）。
	activatorPort = int32(8090)
)

// functionRouteTLS 是合成路由的 TLS 模式（capability Route 词汇：auto =
// ACME；none = 明文，仅 sslip.io 调试域——路由面既定惯例，quickstart
// --tls none 同源）。sslip 调试域走 none：无 DNS/80-443 的环境（e2e dind、
// 内网 staging）ACME 不可达，auto 会把路由只挂 https 入口造成明文探针 404。
func functionRouteTLS(host string) string {
	if strings.HasSuffix(host, ".sslip.io") {
		return "none"
	}
	return "auto"
}

// ns2team 是 parked 分支的 team 解析（项目行链——与 resolveFunctionBackend
// activatorBackend 解析唤醒组件在系统域的端点（P2-1a 部署切片重做：
// manageds-decl 路线——单载体 system ns、attachNetwork 挂全部活跃项目网；
// 期望集 = 合成形态，workload ID "activator" 与 manageds recordOwners 的
// systemOwner 键一致，Addresses 按 ID 匹配）。
func (e *Engine) activatorBackend(ctx context.Context) (string, error) {
	ns := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "activator"}
	expected := []capability.Workload{{ID: "activator", Process: "activator", Ports: []capability.WorkloadPort{{Port: activatorPort}}}}
	eps, err := e.runtime.Addresses(ctx, ns, expected)
	if err != nil {
		return "", fmt.Errorf("activator addresses: %w", err)
	}
	for _, ep := range eps {
		if ep.Addr != "" {
			return fmt.Sprintf("%s:%d", ep.Addr, activatorPort), nil
		}
	}
	return "", fmt.Errorf("no activator endpoint")
}

// resolveFunctionBackend 解析函数后端地址（ns = Function 域；期望集 =
// 函数行 id 的 ensuredSpec—— Addresses 按 workload ID 匹配期望集）。
func (e *Engine) resolveFunctionBackend(ctx context.Context, row *function.Function, port int32) (capability.NamespaceRef, string, error) {
	team, err := e.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return capability.NamespaceRef{}, "", err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, Function: row.ID, FunctionName: row.Name}
	eps, err := e.runtime.Addresses(ctx, ns, e.obs.functionExpectations(row.ID))
	if err != nil {
		return ns, "", fmt.Errorf("addresses %s: %w", ns, err)
	}
	for _, ep := range eps {
		if ep.Port == port && ep.Addr != "" {
			return ns, fmt.Sprintf("%s:%d", ep.Addr, ep.Port), nil
		}
	}
	return ns, "", fmt.Errorf("no endpoint for handler port %d", port)
}

// functionEndpoint 从冻结体读声明口与协议（恰一 http|h2c 口——受理位已
// 归一；UNSPECIFIED→http、tcp 防御跳过）。
func functionEndpoint(specBody []byte) (int32, capability.Protocol, error) {
	s := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(specBody, s); err != nil {
		return 0, "", err
	}
	ports := s.GetHandler().GetPorts()
	if len(ports) != 1 {
		return 0, "", fmt.Errorf("function spec has %d ports (want 1)", len(ports))
	}
	switch ports[0].GetProtocol() {
	case specv1.Protocol_PROTOCOL_H2C:
		return ports[0].GetPort(), capability.ProtocolH2C, nil
	case specv1.Protocol_PROTOCOL_TCP:
		return 0, "", fmt.Errorf("function port protocol tcp is not routable")
	default: // UNSPECIFIED 归一 http（受理位同判据）
		return ports[0].GetPort(), capability.ProtocolHTTP, nil
	}
}

// functionsPublicFingerprint 是函数公网集指纹（§6 mandatory①：并入发布
// 签名——exposure/host/在服换轨任一变更即失配短路）。
func (e *Engine) functionsPublicFingerprint(ctx context.Context) string {
	rows, err := e.repos.Functions.List(ctx)
	if err != nil {
		// 读失败回逐次唯一值：保守推进（宁重发布不漏变更）。
		return fmt.Sprintf("functions-list-error-%v", err)
	}
	// Replicas 必须入指纹（P2-0 修复，Phase 2 设计 v2 §1.6 咬出）：缩零/
	// 唤醒（replicas 0↔rung）改合成路由合并集（parked 期 BackendAddr 指
	// 唤醒组件——P2-1），漏并 = 发布签名短路吞掉该变更（§6 mandatory①
	// 同型漏洞）。
	type fpRow struct {
		ID        string `json:"id"`
		Host      string `json:"host"`
		Current   string `json:"current"`
		Exposure  string `json:"exposure"`
		Replicas  int64  `json:"replicas"`
		UpdatedAt string `json:"updated_at"`
	}
	out := make([]fpRow, 0, len(rows))
	for i := range rows {
		if rows[i].Exposure != "public" {
			continue
		}
		out = append(out, fpRow{
			ID: rows[i].ID, Host: rows[i].PublicHost, Current: rows[i].CurrentRevisionID,
			Exposure: rows[i].Exposure, Replicas: rows[i].Replicas, UpdatedAt: rows[i].UpdatedAt,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &rows) // 保守推进
	}
	return string(b)
}
