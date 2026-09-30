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

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
)

// DefaultTimeout 是单次 unary 调用的默认时长期望（调用方经 context 覆盖）。
const DefaultTimeout = 30 * time.Second

// Client 是平台的类型化入口；零值不可用，经 Dial 构造。
type Client struct {
	conn   *grpc.ClientConn
	System systemv1.SystemServiceClient
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
	return &Client{
		conn:   conn,
		System: systemv1.NewSystemServiceClient(conn),
	}, nil
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
