package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	"github.com/lynx-go/lynx"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// NewGRPCServer 创建控制面 gRPC 服务（lynx server/grpc，内置恢复/日志
// 拦截器、grpc.health.v1 与反射）：注册 v0.1 全部服务面（T2.17/T2.20）。
// 拦截链顺序 = auth（最外）→ rate limit → protovalidate（形状校验，链尾）
// → handler；流式（Follow/Watch）走 auth/ratelimit 流式拦截器。
// 服务必须在 Serve 之前注册（lynx Server 在 Start 时才 Serve，构造期注册安全）。
func NewGRPCServer(
	app lynx.App,
	cfg *AppConfig,
	sys *SystemService,
	auth *api.Authenticator,
	apps *api.AppsService,
	deploys *api.DeploymentsService,
	revisions *api.RevisionsService,
	domains *api.DomainsService,
	env *api.EnvService,
	logsSvc *api.LogsService,
	events *api.EventsService,
	placement *api.PlacementService,
	tokens *api.TokensService,
) (*lynxgrpc.Server, error) {
	validator, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("construct protovalidate validator: %w", err)
	}
	srv := lynxgrpc.NewServer(
		lynxgrpc.WithAddr(cfg.GRPCAddr()),
		lynxgrpc.WithLogger(app.Logger()),
		lynxgrpc.WithHealthCheckers(app.HealthCheckers),
		lynxgrpc.WithInterceptors(
			// 链序 = auth（含 per-token 限流判定，鉴权后语义）→
			// protovalidate（形状校验）→ handler。
			auth.UnaryAuthInterceptor(),
			validateUnaryInterceptor(validator),
		),
		lynxgrpc.WithStreamInterceptors(
			auth.StreamAuthInterceptor(),
		),
	)
	g := srv.GetServer()
	serverv1.RegisterSystemServiceServer(g, sys)
	serverv1.RegisterAppsServiceServer(g, apps)
	serverv1.RegisterDeploymentsServiceServer(g, deploys)
	serverv1.RegisterRevisionsServiceServer(g, revisions)
	serverv1.RegisterDomainsServiceServer(g, domains)
	serverv1.RegisterEnvServiceServer(g, env)
	serverv1.RegisterLogsServiceServer(g, logsSvc)
	serverv1.RegisterEventsServiceServer(g, events)
	serverv1.RegisterPlacementServiceServer(g, placement)
	serverv1.RegisterTokensServiceServer(g, tokens)
	return srv, nil
}

// SystemService 实现 server.v1.SystemService（T0.3 契约验证面 + T2.17
// 状态汇总）。components 是命名健康组件集（复用各服务 CheckHealth 实现
// ——lynx Checker 接口无名，命名清单在本装配点显式维护）。
type SystemService struct {
	serverv1.UnimplementedSystemServiceServer
	components func() []namedHealthComponent
}

// namedHealthComponent 是带名的健康组件（CheckHealth 复用面）。
type namedHealthComponent struct {
	Name  string
	Check func() error
}

// NewSystemService 构造 SystemService：组件集 = 状态层四服务 + 入口。
// Traefik 是降级设计（收敛/续期失败只日志告警，ingressService.CheckHealth
// 恒健康）——此处与装配壳同语义如实上报恒健康。
func NewSystemService(st *state.Store, id *state.NodeIdentity, ob *state.Observer, sb *secrets.Box, ing *ingress.Manager) *SystemService {
	return &SystemService{components: func() []namedHealthComponent {
		return []namedHealthComponent{
			{"state.store", st.CheckHealth},
			{"state.identity", id.CheckHealth},
			{"state.observer", ob.CheckHealth},
			{"state.secrets", sb.CheckHealth},
			{"ingress.traefik", func() error { return nil }},
		}
	}}
}

// Ping 回应 service / version；version 由构建 -ldflags 注入（main.go），
// 未注入时为 "dev"。proto 字段上的 buf.validate 最小约束由拦截器统一校验。
func (s *SystemService) Ping(ctx context.Context, req *serverv1.PingRequest) (*serverv1.PingResponse, error) {
	return &serverv1.PingResponse{Service: "fleetlyd", Version: version}, nil
}

