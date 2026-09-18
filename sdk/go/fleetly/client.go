// Package fleetly 是 fleetly 平台的 Go SDK：对 fleetlyd gRPC 面的客户端
// 封装（CLI/集成方共用，契约来自 proto 生成物 genproto——proto 唯一真源，
// D21）。覆盖 v0.1 全部 12 个服务面（system/apps/deployments/revisions/
// builds/drift/domains/env/logs/events/placement/tokens）；服务方法随
// proto 模块扩展同步添加，不在 SDK 层发明契约外语义。
package fleetly

import (
	"context"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DefaultAddr 是 SDK 默认连接地址（与 fleetlyd gRPC 面缺省监听一致，
// 见 cmd/fleetlyd/config.go 的 defaultGRPCAddr）。
const DefaultAddr = "127.0.0.1:8421"

// Option 修改 Client 配置。
type Option func(*clientConfig)

type clientConfig struct {
	addr        string
	token       string
	dialOptions []grpc.DialOption
}

// WithAddr 覆盖 gRPC 目标地址（默认 127.0.0.1:8421）。
func WithAddr(addr string) Option { return func(c *clientConfig) { c.addr = addr } }

// WithToken 设置 Bearer token：经 PerRPCCredentials 挂进每个请求的
// authorization metadata（含流式首帧）。token 只进请求 metadata，不进
// SDK 侧任何日志/错误文本（凭据不外溢纪律）。
func WithToken(token string) Option { return func(c *clientConfig) { c.token = token } }

// WithDialOptions 附加底层 gRPC 拨号选项（如 TLS 凭据、消息大小上限）。
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(c *clientConfig) { c.dialOptions = append(c.dialOptions, opts...) }
}

// Client 是 fleetlyd gRPC 面的客户端封装：单连接复用，goroutine 安全。
// 各服务面经同名访问器取用（返回 proto 生成客户端——方法契约即 proto）。
type Client struct {
	conn   *grpc.ClientConn
	system serverv1.SystemServiceClient
	apps   serverv1.AppsServiceClient
	deploy serverv1.DeploymentsServiceClient
	revs   serverv1.RevisionsServiceClient
	builds serverv1.BuildsServiceClient
	drift  serverv1.DriftServiceClient
	doms   serverv1.DomainsServiceClient
	env    serverv1.EnvServiceClient
	logs   serverv1.LogsServiceClient
	events serverv1.EventsServiceClient
	place  serverv1.PlacementServiceClient
	tokens serverv1.TokensServiceClient
}

// NewClient 建立 gRPC 连接（默认 127.0.0.1:8421，明文；连接惰性建立，
// TLS 随鉴权阶段〔D21 拦截器链〕引入）。调用方负责 Close。
func NewClient(opts ...Option) (*Client, error) {
	cfg := clientConfig{addr: DefaultAddr}
	for _, opt := range opts {
		opt(&cfg)
	}
	dialOpts := append(
		[]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		cfg.dialOptions...,
	)
	// token 非空才挂凭据——空 token 让服务端以 401 显式拒绝（信封退化
	// 形态），客户端侧不伪造「已鉴权」表象。
	if cfg.token != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerCredentials(cfg.token)))
	}
	conn, err := grpc.NewClient(cfg.addr, dialOpts...)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:   conn,
		system: serverv1.NewSystemServiceClient(conn),
		apps:   serverv1.NewAppsServiceClient(conn),
		deploy: serverv1.NewDeploymentsServiceClient(conn),
		revs:   serverv1.NewRevisionsServiceClient(conn),
		builds: serverv1.NewBuildsServiceClient(conn),
		drift:  serverv1.NewDriftServiceClient(conn),
		doms:   serverv1.NewDomainsServiceClient(conn),
		env:    serverv1.NewEnvServiceClient(conn),
		logs:   serverv1.NewLogsServiceClient(conn),
		events: serverv1.NewEventsServiceClient(conn),
		place:  serverv1.NewPlacementServiceClient(conn),
		tokens: serverv1.NewTokensServiceClient(conn),
	}, nil
}

