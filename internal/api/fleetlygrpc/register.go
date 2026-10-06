package fleetlygrpc

// Register 把五上下文服务注册到 gRPC server（assembly 唯一调用点）。

import (
	"google.golang.org/grpc"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// RegisterAll 注册全部上下文服务。
func RegisterAll(srv *grpc.Server, s *Services) {
	structurev1.RegisterProjectsServiceServer(srv, &ProjectsService{s: s})
	structurev1.RegisterAppsServiceServer(srv, &AppsService{s: s})
	structurev1.RegisterSecretsServiceServer(srv, &SecretsService{s: s})
	structurev1.RegisterConfigsServiceServer(srv, &ConfigsService{s: s})
	structurev1.RegisterSharedVariablesServiceServer(srv, &SharedVariablesService{s: s})
	structurev1.RegisterVolumesServiceServer(srv, &VolumesService{s: s})
	structurev1.RegisterNetworksServiceServer(srv, &NetworksService{s: s})
	structurev1.RegisterDatabasesServiceServer(srv, &DatabasesService{s: s})
	deliveryv1.RegisterDeploymentsServiceServer(srv, &DeploymentsService{s: s})
	deliveryv1.RegisterRevisionsServiceServer(srv, &RevisionsService{s: s})
	deliveryv1.RegisterBuildsServiceServer(srv, &BuildsService{s: s})
	deliveryv1.RegisterHooksServiceServer(srv, &HooksService{s: s})
	automationv1.RegisterTasksServiceServer(srv, &TasksService{s: s})
	automationv1.RegisterRunsServiceServer(srv, &RunsService{s: s})
	automationv1.RegisterSchedulesServiceServer(srv, &SchedulesService{s: s})
	runtimev1.RegisterNodesServiceServer(srv, &NodesService{s: s})
	runtimev1.RegisterExecServiceServer(srv, &ExecService{s: s})
	proxyv1.RegisterRoutesServiceServer(srv, &RoutesService{s: s})
	telemetryv1.RegisterEventsServiceServer(srv, &EventsService{s: s})
	telemetryv1.RegisterLogsServiceServer(srv, &LogsService{s: s})
	telemetryv1.RegisterMetricsServiceServer(srv, &MetricsService{s: s})
	telemetryv1.RegisterAlertingServiceServer(srv, &AlertingService{s: s})
	identityv1.RegisterUsersServiceServer(srv, &UsersService{s: s})
	identityv1.RegisterTeamsServiceServer(srv, &TeamsService{s: s})
	identityv1.RegisterRolesServiceServer(srv, &RolesService{s: s})
	identityv1.RegisterTokensServiceServer(srv, &TokensService{s: s})
	identityv1.RegisterInvitationsServiceServer(srv, &InvitationsService{s: s})
	identityv1.RegisterAuditQueryServiceServer(srv, &AuditQueryService{s: s})
	systemv1.RegisterGovernanceServiceServer(srv, &GovernanceService{s: s})
	systemv1.RegisterPlatformServiceServer(srv, &PlatformService{s: s})
}
