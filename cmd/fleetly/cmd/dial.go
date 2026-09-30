package cmd

// dialFromEnv 是动词的统一拨号入口（解析连接旗标 → SDK 拨号 → ctx 附
// Bearer 凭证与 CLI 自标识头；bufconn golden 夹具经 dialClient 接缝注入）。

import (
	"context"

	"google.golang.org/grpc/metadata"

	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

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

func dialFromEnv(ctx context.Context) (context.Context, *fleetly.Client, error) {
	var cf connFlags
	// 连接旗标已在动词 SetFlags 挂载；addr 缺省从环境取（凭据文件的 addr
	// 不参与：endpoint 只信显式配置——防不可信文件重定向）。
	cf.addr = envOr(envAddr, defaultAddr)
	token := resolveToken(cf.token)
	c, err := dialClient(cf.addr)
	if err != nil {
		return nil, nil, err
	}
	// ctx 一次附全：Bearer 凭证 + CLI 自标识（审计来源 api/cli 区分锚）。
	ctx = fleetly.WithToken(ctx, token)
	ctx = metadata.AppendToOutgoingContext(ctx, authn.HeaderClientSource, "cli")
	return ctx, c, nil
}
