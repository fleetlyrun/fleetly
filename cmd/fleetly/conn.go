package main

// 远程命令的公共连接参数（T2.18 CLI-over-SDK 改造）：CLI 只经 SDK（gRPC）
// 消费平台——不再有任何直开 DB / 直连 docker / 直读密钥的路径。--addr 与
// --token 是远程动词的公共 flag（env 覆盖 FLEETLY_ADDR / FLEETLY_TOKEN）；
// token 缺失时不在客户端伪造鉴权表象——照常发起请求，由服务端 401 显式
// 拒绝，CLI 渲染信封并附可行动提示（renderCLIError 的 Unauthenticated
// 分支：bootstrap token 见 fleetlyd 首启日志，或由管理员 tokens create 签发）。

import (
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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

// register 把 --addr/--token 挂进动词 flag 集（缺省回落环境变量——CI/
// 脚本形态无需逐命令传参）。
func (f *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.addr, "addr", envOrDefault("FLEETLY_ADDR", fleetly.DefaultAddr), "fleetlyd gRPC address")
	fs.StringVar(&f.token, "token", os.Getenv("FLEETLY_TOKEN"), "API token (bootstrap token: fleetlyd first-start log; or fleetly tokens create)")
}

// dial 建立 SDK 客户端（连接惰性建立；Close 交还调用方）。
func (f *connFlags) dial() (*fleetly.Client, error) {
	opts := []fleetly.Option{fleetly.WithAddr(f.addr), fleetly.WithDialOptions(extraDialOptions...)}
	if f.token != "" {
		opts = append(opts, fleetly.WithToken(f.token))
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