// GetSystemStatus 健康汇总（引擎/Traefik/状态层——复用 CheckHealth 面）：
// 逐组件如实上报，不聚合单一布尔，判断权在消费方。
func (s *SystemService) GetSystemStatus(ctx context.Context, req *serverv1.GetSystemStatusRequest) (*serverv1.GetSystemStatusResponse, error) {
	resp := &serverv1.GetSystemStatusResponse{Service: "fleetlyd", Version: version}
	for _, c := range s.components() {
		ch := &serverv1.ComponentHealth{Name: c.Name}
		if err := c.Check(); err != nil {
			ch.Ok = false
			ch.Error = err.Error()
		} else {
			ch.Ok = true
		}
		resp.Components = append(resp.Components, ch)
	}
	return resp, nil
}

// rateUnaryInterceptor / rateStreamInterceptor 已裁撤：per-token 限流在
// 鉴权之后才有身份可言，独立拦截器需要二次解析 metadata 与二次认证语义
// ——判定内联在 api.Authenticator.Authenticate（token 校验成功后立即
// 判限），链位注释保留以免后续误挂。

// validateExemptPrefixes 豁免框架服务：health/reflection 请求不带
// buf.validate 规则，跳过以求值零开销（torchwood 同款白名单）。
var validateExemptPrefixes = []string{
	"/grpc.health.v1.",
	"/grpc.reflection.",
}

// validateUnaryInterceptor 按 proto 上的 buf.validate 注解（protovalidate
// CEL 运行时求值）对请求消息做形状校验：形状约束在 proto 声明、在此统一
// 生效；跨字段与业务规则仍留在 app 用例层。仅 unary，与现有服务面一致。
func validateUnaryInterceptor(validator protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if validator == nil || validateExempt(info.FullMethod) {
			return handler(ctx, req)
		}
		msg, ok := req.(proto.Message)
		if !ok {
			// 非 proto 消息（理论不可达）不校验，交由后续链路处理。
			return handler(ctx, req)
		}
		if err := validator.Validate(msg); err != nil {
			return nil, validateStatusError(err)
		}
		return handler(ctx, req)
	}
}

// validateStatusError 映射校验错误为带 ErrorResponse 信封 detail 的 status：
//   - 规则违规 → InvalidArgument；不新造错误码（清单外码须 T0.5 冻结），
//     信封 code 留空（gateway 侧按退化信封渲染），违规明细进 message 与
//     context（键 = 字段路径，值 = 规则 ID + 说明）；
//   - CEL 编译/求值故障 → Internal（注解缺陷属服务端 bug，fail-closed
//     拒绝而非放行未校验请求），同样携带信封 detail。
func validateStatusError(err error) error {
	var violations *protovalidate.ValidationError
	if errors.As(err, &violations) {
		parts := make([]string, 0, len(violations.Violations))
		ctx := make(map[string]string, len(violations.Violations))
		for _, violation := range violations.Violations {
			parts = append(parts, violation.String())
			field := ""
			if violation.Proto != nil {
				field = protovalidate.FieldPathString(violation.Proto.GetField())
			}
			if field == "" {
				field = fmt.Sprintf("violation.%d", len(ctx))
			}
			ctx[field] = violation.Proto.GetRuleId() + ": " + violation.Proto.GetMessage()
		}
		return statusWithEnvelope(codes.InvalidArgument, strings.Join(parts, "; "), ctx)
	}
	msg := "request validation rules failed to evaluate: " + err.Error()
	return statusWithEnvelope(codes.Internal, msg, nil)
}

// statusWithEnvelope 构造携带 ErrorResponse detail 的 status（信封 code
// 留空、message 承载文案；detail 附加失败时退化为无 detail status，gateway
// 走退化信封路径）。
func statusWithEnvelope(c codes.Code, message string, context map[string]string) error {
	st := status.New(c, message)
	withDetail, derr := st.WithDetails(&sharedv1.ErrorResponse{Message: message, Context: context})
	if derr != nil {
		return st.Err()
	}
	return withDetail.Err()
}

func validateExempt(fullMethod string) bool {
	for _, prefix := range validateExemptPrefixes {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}
