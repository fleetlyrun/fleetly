package main

// 远程命令的公共连接参数（T2.18 CLI-over-SDK 改造）：CLI 只经 SDK（gRPC）
// 消费平台——不再有任何直开 DB / 直连 docker / 直读密钥的路径。--addr 与
// --token 是远程动词的公共 flag（env 覆盖 FLEETLY_ADDR / FLEETLY_TOKEN）；
// token 缺失时不在客户端伪造鉴权表象——照常发起请求，由服务端 401 显式
// 拒绝，CLI 渲染信封并附可行动提示（renderCLIError 的 Unauthenticated
// 分支：bootstrap token 见 <数据根>/bootstrap-token 文件（B5：一次写入、
// 不进日志、首登后删除），或由管理员 tokens create 签发）。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultRPCTimeout 是一元 RPC 的缺省 deadline（S17-D3）：daemon 假死时
// 单次调用 30s 内失败，CLI 不永久挂起。流式 RPC（logs follow / events
// watch）不设此上限——跟随/观看的收口交给信号取消；deploy/build 等待
// 轮询的总预算由动词自身 --timeout 管，每轮单次调用仍受此上限保护。
const defaultRPCTimeout = 30 * time.Second

// defaultUnaryTimeout 是 CLI 全部一元 RPC 的缺省 deadline 拦截器（S17-D3）。
// 分流点选在拨号层而非动词清单：gRPC 面只有「一元 / 流式」两种调用形态，
// 拦截器恰好覆盖全部一元调用、天然不触流式——与 S17-D2 substrate 侧
// 「非流式调用包 WithTimeout、流式保持调用方 ctx」同一模式，无需维护
// 动词豁免清单。
func defaultUnaryTimeout(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	rpcCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return invoker(rpcCtx, method, req, reply, cc, opts...)
}

// isCleanCancel 判定 err 是否「调用方主动取消」的干净收尾（S17-D3）：
// 根 ctx 已 Canceled（Ctrl-C/SIGTERM 信号或父取消），且 err 是
// context.Canceled 本尊或其 gRPC 投影（codes.Canceled——gRPC 的
// FromContextError 不携带 cause，errors.Is 单判不可靠，需 code 双判）。
// 流式动词与轮询等待循环据此判 exit 0；DeadlineExceeded（超时）与流上
// 其他错误（服务端关闭/网络断——此时本方 ctx 未取消）不在此列。
func isCleanCancel(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() != context.Canceled {
		return false
	}
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

// extraDialOptions 是测试注入点（bufconn 拨号——golden 测试在进程内起
// 真实 api 服务）；生产恒空，不参与装配。
var extraDialOptions []grpc.DialOption

// fleetlyClient 是 SDK 客户端的包内别名（命令签名 `*fleetlyClient` 读感
// 统一；类型别名——与 *fleetly.Client 完全同型）。
type fleetlyClient = fleetly.Client

// timestampProto 是 proto Timestamp 的包内别名（nil = 字段未发生）。
type timestampProto = timestamppb.Timestamp

// connFlags 是远程动词内嵌的连接参数集（各动词 SetFlags 统一注册）。
type connFlags struct {
	addr  string
	token string
}

// register 把 --addr/--token 挂进动词 flag 集（--addr 缺省回落环境变量
// ——CI/脚本形态无需逐命令传参）。--token 的默认值必须保持空串（H1）：
// std flag 的 -h/help 会把非空默认值明文打进 stdout，帮助输出常被贴进
// 工单/CI 日志/AI 会话——env 回落挪到消费点 dial() 里做，-h 面永不出现
// token 本体。
func (f *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.addr, "addr", envOrDefault("FLEETLY_ADDR", fleetly.DefaultAddr), "fleetlyd gRPC address")
	fs.StringVar(&f.token, "token", "", "API token (env: FLEETLY_TOKEN; bootstrap token: see <data-root>/bootstrap-token)")
}

// dial 建立 SDK 客户端（连接惰性建立；Close 交还调用方）。一元 RPC 的
// 缺省 deadline 拦截器随连接挂载（S17-D3）。token 在此消费点回落
// FLEETLY_TOKEN（H1：flag 默认值置空防 -h 回显，env 语义不变）。
func (f *connFlags) dial() (*fleetly.Client, error) {
	token := f.token
	if token == "" {
		token = os.Getenv("FLEETLY_TOKEN")
	}
	dialOpts := append([]grpc.DialOption{grpc.WithUnaryInterceptor(defaultUnaryTimeout)}, extraDialOptions...)
	opts := []fleetly.Option{fleetly.WithAddr(f.addr), fleetly.WithDialOptions(dialOpts...)}
	if token != "" {
		opts = append(opts, fleetly.WithToken(token))
	}
	return fleetly.NewClient(opts...)
}

// withClient 是远程动词的执行骨架：拨号 → 执行 → 释放。
func (f *connFlags) withClient(fn func(*fleetlyClient) error) error {
	c, err := f.dial()
	if err != nil {
		return fmt.Errorf("connect fleetlyd %s: %w", f.addr, err)
	}
	defer func() { _ = c.Close() }()
	return fn(c)
}

// envOrDefault 取环境变量（空值回落缺省）。
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// osReadFile 读本地文件（位置参数路径的统一错误文案；路径来自操作者
// 位置参数——CLI 的本职就是读指定文件，不构成路径注入面）。
func osReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304：路径为操作者显式位置参数
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// int32Clamp 把 int 收窄为 int32（proto 的 limit 字段天花板 ≤1000——
// 饱和收窄即安全，负值由 flag 缺省保证不出现）。
func int32Clamp(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	default:
		return int32(n)
	}
}