// bearerCredentials 是 Bearer token 的 PerRPCCredentials 实现：每个请求
// （一元与流式）自动携带 authorization metadata。
type bearerCredentials string

func (b bearerCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}

// RequireTransportSecurity 报告凭据是否要求 TLS：false——v0.1 控制面缺省
// 明文回环（fleetlyd 默认只绑 127.0.0.1），TLS 随鉴权阶段引入后由调用方
// 经 WithDialOptions 切换。
func (b bearerCredentials) RequireTransportSecurity() bool { return false }

// System 取系统/集群观察面（Ping/Status/Nodes/Ingress）。
func (c *Client) System() serverv1.SystemServiceClient { return c.system }

// Apps 取应用资源面。
func (c *Client) Apps() serverv1.AppsServiceClient { return c.apps }

// Deployments 取部署资源面。
func (c *Client) Deployments() serverv1.DeploymentsServiceClient { return c.deploy }

// Revisions 取版本快照只读面。
func (c *Client) Revisions() serverv1.RevisionsServiceClient { return c.revs }

// Builds 取构建资源面。
func (c *Client) Builds() serverv1.BuildsServiceClient { return c.builds }

// Drift 取运行域漂移面。
func (c *Client) Drift() serverv1.DriftServiceClient { return c.drift }

// Domains 取域名台账/验证面。
func (c *Client) Domains() serverv1.DomainsServiceClient { return c.doms }

// Env 取平台 env 面。
func (c *Client) Env() serverv1.EnvServiceClient { return c.env }

// Logs 取应用日志面。
func (c *Client) Logs() serverv1.LogsServiceClient { return c.logs }

// Events 取事件流面。
func (c *Client) Events() serverv1.EventsServiceClient { return c.events }

// Placement 取放置绑定只读面。
func (c *Client) Placement() serverv1.PlacementServiceClient { return c.place }

// Tokens 取 token 管理面（admin scope）。
func (c *Client) Tokens() serverv1.TokensServiceClient { return c.tokens }

// Ping 探测控制面存活并取回 service / version（豁免鉴权——装面前的
// 存活检查路径）。
func (c *Client) Ping(ctx context.Context) (*serverv1.PingResponse, error) {
	return c.system.Ping(ctx, &serverv1.PingRequest{})
}

// FollowLogs 跟随应用日志流：逐帧回调直至流结束（服务端关闭）或 ctx 取消
// ——断线重连 = 重新调用（Follow 无跨断线游标，回放窗口由服务端 ring
// 承载）。返回值携带流的最终状态（ctx 取消返回 ctx.Err）。
func (c *Client) FollowLogs(ctx context.Context, app, service string, fn func(*serverv1.FollowLogsResponse) error) error {
	stream, err := c.logs.FollowLogs(ctx, &serverv1.FollowLogsRequest{App: app, Service: service})
	if err != nil {
		return err
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err // io.EOF = 服务端正常关闭
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

// WatchEvents 消费事件流：逐帧回调直至流结束或 ctx 取消。since_seq=0 =
// 从保留窗起点；游标早于保留窗时流上会先收到一帧 cursor_expired 信封帧
// 后正常关闭（消费方以 oldest_seq 重新拉全量——events.proto 取舍注记）。
func (c *Client) WatchEvents(ctx context.Context, sinceSeq int64, fn func(*serverv1.WatchEventsResponse) error) error {
	stream, err := c.events.WatchEvents(ctx, &serverv1.WatchEventsRequest{SinceSeq: sinceSeq})
	if err != nil {
		return err
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err // io.EOF = 服务端正常关闭
		}
		if err := fn(frame); err != nil {
			return err
		}
	}
}

// Close 释放底层连接。
func (c *Client) Close() error { return c.conn.Close() }
