// Package fleetly 是 fleetly 平台的类型化 gRPC 客户端 SDK。CLI-over-SDK
// 是 cmd/fleetly 消费平台的唯一通道；外部集成方同样经本 SDK 连接
// fleetlyd（gRPC 或经 gateway 的 REST 面）。
//
// 凭证：Authorization: Bearer <token>（Token 体系随账号批次接入；当前
// 无鉴权方法为 public 面）。
package fleetly

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// DefaultTimeout 是单次 unary 调用的默认时长期望（调用方经 context 覆盖）。
const DefaultTimeout = 30 * time.Second

// Client 是平台的类型化入口；零值不可用，经 Dial 构造。
type Client struct {
	conn       *grpc.ClientConn
	System     systemv1.SystemServiceClient
	Governance systemv1.GovernanceServiceClient
	Platform   systemv1.PlatformServiceClient

	Projects    structurev1.ProjectsServiceClient
	Apps        structurev1.AppsServiceClient
	Secrets     structurev1.SecretsServiceClient
	Configs     structurev1.ConfigsServiceClient
	SharedVars  structurev1.SharedVariablesServiceClient
	AppVars     structurev1.AppVariablesServiceClient
	Volumes     structurev1.VolumesServiceClient
	Networks    structurev1.NetworksServiceClient
	Databases   structurev1.DatabasesServiceClient
	Deployments deliveryv1.DeploymentsServiceClient
	Templates   deliveryv1.TemplatesServiceClient
	Revisions   deliveryv1.RevisionsServiceClient
	Builds      deliveryv1.BuildsServiceClient
	Hooks       deliveryv1.HooksServiceClient
	GitHubApps  deliveryv1.GitHubAppsServiceClient
	Registry    deliveryv1.RegistryServiceClient
	Tasks       automationv1.TasksServiceClient
	Runs        automationv1.RunsServiceClient
	Schedules   automationv1.SchedulesServiceClient
	Nodes       runtimev1.NodesServiceClient
	Exec        runtimev1.ExecServiceClient
	Workloads   runtimev1.WorkloadsServiceClient
	// Diagnosis 是诊断读面（B2a，zeabur 借鉴设计 §2.5）：DiagnoseApp 单 App
	// 深诊断 + ListProjectFindings 跨 App 聚合（「需要你」页）。
	Diagnosis runtimev1.DiagnosisServiceClient
	Routes    proxyv1.RoutesServiceClient
	Events    telemetryv1.EventsServiceClient
	Logs      telemetryv1.LogsServiceClient
	Metrics   telemetryv1.MetricsServiceClient
	Alerting  telemetryv1.AlertingServiceClient

	Users       identityv1.UsersServiceClient
	Teams       identityv1.TeamsServiceClient
	Roles       identityv1.RolesServiceClient
	Tokens      identityv1.TokensServiceClient
	Invitations identityv1.InvitationsServiceClient
	Audit       identityv1.AuditQueryServiceClient
}

// Options 是 Dial 的可选项累积器。
type options struct {
	dialOptions []grpc.DialOption
}

// Option 是 Dial 选项。
type Option func(*options)

// WithDialOptions 追加原生 grpc.DialOption（bufconn 测试夹具、TLS、拦截
// 器等经此注入）。
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// 地址 scheme 前缀（架构文档 §1 约定）：grpcs:// = TLS（系统根，经
// Proxy/反向代理的公网形态——ADR-0079 API 公网发布面）；grpc:// = 显式
// 明文。其余形态（含无 scheme 的裸 host:port）原样透传 grpc 原生解析，
// 保留 unix:/// 等原生 target。
const (
	addrSchemeTLS      = "grpcs://"
	addrSchemeInsecure = "grpc://"
)

