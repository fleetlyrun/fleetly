package cmd

// dialFromEnv 是动词的统一拨号入口（解析连接旗标 → SDK 拨号 → ctx 附
// Bearer 凭证与 CLI 自标识头；bufconn golden 夹具经 dialClient 接缝注入）。

import (
	"context"

	"google.golang.org/grpc/metadata"

	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func dialFromEnv(ctx context.Context) (context.Context, *fleetly.Client, error) {
	var cf connFlags
	// 连接旗标已在动词 SetFlags 挂载；resolve 从环境取缺省。
	cf.addr = envOr(envAddr, defaultAddr)
	cf.token = envOr(envToken, "")
	c, err := dialClient(cf.addr)
	if err != nil {
		return nil, nil, err
	}
	// ctx 一次附全：Bearer 凭证（token 解析序 flag > env > 凭据文件，随
	// login 批接入）+ CLI 自标识（审计来源 api/cli 区分的唯一锚）。
	ctx = fleetly.WithToken(ctx, cf.token)
	ctx = metadata.AppendToOutgoingContext(ctx, authn.HeaderClientSource, "cli")
	return ctx, c, nil
}
