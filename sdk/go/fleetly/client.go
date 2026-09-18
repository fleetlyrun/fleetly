// Package fleetly 是 fleetly 平台的 Go SDK：对 fleetlyd gRPC 面的
// 最小客户端封装（CLI/集成方共用，契约来自 proto 生成物 genproto）。
// v0 阶段仅暴露 SystemService.Ping；随 proto 模块扩展逐个添加服务面。
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
	dialOptions []grpc.DialOption
}

// WithAddr 覆盖 gRPC 目标地址（默认 127.0.0.1:8421）。
func WithAddr(addr string) Option { return func(c *clientConfig) { c.addr = addr } }

// WithDialOptions 附加底层 gRPC 拨号选项（如 TLS 凭据、消息大小上限）。
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(c *clientConfig) { c.dialOptions = append(c.dialOptions, opts...) }
}

// Client 是 fleetlyd gRPC 面的客户端封装：单连接复用，goroutine 安全。
type Client struct {
	conn   *grpc.ClientConn
	system serverv1.SystemServiceClient
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
	conn, err := grpc.NewClient(cfg.addr, dialOpts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, system: serverv1.NewSystemServiceClient(conn)}, nil
}

// Ping 探测控制面存活并取回 service / version。
func (c *Client) Ping(ctx context.Context) (*serverv1.PingResponse, error) {
	return c.system.Ping(ctx, &serverv1.PingRequest{})
}

// Close 释放底层连接。
func (c *Client) Close() error { return c.conn.Close() }
