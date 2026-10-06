package assembly

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lynx-go/grpcapi/gateway"
	"github.com/lynx-go/lynx"
	lynxhttp "github.com/lynx-go/lynx/server/http"
	"google.golang.org/grpc"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/console"
)

// httpReadHeaderTimeout 慢握手/Slowloris 上限；SSE 长连接落地（events
// follow，N1）时读写超时维持 0、仅保留此头超时。
const httpReadHeaderTimeout = 10 * time.Second

// gatewayRegistrations 是 REST 注解面的完整挂载清单（守卫 A 的对账对象：
// genproto 每个 Register*HandlerClient 必须在此出现，P1-13/A-9——identity
// 六服务曾整面漏挂）。ReceiveWebhook 唯一例外：HMAC 需原始请求体字节，
// 走 gateway_hooks.go 的原生挂法，不进本清单。
func gatewayRegistrations() []gateway.RegisterFunc {
	return []gateway.RegisterFunc{
		registerClient(systemv1.NewSystemServiceClient, systemv1.RegisterSystemServiceHandlerClient),
		registerClient(systemv1.NewGovernanceServiceClient, systemv1.RegisterGovernanceServiceHandlerClient),
		registerClient(systemv1.NewPlatformServiceClient, systemv1.RegisterPlatformServiceHandlerClient),
		registerClient(identityv1.NewUsersServiceClient, identityv1.RegisterUsersServiceHandlerClient),
		registerClient(identityv1.NewTeamsServiceClient, identityv1.RegisterTeamsServiceHandlerClient),
		registerClient(identityv1.NewRolesServiceClient, identityv1.RegisterRolesServiceHandlerClient),
		registerClient(identityv1.NewTokensServiceClient, identityv1.RegisterTokensServiceHandlerClient),
		registerClient(identityv1.NewInvitationsServiceClient, identityv1.RegisterInvitationsServiceHandlerClient),
		registerClient(identityv1.NewAuditQueryServiceClient, identityv1.RegisterAuditQueryServiceHandlerClient),
		registerClient(structurev1.NewProjectsServiceClient, structurev1.RegisterProjectsServiceHandlerClient),
		registerClient(structurev1.NewAppsServiceClient, structurev1.RegisterAppsServiceHandlerClient),
		registerClient(structurev1.NewSecretsServiceClient, structurev1.RegisterSecretsServiceHandlerClient),
		registerClient(structurev1.NewConfigsServiceClient, structurev1.RegisterConfigsServiceHandlerClient),
		registerClient(structurev1.NewSharedVariablesServiceClient, structurev1.RegisterSharedVariablesServiceHandlerClient),
		registerClient(structurev1.NewVolumesServiceClient, structurev1.RegisterVolumesServiceHandlerClient),
		registerClient(structurev1.NewNetworksServiceClient, structurev1.RegisterNetworksServiceHandlerClient),
		registerClient(structurev1.NewDatabasesServiceClient, structurev1.RegisterDatabasesServiceHandlerClient),
		registerClient(deliveryv1.NewDeploymentsServiceClient, deliveryv1.RegisterDeploymentsServiceHandlerClient),
		registerClient(deliveryv1.NewRevisionsServiceClient, deliveryv1.RegisterRevisionsServiceHandlerClient),
		registerClient(deliveryv1.NewBuildsServiceClient, deliveryv1.RegisterBuildsServiceHandlerClient),
		// Hooks 配置面（Set/Get/Rotate）：注解面 RPC；接收面 ReceiveWebhook
		// 在原生挂法（mountHooks），不在此。
		registerClient(deliveryv1.NewHooksServiceClient, deliveryv1.RegisterHooksServiceHandlerClient),
		registerClient(runtimev1.NewNodesServiceClient, runtimev1.RegisterNodesServiceHandlerClient),
		// Exec 受理面注解 RPC（F3.2，ADR-0049）；会话流在原生挂法
		//（mountExecStream——WS），不在此。
		registerClient(runtimev1.NewExecServiceClient, runtimev1.RegisterExecServiceHandlerClient),
		// Automation（F1.5/F1.6/F1.7）：Task/Run/Schedule 聚合面。
		registerClient(automationv1.NewTasksServiceClient, automationv1.RegisterTasksServiceHandlerClient),
		registerClient(automationv1.NewRunsServiceClient, automationv1.RegisterRunsServiceHandlerClient),
		registerClient(automationv1.NewSchedulesServiceClient, automationv1.RegisterSchedulesServiceHandlerClient),
		registerClient(proxyv1.NewRoutesServiceClient, proxyv1.RegisterRoutesServiceHandlerClient),
		registerClient(telemetryv1.NewEventsServiceClient, telemetryv1.RegisterEventsServiceHandlerClient),
		registerClient(telemetryv1.NewLogsServiceClient, telemetryv1.RegisterLogsServiceHandlerClient),
		// Metrics/Alerting（F2.5，ADR-0041）：查询面 + 告警配置面。
		registerClient(telemetryv1.NewMetricsServiceClient, telemetryv1.RegisterMetricsServiceHandlerClient),
		registerClient(telemetryv1.NewAlertingServiceClient, telemetryv1.RegisterAlertingServiceHandlerClient),
	}
}

