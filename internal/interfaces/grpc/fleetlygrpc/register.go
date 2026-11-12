package fleetlygrpc

// Register 把五上下文服务注册到 gRPC server（assembly 唯一调用点）。

import (
	"context"
	"time"

	"google.golang.org/grpc"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
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
)

// RegisterAll 注册全部上下文服务。
func RegisterAll(srv *grpc.Server, s *Services) {
	structuregrpc.Register(srv, s.structureDeps())
	dataenginesgrpc.Register(srv)
	datagrpc.Register(srv, s.dataDeps())
	deliverygrpc.Register(srv, s.deliveryDeps())
	deliveryv1.RegisterTemplatesServiceServer(srv, &TemplatesService{s: s})
	hooksgrpc.Register(srv, hooksgrpc.Deps{
		Hooks: s.Hooks, Apps: s.Apps, Projects: lateProjectRepo{s}, Cipher: s.Cipher, Deploy: s.Deploy, Registry: s.Registry, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		VarLookup: func(ctx context.Context, appRow *app.App) (composeintake.VariableLookup, error) {
			return deliverygrpc.DeployVarLookup(ctx, s.SharedVariables, s.AppVariables, appRow)
		},
	})
	githubappsgrpc.Register(srv, githubappsgrpc.Deps{
		GitHubApps: s.GitHubApps, GitHub: func() githubapp.Client { return s.GitHub }, Cipher: s.Cipher, Projects: lateProjectRepo{s}, Auth: s,
		Commit:          func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		ManifestBase:    func() string { return s.GitHubManifestBase },
		ManifestAPIBase: func() string { return s.GitHubManifestAPIBase },
	})
	registrygrpc.Register(srv, registrygrpc.Deps{Registry: s.Registry, Auth: s})
	automationgrpc.Register(srv, automationgrpc.Deps{
		Tasks: s.Tasks, TaskRepo: s.TaskRepo, Runs: s.Runs, Schedules: s.Schedules, Tokens: s.Tokens,
		Projects: lateProjectRepo{s}, Auth: s, Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		TaskQuota: s.taskQuota, ParentProjectAlive: s.parentProjectAlive,
		ResolveProjectRef: s.resolveProjectRef, Subscribe: s.subscribeEvents, TeamFilter: eventsTeamFilterOf,
		Wait: waitSource(s), Now: func() time.Time { return s.DB.Clock().Now() },
	})
	nodesgrpc.Register(srv, nodesgrpc.Deps{
		NodeRepo: s.NodeRepo, Nodes: s.Nodes, Runtime: func() capability.Runtime { return s.Runtime }, GatewayPort: s.GatewayPort,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	execgrpc.Register(srv, execgrpc.Deps{
		Exec: s.Exec, Nodes: s.Nodes, Auth: s, Anchor: s.Anchor, Tickets: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	diagnosisgrpc.Register(srv, diagnosisgrpc.Deps{System: s.System, Apps: s.Apps, Auth: s})
	routesgrpc.Register(srv, routesgrpc.Deps{
		Routes: s.Routes, Projects: lateProjectRepo{s}, System: s.System, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	eventsgrpc.Register(srv, eventsgrpc.Deps{Outbox: s.OutboxEvents, Tickets: s})
	logsgrpc.Register(srv, logsgrpc.Deps{
		Logging: func() capability.Logging { return s.Logging }, Runtime: func() capability.Runtime { return s.Runtime },
		Apps: s.Apps, Projects: lateProjectRepo{s}, Databases: s.Databases, PubSubs: s.PubSubs,
		BucketStores: s.BucketStores, Runs: s.Runs, Tasks: s.TaskRepo, Auth: s,
	})
	metricsgrpc.Register(srv, metricsgrpc.Deps{
		Metrics: func() capability.Metrics { return s.Metrics }, Databases: s.Databases, PubSubs: s.PubSubs, BucketStores: s.BucketStores, Auth: s,
	})
	alertinggrpc.Register(srv, alertinggrpc.Deps{
		Cipher: s.Cipher, System: s.System, Apps: s.Apps, Projects: lateProjectRepo{s}, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	identitygrpc.Register(srv, identitygrpc.Deps{
		Users: s.Users, Teams: s.Teams, Roles: s.Roles, Tokens: s.Tokens,
		Memberships: s.Memberships, Invitations: s.Invitations, Audits: s.Audits,
		Projects: lateProjectRepo{s}, Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		ScopeVocabulary: s.ScopeVocabulary, LoginTokenTTL: func() time.Duration { return s.LoginTokenTTL }, Clock: s.DB.Clock(),
	})
	governancegrpc.Register(srv, governancegrpc.Deps{
		Freezes: s.Freezes,
		Commit:  func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	platformgrpc.Register(srv, platformgrpc.Deps{Backup: s.Backup, DataRoot: s.DataRoot})
}
