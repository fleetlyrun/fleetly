//go:build wireinject
// +build wireinject

package assembly

import (
	"github.com/google/wire"
	"github.com/lynxland/lynx"
	"github.com/lynxland/lynx/boot"
	"github.com/lynxland/lynx/buildinfo"
)

func wireBootstrap(app lynx.App, info buildinfo.BuildInfo) (*boot.Bootstrap, func(), error) {
	panic(wire.Build(ProviderSet))
}
