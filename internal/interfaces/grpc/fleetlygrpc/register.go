package fleetlygrpc

// Register 把五上下文服务注册到 gRPC server（assembly 唯一调用点）。

import (
	"context"
	"time"

	"google.golang.org/grpc"

	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/alertinggrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/automationgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/dataenginesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/datagrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/deliverygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/diagnosisgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/eventsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/execgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/githubappsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/governancegrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/hooksgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/identitygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/logsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/metricsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/nodesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/platformgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/registrygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/routesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/structuregrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/templatesgrpc"
)

// RegisterAll 注册全部上下文服务。
func RegisterAll(srv *grpc.Server, s *Services) {
	structuregrpc.Register(srv, s.structureDeps())
	dataenginesgrpc.Register(srv)
	datagrpc.Register(srv, s.dataDeps())
	deliverygrpc.Register(srv, s.deliveryDeps())
	templatesgrpc.Register(srv, s.templatesDeps())
	hooksgrpc.Register(srv, hooksgrpc.Deps{
		Hooks: s.Delivery.Hooks, Apps: s.Structure.Apps, Projects: lateProjectRepo{s}, Cipher: s.Core.Cipher, Deploy: s.Modules.Deploy, Registry: s.Modules.Registry, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		VarLookup: func(ctx context.Context, appRow *app.App) (composeintake.VariableLookup, error) {
			return deliverygrpc.DeployVarLookup(ctx, s.Structure.SharedVariables, s.Structure.AppVariables, appRow)
		},
	})
	githubappsgrpc.Register(srv, githubappsgrpc.Deps{
		GitHubApps: s.Delivery.GitHubApps, GitHub: func() githubapp.Client { return s.Clients.GitHub }, Cipher: s.Core.Cipher, Projects: lateProjectRepo{s}, Auth: s,
		Commit:          func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		ManifestBase:    func() string { return s.Config.GitHubManifestBase },
		ManifestAPIBase: func() string { return s.Config.GitHubManifestAPIBase },
	})
	registrygrpc.Register(srv, registrygrpc.Deps{Registry: s.Modules.Registry, Auth: s})
	automationgrpc.Register(srv, automationgrpc.Deps{
		Tasks: s.Modules.Tasks, TaskRepo: s.Automation.TaskRepo, Runs: s.Automation.Runs, Schedules: s.Automation.Schedules, Tokens: s.Identity.Tokens,
		Projects: lateProjectRepo{s}, Auth: s, Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		TaskQuota: s.taskQuota, ParentProjectAlive: s.parentProjectAlive,
		ResolveProjectRef: s.resolveProjectRef, Subscribe: s.subscribeEvents, TeamFilter: eventsTeamFilterOf,
		Wait: waitSource(s), Now: func() time.Time { return s.Core.DB.Clock().Now() },
	})
	nodesgrpc.Register(srv, nodesgrpc.Deps{
		NodeRepo: s.Platform.NodeRepo, Nodes: s.Modules.Nodes, Runtime: func() capability.Runtime { return s.Core.Runtime }, GatewayPort: s.Config.GatewayPort,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	execgrpc.Register(srv, execgrpc.Deps{
		Exec: s.Modules.Exec, Nodes: s.Modules.Nodes, Auth: s, Anchor: s.Core.Anchor, Tickets: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	diagnosisgrpc.Register(srv, diagnosisgrpc.Deps{System: s.Modules.System, Apps: s.Structure.Apps, Auth: s})
	routesgrpc.Register(srv, routesgrpc.Deps{
		Routes: s.Platform.Routes, Projects: lateProjectRepo{s}, System: s.Modules.System, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	eventsgrpc.Register(srv, eventsgrpc.Deps{Outbox: s.Platform.OutboxEvents, Tickets: s})
	logsgrpc.Register(srv, logsgrpc.Deps{
		Logging: func() capability.Logging { return s.Core.Logging }, Runtime: func() capability.Runtime { return s.Core.Runtime },
		Apps: s.Structure.Apps, Projects: lateProjectRepo{s}, Databases: s.Data.Databases, PubSubs: s.Data.PubSubs,
		BucketStores: s.Data.BucketStores, Runs: s.Automation.Runs, Tasks: s.Automation.TaskRepo, Auth: s,
	})
	metricsgrpc.Register(srv, metricsgrpc.Deps{
		Metrics: func() capability.Metrics { return s.Core.Metrics }, Databases: s.Data.Databases, PubSubs: s.Data.PubSubs, BucketStores: s.Data.BucketStores, Auth: s,
	})
	alertinggrpc.Register(srv, alertinggrpc.Deps{
		Cipher: s.Core.Cipher, System: s.Modules.System, Apps: s.Structure.Apps, Projects: lateProjectRepo{s}, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	identitygrpc.Register(srv, identitygrpc.Deps{
		Users: s.Identity.Users, Teams: s.Identity.Teams, Roles: s.Identity.Roles, Tokens: s.Identity.Tokens,
		Memberships: s.Identity.Memberships, Invitations: s.Identity.Invitations, Audits: s.Identity.Audits,
		Projects: lateProjectRepo{s}, Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		ScopeVocabulary: s.Config.ScopeVocabulary, LoginTokenTTL: func() time.Duration { return s.Config.LoginTokenTTL }, Clock: s.Core.DB.Clock(),
	})
	governancegrpc.Register(srv, governancegrpc.Deps{
		Freezes: s.Platform.Freezes,
		Commit:  func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	platformgrpc.Register(srv, platformgrpc.Deps{Backup: s.Modules.Backup, DataRoot: s.Config.DataRoot})
}
