//go:build wireinject
// +build wireinject

package assembly

import (
	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

func wireBootstrap(app lynx.App, info buildinfo.BuildInfo) (*boot.Bootstrap, func(), error) {
	panic(wire.Build(ProviderSet))
}
