// Package routesgrpc 实现 fleetly.proxy.v1.RoutesService：Route CRUD、
// 行级授权、父资源存活守卫与 Proxy 全量发布唤醒。
package routesgrpc

import (
	"context"

	"google.golang.org/grpc"

	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// System 是 Proxy 发布唤醒的窄消费接口（engine 门面实现）。
type System interface {
	PublishRoutesNow()
}

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeProjectID(ctx context.Context, projectID string) error
	AuthorizeRouteID(ctx context.Context, routeID string) error
}

// Deps 是路由服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Routes   route.Repo
	Projects project.Repo
	System   System
	Auth     Authorizer
	Commit   func(ctx context.Context, fact grpcapi.WriteFact) error
}

// Service 满足 proxyv1.RoutesServiceServer。
type Service struct {
	proxyv1.UnimplementedRoutesServiceServer
	deps Deps
}

// New 构造路由服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册路由服务。
func Register(srv *grpc.Server, deps Deps) {
	proxyv1.RegisterRoutesServiceServer(srv, New(deps))
}

var _ proxyv1.RoutesServiceServer = (*Service)(nil)

// ---- Routes（Proxy & TLS 上下文） ----

func (svc *Service) CreateRoute(ctx context.Context, req *proxyv1.CreateRouteRequest) (*proxyv1.CreateRouteResponse, error) {
	// 五要素组合必填已退役 descriptor 注解（P4-1 批 1 Run B2）：拆 per-field
	// required（原 "project_id, host, app_id, process and port: must not
	// be empty" → 各字段独立报缺，文案契约变化在案），由 protovalidate
	// 拦截器在受理面拦截。host/path 语义校验是白名单动态面，保留手写。
	// 受理面校验（安全批 P0）：host/path 原样内插进 traefik 规则的反引号
	// 定界符内（Host(`%s`)），反引号等元字符可注入/劫持路由规则；白名单
	// 与 Proxy Provider 纵深面共用同一真源（capability.ValidateRouteHost）。
	// host 统一小写（R07-M2）：traefik 按小写请求 host 匹配，大写规则值
	// 永不命中——受理面即归一，落库/返回都是小写形态。
	host := capability.NormalizeRouteHost(req.GetHost())
	if err := capability.ValidateRouteHost(host); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	if err := capability.ValidateRoutePath(req.GetPath()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%v", err)
	}
	protocol := capability.Protocol(req.GetProtocol())
	if protocol == "" {
		protocol = capability.ProtocolHTTP
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	tlsMode := req.GetTlsMode()
	if tlsMode == "" {
		tlsMode = "auto"
	}
	row := &route.Route{
		ProjectID: req.GetProjectId(), Host: host, Path: req.GetPath(),
		AppID: req.GetAppId(), Process: req.GetProcess(), Port: req.GetPort(),
		Protocol: protocol, TLSMode: tlsMode,
	}
	// 父资源存活校验（批 0 复核，同族面）：路由挂在不存在/已删的
	// Project 或 App 下此前直接成功（routes 无 FK）——活路由指向已删
	// App 会让 Proxy 全量发布把流量钉在 tombstone 上。
	err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			parentProjectAlive(req.GetProjectId()),
			projectAppAlive(req.GetProjectId(), req.GetAppId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Routes.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：Route CRUD 未入事件流（路由发布事实
			// 由 Proxy 全量发布面承载；A 化前亦无事件行，行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "route.create", Resource: "route/" + row.ID, AfterFP: row.Host,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "route")
	}
	svc.deps.System.PublishRoutesNow() // Proxy 全量发布即时触发
	return &proxyv1.CreateRouteResponse{Route: routeMsg(*row)}, nil
}

// ListRoutes 非 owner 按 Team 过滤（ADR-0035 List 面：行级过滤在内存比对
// 调用方 Team 的 project 集合；owner 全量）。project 过滤下推 SQL 与游标
// 分页叠加（过滤语义不变）；Team 过滤在分页后的页内比对（peers 可见性
// 过滤同款形态——过滤语义不变，非 owner 的页可能稀疏）。
func (svc *Service) ListRoutes(ctx context.Context, req *proxyv1.ListRoutesRequest) (*proxyv1.ListRoutesResponse, error) {
	teamProjects, all, err := grpcapi.TeamProjectFilter(ctx, svc.deps.Projects)
	if err != nil {
		return nil, err
	}
	list, err := svc.deps.Routes.ListPage(ctx,
		req.GetProjectId(), req.GetAfterRouteSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "route")
	}
	out := &proxyv1.ListRoutesResponse{}
	for _, r := range list {
		if !all && !teamProjects[r.ProjectID] {
			continue
		}
		out.Routes = append(out.Routes, routeMsg(r))
	}
	return out, nil
}

func (svc *Service) DeleteRoute(ctx context.Context, req *proxyv1.DeleteRouteRequest) (*proxyv1.DeleteRouteResponse, error) {
	// 行级授权（ADR-0035）：此前 ID 直删零校验。
	if err := svc.deps.Auth.AuthorizeRouteID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Routes.SoftDelete(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：Route CRUD 未入事件流（同 CreateRoute——
			// 路由发布事实由 Proxy 全量发布面承载；行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "route.delete", Resource: "route/" + req.GetId(),
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "route")
	}
	svc.deps.System.PublishRoutesNow()
	return &proxyv1.DeleteRouteResponse{}, nil
}

// parentProjectAlive 是 Project 存活受理检查（事务内）。
func parentProjectAlive(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RequireActiveProject(ctx, st, projectID)
	}
}

// projectAppAlive 是 App 在 Project 内存活的受理检查（事务内）。
func projectAppAlive(projectID, appID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RequireProjectApp(ctx, st, projectID, appID)
	}
}

// routeMsg 把 Route 行投影为 proto 消息。
func routeMsg(r route.Route) *proxyv1.Route {
	return &proxyv1.Route{
		Id: r.ID, Seq: r.Seq, ProjectId: r.ProjectID, Host: r.Host, Path: r.Path,
		AppId: r.AppID, Process: r.Process, Port: r.Port,
		Protocol: string(r.Protocol), TlsMode: r.TLSMode, CreatedAt: r.CreatedAt,
	}
}
