//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.
package runtime

import (
	"log/slog"

	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
)

// wireBootstrap 是 Wire 注入器（version 经 Bootstrap 参数进入依赖图，
// 见 bootstrap.go 的 Version 注释）。
func wireBootstrap(app lynx.App, slogger *slog.Logger, version Version) (*boot.Bootstrap, func(), error) {
	panic(wire.Build(ProviderSet))
}
