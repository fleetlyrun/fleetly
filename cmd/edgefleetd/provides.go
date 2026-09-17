package main

import (
	"context"

	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"
)

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// ProviderSet 是 edgefleetd 的 Wire 依赖集（装配形态对齐 lynx
// _examples/boot：Bootstrap 聚合钩子与服务，cleanup 由 main 挂 OnPostStop）。
var ProviderSet = wire.NewSet(
	boot.New,
	NewConfig,
	NewGRPCServer,
	NewSystemService,
	NewHTTPServer,
	NewServices,
	NewServiceFactories,
	NewPreStarts,
	NewDrains,
	NewPreStops,
	NewPostStops,
)

// NewConfig 从应用配置（flags + 配置文件）解出 AppConfig，缺省值在此回落。
func NewConfig(app lynx.App) (*AppConfig, error) {
	c := new(AppConfig)
	if err := app.Config().Unmarshal(c); err != nil {
		return nil, err
	}
	if c.Addr == "" {
		c.Addr = defaultHTTPAddr
	}
	if c.GRPC.Addr == "" {
		c.GRPC.Addr = defaultGRPCAddr
	}
	return c, nil
}

// NewHTTPServer 创建控制面 HTTP 服务：根 handler 是 grpc-gateway mux
// （REST /v1/** 经 gateway 反代到本进程 gRPC，见 newGatewayMux）；
// /healthz/liveness 与 /healthz/readiness 由 lynxhttp.Server 自行挂载，
// 与 gateway 路由共存（torchwood 同款双面单端口形态）。
func NewHTTPServer(app lynx.App, cfg *AppConfig) (*lynxhttp.Server, error) {
	mux, err := newGatewayMux(grpcEndpointFromAddr(cfg.GRPCAddr()))
	if err != nil {
		return nil, err
	}
	return lynxhttp.NewServer(mux,
		lynxhttp.WithAddr(cfg.Addr),
		lynxhttp.WithHealthCheckers(app.HealthCheckers),
		lynxhttp.WithLogger(app.Logger("logger", "http-requestlog")),
	), nil
}

func NewServices(hs *lynxhttp.Server, gs *lynxgrpc.Server) []lynx.Service {
	return []lynx.Service{hs, gs}
}

func NewServiceFactories() []lynx.ServiceFactory {
	return []lynx.ServiceFactory{}
}

func NewPreStarts(app lynx.App) boot.PreStartHooks {
	return boot.PreStartHooks{
		func(ctx context.Context) error {
			app.Logger().Info("edgefleetd starting")
			return nil
		},
	}
}

func NewDrains() boot.DrainHooks {
	return boot.DrainHooks{}
}

func NewPreStops(app lynx.App) boot.PreStopHooks {
	return boot.PreStopHooks{
		func(ctx context.Context) error {
			app.Logger().Info("edgefleetd stopping")
			return nil
		},
	}
}

func NewPostStops() boot.PostStopHooks {
	return boot.PostStopHooks{}
}