// NewGatewayHandler 构造 gateway 的 HTTP handler（grpc-gateway mux + 注解面
// 全量注册 + 原生 webhook 挂法 + 原生 SSE 事件入口 + Console 静态面）。
// 从 NewGatewayServer 抽出为独立入口：apitest REST 冒烟经 httptest 驱动与
// 生产完全同一清单与错误信封——清单漂移先在守卫 A 红，行为面在此冒烟红。
// eventsSrc 为 nil 时跳过 SSE 挂载（不需要订阅面的 REST 冒烟）。最外层
// console.Mount 把非 /v1 路径交给 embed 静态产物（F2.6/ADR-0044；/v1/*
// 零变化）。execSrc nil = exec 原生入口停用（纯 gateway 测试形态）。
func NewGatewayHandler(logger *slog.Logger, conn grpc.ClientConnInterface, eventsSrc *fleetlygrpc.EventStreamSource, execSrc *fleetlygrpc.ExecStreamSource) (http.Handler, error) {
	mux := gateway.NewMux(gateway.MuxOptions{
		ErrorHandler: newGatewayErrorHandler(logger),
	})
	if err := gateway.Register(context.Background(), mux, conn, gatewayRegistrations()...); err != nil {
		return nil, err
	}
	h := mountHooks(mux, newHooksHandler(deliveryv1.NewHooksServiceClient(conn)))
	// 上传产物 REST 入口（F3.5，ADR-0019 附录 A 留白）：与 ListUploads
	// 同路径、按方法分派（非 POST 回落 gateway，注解面不被遮蔽）。
	h = mountUploads(h, newUploadsHandler(deliveryv1.NewBuildsServiceClient(conn), h))
	if eventsSrc != nil {
		h = mountEventsSSE(h, newEventsSSEHandler(eventsSrc))
	}
	if execSrc != nil {
		// exec 原生入口三件（F3.2，ADR-0049）：中继代理 WS + 会话消费端
		// WS + 控制面二进制下载。
		h = mountRelay(h, execSrc.Engine())
		h = mountExecStream(h, execSrc)
		h = mountPlatformBinary(h, execSrc.Engine())
	}
	return console.Mount(h), nil
}

// NewGatewayServer 装配 REST gateway（grpc-gateway）：统一错误信封出口
// （newGatewayErrorHandler，apperr 驱动）、protojson snake_case marshaler、
// 整条 gateway 共享一条 gRPC 连接（gateway.Dial 惰性建连）。cleanup 关闭
// 共享连接。SSE 事件入口经 EventStreamSource 与 gRPC StreamEvents 同源。
func NewGatewayServer(
	app lynx.App,
	cfg *config.AppConfig,
	services *fleetlygrpc.Services,
) (*lynxhttp.Server, func(), error) {
	conn, err := gateway.Dial(app.Context(), cfg.GRPCAddr(), gateway.DialConfig{})
	if err != nil {
		return nil, nil, err
	}
	handler, err := NewGatewayHandler(app.Logger(), conn, fleetlygrpc.NewEventStreamSource(services), fleetlygrpc.NewExecStreamSource(services))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	srv := lynxhttp.NewServer(handler,
		lynxhttp.WithAddr(cfg.HTTPAddr()),
		lynxhttp.WithLogger(app.Logger()),
		// 长连接（SSE/WS）落地前保持保守读头超时；读写超时 0 由
		// WithServerOptions 显式声明，防 lynx 默认 60s 打在 net.Conn 上。
		lynxhttp.WithServerOptions(func(s *http.Server) {
			s.ReadTimeout = 0
			s.WriteTimeout = 0
			s.ReadHeaderTimeout = httpReadHeaderTimeout
		}),
		// Recovery 声明在最外层：gateway 转发任一环节 panic 都恢复为
		// 500 + 统一 JSON 错误体，不拖垮进程。
		lynxhttp.WithMiddleware(lynxhttp.Recovery()),
	)
	return srv, func() { _ = conn.Close() }, nil
}

// registerClient 以闭包适配 genproto 生成的 New*Client + Register*HandlerClient
// 对为 gateway.RegisterFunc（torchwood 平移）。
func registerClient[T any](
	newClient func(grpc.ClientConnInterface) T,
	register func(context.Context, *runtime.ServeMux, T) error,
) gateway.RegisterFunc {
	return func(ctx context.Context, mux *runtime.ServeMux, conn grpc.ClientConnInterface) error {
		return register(ctx, mux, newClient(conn))
	}
}

// mountEventsSSE 把 SSE 入口挂在 root mux 的精确路径（先于 gateway 的
// "/" 回落；与 webhook 原生挂法同族）。
func mountEventsSSE(gw http.Handler, sse http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(eventsSSEPath, sse)
	mux.Handle("/", gw)
	return mux
}
