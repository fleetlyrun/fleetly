package main

import (
	"context"
	gohttp "net/http"

	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxhttp "github.com/lynx-go/lynx/server/http"
)

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// ProviderSet 是 edgefleetd 的 Wire 依赖集（装配形态对齐 lynx
// _examples/boot：Bootstrap 聚合钩子与服务，cleanup 由 main 挂 OnPostStop）。
var ProviderSet = wire.NewSet(
	boot.New,
	NewConfig,
	NewHTTPServer,
	NewServices,
	NewServiceFactories,
	NewPreStarts,
	NewDrains,
	NewPreStops,
	NewPostStops,
)

// NewConfig 从应用配置（flags + 配置文件）解出 AppConfig。
func NewConfig(app lynx.App) (*AppConfig, error) {
	c := new(AppConfig)
	if err := app.Config().Unmarshal(c); err != nil {
		return nil, err
	}
	if c.Addr == "" {
		c.Addr = defaultHTTPAddr
	}
	return c, nil
}

// NewHTTPServer 创建控制面 HTTP 服务：本阶段仅挂 lynx 内置健康端点
// （/healthz/liveness 与 /healthz/readiness），业务路由随后续阶段接入
// （根 mux 由 newRootMux 统一提供，测试复用同一构造）。
func NewHTTPServer(app lynx.App, cfg *AppConfig) *lynxhttp.Server {
	return lynxhttp.NewServer(newRootMux(),
		lynxhttp.WithAddr(cfg.Addr),
		lynxhttp.WithHealthCheckers(app.HealthCheckers),
		lynxhttp.WithLogger(app.Logger("logger", "http-requestlog")),
	)
}

// newRootMux 返回控制面 HTTP 根路由；健康端点由 lynxhttp.Server 在
// buildHandler 时另行挂载，不经过此 mux。
func newRootMux() *gohttp.ServeMux {
	return gohttp.NewServeMux()
}

func NewServices(hs *lynxhttp.Server) []lynx.Service {
	return []lynx.Service{hs}
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
