package fleetlygrpc

// Register 把五上下文服务注册到 gRPC server（assembly 唯一调用点）。

import (
	"context"

	"google.golang.org/grpc"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/dataenginesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/diagnosisgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/eventsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/execgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/governancegrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/metricsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/nodesgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/platformgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/routesgrpc"
)

// RegisterAll 注册全部上下文服务。
func RegisterAll(srv *grpc.Server, s *Services) {
	structurev1.RegisterProjectsServiceServer(srv, &ProjectsService{s: s})
	structurev1.RegisterAppsServiceServer(srv, &AppsService{s: s})
	structurev1.RegisterSecretsServiceServer(srv, &SecretsService{s: s})
	structurev1.RegisterConfigsServiceServer(srv, &ConfigsService{s: s})
	structurev1.RegisterSharedVariablesServiceServer(srv, &SharedVariablesService{s: s})
	structurev1.RegisterAppVariablesServiceServer(srv, &AppVariablesService{s: s})
	structurev1.RegisterVolumesServiceServer(srv, &VolumesService{s: s})
	structurev1.RegisterNetworksServiceServer(srv, &NetworksService{s: s})
	structurev1.RegisterDatabasesServiceServer(srv, &DatabasesService{s: s})
	dataenginesgrpc.Register(srv)
	structurev1.RegisterPubSubsServiceServer(srv, &PubSubsService{s: s})
	structurev1.RegisterBucketStoresServiceServer(srv, &BucketStoresService{s: s})
	deliveryv1.RegisterDeploymentsServiceServer(srv, &DeploymentsService{s: s})
	deliveryv1.RegisterRevisionsServiceServer(srv, &RevisionsService{s: s})
	deliveryv1.RegisterBuildsServiceServer(srv, &BuildsService{s: s})
	deliveryv1.RegisterTemplatesServiceServer(srv, &TemplatesService{s: s})
	deliveryv1.RegisterHooksServiceServer(srv, &HooksService{s: s})
	deliveryv1.RegisterGitHubAppsServiceServer(srv, newGitHubAppsService(s))
	deliveryv1.RegisterRegistryServiceServer(srv, &RegistryService{s: s})
	automationv1.RegisterTasksServiceServer(srv, &TasksService{s: s})
	automationv1.RegisterRunsServiceServer(srv, &RunsService{s: s})
	automationv1.RegisterSchedulesServiceServer(srv, &SchedulesService{s: s})
	nodesgrpc.Register(srv, nodesgrpc.Deps{
		NodeRepo: s.NodeRepo, Nodes: s.Nodes, Runtime: func() capability.Runtime { return s.Runtime }, GatewayPort: s.GatewayPort,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	execgrpc.Register(srv, execgrpc.Deps{
		Exec: s.Exec, Nodes: s.Nodes, Auth: s, Anchor: s.Anchor, Tickets: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	runtimev1.RegisterWorkloadsServiceServer(srv, &WorkloadsService{s: s})
	diagnosisgrpc.Register(srv, diagnosisgrpc.Deps{System: s.System, Apps: s.Apps, Auth: s})
	routesgrpc.Register(srv, routesgrpc.Deps{
		Routes: s.Routes, Projects: s.Projects, System: s.System, Auth: s,
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	eventsgrpc.Register(srv, eventsgrpc.Deps{Outbox: s.OutboxEvents, Tickets: s})
	telemetryv1.RegisterLogsServiceServer(srv, &LogsService{s: s})
	metricsgrpc.Register(srv, metricsgrpc.Deps{
		Metrics: func() capability.Metrics { return s.Metrics }, Databases: s.Databases, PubSubs: s.PubSubs, BucketStores: s.BucketStores, Auth: s,
	})
	telemetryv1.RegisterAlertingServiceServer(srv, &AlertingService{s: s})
	identityv1.RegisterUsersServiceServer(srv, &UsersService{s: s})
	identityv1.RegisterTeamsServiceServer(srv, &TeamsService{s: s})
	identityv1.RegisterRolesServiceServer(srv, &RolesService{s: s})
	identityv1.RegisterTokensServiceServer(srv, &TokensService{s: s})
	identityv1.RegisterInvitationsServiceServer(srv, &InvitationsService{s: s})
	identityv1.RegisterAuditQueryServiceServer(srv, &AuditQueryService{s: s})
	governancegrpc.Register(srv, governancegrpc.Deps{
		Freezes: s.Freezes,
		Commit:  func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	})
	platformgrpc.Register(srv, platformgrpc.Deps{Backup: s.Backup, DataRoot: s.DataRoot})
}
