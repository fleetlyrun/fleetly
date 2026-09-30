package assembly

import (
	"context"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lynx-go/grpcapi/gateway"
	"github.com/lynx-go/lynx"
	lynxhttp "github.com/lynx-go/lynx/server/http"
	"google.golang.org/grpc"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// httpReadHeaderTimeout 慢握手/Slowloris 上限；SSE 长连接落地（events
// follow，N1）时读写超时维持 0、仅保留此头超时。
const httpReadHeaderTimeout = 10 * time.Second

// NewGatewayServer 装配 REST gateway（grpc-gateway）：统一错误信封出口
// （newGatewayErrorHandler，apperr 驱动）、protojson snake_case marshaler、
// 整条 gateway 共享一条 gRPC 连接（gateway.Dial 惰性建连）。cleanup 关闭
// 共享连接。
func NewGatewayServer(
	app lynx.App,
	cfg *config.AppConfig,
) (*lynxhttp.Server, func(), error) {
	mux := gateway.NewMux(gateway.MuxOptions{
		ErrorHandler: newGatewayErrorHandler(app.Logger()),
	})

	conn, err := gateway.Dial(app.Context(), cfg.GRPCAddr(), gateway.DialConfig{})
	if err != nil {
		return nil, nil, err
	}

	register := []gateway.RegisterFunc{
		registerClient(systemv1.NewSystemServiceClient, systemv1.RegisterSystemServiceHandlerClient),
		registerClient(structurev1.NewProjectsServiceClient, structurev1.RegisterProjectsServiceHandlerClient),
		registerClient(structurev1.NewAppsServiceClient, structurev1.RegisterAppsServiceHandlerClient),
		registerClient(structurev1.NewSecretsServiceClient, structurev1.RegisterSecretsServiceHandlerClient),
		registerClient(structurev1.NewConfigsServiceClient, structurev1.RegisterConfigsServiceHandlerClient),
		registerClient(structurev1.NewVolumesServiceClient, structurev1.RegisterVolumesServiceHandlerClient),
		registerClient(structurev1.NewNetworksServiceClient, structurev1.RegisterNetworksServiceHandlerClient),
		registerClient(deliveryv1.NewDeploymentsServiceClient, deliveryv1.RegisterDeploymentsServiceHandlerClient),
		registerClient(deliveryv1.NewRevisionsServiceClient, deliveryv1.RegisterRevisionsServiceHandlerClient),
		registerClient(deliveryv1.NewBuildsServiceClient, deliveryv1.RegisterBuildsServiceHandlerClient),
		registerClient(runtimev1.NewNodesServiceClient, runtimev1.RegisterNodesServiceHandlerClient),
		registerClient(edgev1.NewRoutesServiceClient, edgev1.RegisterRoutesServiceHandlerClient),
		registerClient(telemetryv1.NewEventsServiceClient, telemetryv1.RegisterEventsServiceHandlerClient),
		registerClient(telemetryv1.NewLogsServiceClient, telemetryv1.RegisterLogsServiceHandlerClient),
	}
	if err := gateway.Register(app.Context(), mux, conn, register...); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	srv := lynxhttp.NewServer(mountHooks(mux, newHooksHandler(deliveryv1.NewHooksServiceClient(conn))),
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