// Dial 建立到 fleetlyd gRPC 端点的连接（惰性建连，失败延迟到首次调用；
// 传入 context 仅用于拨号参数构造）。
//
// addr 形态：grpcs://host:port（TLS）、grpc://host:port（明文）或裸
// host:port（明文——本机/内网直连的现状缺省）。
func Dial(addr string, opts ...Option) (*Client, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	transport := grpc.WithTransportCredentials(insecure.NewCredentials())
	if strings.HasPrefix(addr, addrSchemeTLS) {
		transport = grpc.WithTransportCredentials(credentials.NewTLS(nil))
		addr = strings.TrimPrefix(addr, addrSchemeTLS)
	} else if strings.HasPrefix(addr, addrSchemeInsecure) {
		addr = strings.TrimPrefix(addr, addrSchemeInsecure)
	}
	dialOpts := append([]grpc.DialOption{transport}, o.dialOptions...)
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

// newClient 从已有连接构造类型化客户端（bufconn 夹具）。
func newClient(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:        conn,
		System:      systemv1.NewSystemServiceClient(conn),
		Governance:  systemv1.NewGovernanceServiceClient(conn),
		Platform:    systemv1.NewPlatformServiceClient(conn),
		Projects:    structurev1.NewProjectsServiceClient(conn),
		Apps:        structurev1.NewAppsServiceClient(conn),
		Secrets:     structurev1.NewSecretsServiceClient(conn),
		Configs:     structurev1.NewConfigsServiceClient(conn),
		SharedVars:  structurev1.NewSharedVariablesServiceClient(conn),
		AppVars:     structurev1.NewAppVariablesServiceClient(conn),
		Volumes:     structurev1.NewVolumesServiceClient(conn),
		Networks:    structurev1.NewNetworksServiceClient(conn),
		Databases:   structurev1.NewDatabasesServiceClient(conn),
		Deployments: deliveryv1.NewDeploymentsServiceClient(conn),
		Templates:   deliveryv1.NewTemplatesServiceClient(conn),
		Revisions:   deliveryv1.NewRevisionsServiceClient(conn),
		Builds:      deliveryv1.NewBuildsServiceClient(conn),
		Hooks:       deliveryv1.NewHooksServiceClient(conn),
		GitHubApps:  deliveryv1.NewGitHubAppsServiceClient(conn),
		Registry:    deliveryv1.NewRegistryServiceClient(conn),
		Tasks:       automationv1.NewTasksServiceClient(conn),
		Runs:        automationv1.NewRunsServiceClient(conn),
		Schedules:   automationv1.NewSchedulesServiceClient(conn),
		Nodes:       runtimev1.NewNodesServiceClient(conn),
		Exec:        runtimev1.NewExecServiceClient(conn),
		Workloads:   runtimev1.NewWorkloadsServiceClient(conn),
		Diagnosis:   runtimev1.NewDiagnosisServiceClient(conn),
		Routes:      proxyv1.NewRoutesServiceClient(conn),
		Events:      telemetryv1.NewEventsServiceClient(conn),
		Logs:        telemetryv1.NewLogsServiceClient(conn),
		Metrics:     telemetryv1.NewMetricsServiceClient(conn),
		Alerting:    telemetryv1.NewAlertingServiceClient(conn),

		Users:       identityv1.NewUsersServiceClient(conn),
		Teams:       identityv1.NewTeamsServiceClient(conn),
		Roles:       identityv1.NewRolesServiceClient(conn),
		Tokens:      identityv1.NewTokensServiceClient(conn),
		Invitations: identityv1.NewInvitationsServiceClient(conn),
		Audit:       identityv1.NewAuditQueryServiceClient(conn),
	}
}

// Close 关闭底层连接。
func (c *Client) Close() error { return c.conn.Close() }

// WithToken 为单次调用构造带 Bearer 凭证的 context（Token 体系接入后由
// CLI conn 层统一调用）。
func WithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// WithIdempotencyKey 为创建型调用构造带幂等键的 context（ADR-0024：头
// 形态通用幂等——同键同体重放返回同一响应、同键异体 409、24h 保留）。
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "idempotency-key", key)
}
