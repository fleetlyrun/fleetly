// Package fleetly 是 fleetly 平台的类型化 gRPC 客户端 SDK。CLI-over-SDK
// 是 cmd/fleetly 消费平台的唯一通道；外部集成方同样经本 SDK 连接
// fleetlyd（gRPC 或经 gateway 的 REST 面）。
//
// 凭证：Authorization: Bearer <token>（Token 体系随账号批次接入；当前
// 无鉴权方法为 public 面）。
package fleetly

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// DefaultTimeout 是单次 unary 调用的默认时长期望（调用方经 context 覆盖）。
const DefaultTimeout = 30 * time.Second

// Client 是平台的类型化入口；零值不可用，经 Dial 构造。
type Client struct {
	conn   *grpc.ClientConn
	System systemv1.SystemServiceClient

	Projects    structurev1.ProjectsServiceClient
	Apps        structurev1.AppsServiceClient
	Secrets     structurev1.SecretsServiceClient
	Configs     structurev1.ConfigsServiceClient
	Volumes     structurev1.VolumesServiceClient
	Networks    structurev1.NetworksServiceClient
	Deployments deliveryv1.DeploymentsServiceClient
	Revisions   deliveryv1.RevisionsServiceClient
	Builds      deliveryv1.BuildsServiceClient
	Hooks       deliveryv1.HooksServiceClient
	Nodes       runtimev1.NodesServiceClient
	Routes      edgev1.RoutesServiceClient
	Events      telemetryv1.EventsServiceClient
	Logs        telemetryv1.LogsServiceClient

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

// Dial 建立到 fleetlyd gRPC 端点的连接（惰性建连，失败延迟到首次调用；
// 传入 context 仅用于拨号参数构造）。
func Dial(addr string, opts ...Option) (*Client, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	// 默认 insecure：fleetlyd gRPC 面设计为本机/内网直连；对外暴露经
	// Edge TLS 或反向代理。TLS 形态随证书体系批次提供 WithDialOptions
	// 注入（grpc:// 与 grpcs:// scheme，架构文档 §1）。
	dialOpts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, o.dialOptions...)
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
		Projects:    structurev1.NewProjectsServiceClient(conn),
		Apps:        structurev1.NewAppsServiceClient(conn),
		Secrets:     structurev1.NewSecretsServiceClient(conn),
		Configs:     structurev1.NewConfigsServiceClient(conn),
		Volumes:     structurev1.NewVolumesServiceClient(conn),
		Networks:    structurev1.NewNetworksServiceClient(conn),
		Deployments: deliveryv1.NewDeploymentsServiceClient(conn),
		Revisions:   deliveryv1.NewRevisionsServiceClient(conn),
		Builds:      deliveryv1.NewBuildsServiceClient(conn),
		Hooks:       deliveryv1.NewHooksServiceClient(conn),
		Nodes:       runtimev1.NewNodesServiceClient(conn),
		Routes:      edgev1.NewRoutesServiceClient(conn),
		Events:      telemetryv1.NewEventsServiceClient(conn),
		Logs:        telemetryv1.NewLogsServiceClient(conn),

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
