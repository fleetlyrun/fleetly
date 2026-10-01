package cmd

// dialFromEnv 是动词的统一拨号入口（解析连接旗标 → SDK 拨号 → ctx 附
// Bearer 凭证与 CLI 自标识头 + 非流式请求的默认 deadline；返回的 cancel
// 释放 deadline 资源（流式豁免形态下是无操作），调用方 defer；bufconn
// golden 夹具经 dialClient 接缝注入）。

import (
	"context"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// cliRequestTimeout 是 CLI 非流式请求的默认 deadline（120s）。选值依据：
// 服务端 unary 硬上限 30s（assembly grpcUnaryTimeout），客户端预算另需
// 覆盖惰性拨号、TLS 握手与交互网络的往返余量，120s 足够宽裕又不至于让
// 挂死的服务无限占用终端。流式动词（logs / builds logs 的 follow 会话）
// 经 noDeadline 豁免——follow 设计上长存活，客户端 deadline 会腰斩尾随流。
const cliRequestTimeout = 120 * time.Second

// dialOptions 是 dialFromEnv 的行为开关集合。
type dialOptions struct {
	noDeadline bool
}

// dialOption 是 dialFromEnv 的可选项。
type dialOption func(*dialOptions)

// noDeadline 豁免请求级 deadline：流式动词专用（服务端流面的生命周期由
// 取消信号管理，客户端 deadline 只会杀掉 follow 会话）。
func noDeadline() dialOption {
	return func(o *dialOptions) { o.noDeadline = true }
}

// resolveToken 按 显式旗标 > FLEETLY_TOKEN env > 凭据文件 归一 token。
func resolveToken(flagToken string) string {
	if flagToken != "" {
		return flagToken
	}
	if t := envOr(envToken, ""); t != "" {
		return t
	}
	_, token, err := readCredentials()
	if err != nil {
		return "" // 凭据文件不可读不阻断连接（匿名诊断面仍可用），错误留给 401 呈现
	}
	return token
}

func dialFromEnv(ctx context.Context, opts ...dialOption) (context.Context, context.CancelFunc, *fleetly.Client, error) {
	var o dialOptions
	for _, opt := range opts {
		opt(&o)
	}
	var cf connFlags
	// 连接旗标已在动词 SetFlags 挂载；addr 缺省从环境取（凭据文件的 addr
	// 不参与：endpoint 只信显式配置——防不可信文件重定向）。
	cf.addr = envOr(envAddr, defaultAddr)
	token := resolveToken(cf.token)
	c, err := dialClient(cf.addr)
	if err != nil {
		return nil, nil, nil, err
	}
	// 非流式默认附请求级 deadline；流式动词豁免时 cancel 返回无操作，
	// 调用方对两种形态统一 defer。
	cancel := context.CancelFunc(func() {})
	if !o.noDeadline {
		ctx, cancel = context.WithTimeout(ctx, cliRequestTimeout)
	}
	// ctx 一次附全：Bearer 凭证 + CLI 自标识（审计来源 api/cli 区分锚）。
	ctx = fleetly.WithToken(ctx, token)
	ctx = metadata.AppendToOutgoingContext(ctx, authn.HeaderClientSource, "cli")
	return ctx, cancel, c, nil
}
