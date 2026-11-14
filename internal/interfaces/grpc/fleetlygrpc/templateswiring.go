package fleetlygrpc

// templateswiring.go 是 templatesgrpc 的组合根投影：Services 的宽依赖在此
// 收窄为 templatesgrpc.Deps，内层动作复用各拆分包的构造器。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/datagrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/deliverygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/routesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/structuregrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/templatesgrpc"
)

func (s *Services) templatesDeps() templatesgrpc.Deps {
	return templatesgrpc.Deps{
		Catalog: s.Templates.Catalog, CatalogURL: func() string { return s.Config.TemplatesCatalogURL },
		Cipher: s.Core.Cipher, Apps: s.Structure.Apps, Networks: s.Structure.Networks, Secrets: s.Structure.Secrets,
		Databases: s.Data.Databases, Routes: s.Platform.Routes, Projects: lateProjectRepo{s}, Auth: s,
		RequireScope:   s.requireScope,
		Commit:         func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		CreateApp:      structuregrpc.NewApps(s.structureDeps()).CreateApp,
		CreateNetwork:  structuregrpc.NewNetworks(s.structureDeps()).CreateNetwork,
		PutSecret:      structuregrpc.NewSecrets(s.structureDeps()).PutSecret,
		CreateDatabase: datagrpc.NewDatabases(s.dataDeps()).CreateDatabase,
		Deploy:         deliverygrpc.NewDeployments(s.deliveryDeps()).Deploy,
		CreateRoute: routesgrpc.New(routesgrpc.Deps{
			Routes: s.Platform.Routes, Projects: lateProjectRepo{s}, System: s.Modules.System, Auth: s,
			Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		}).CreateRoute,
		MintDatabasePassword: datagrpc.MintDatabasePassword,
	}
}
