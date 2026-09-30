package cmd

// dialFromEnv 是动词的统一拨号入口（解析连接旗标 → SDK 拨号；bufconn
// golden 夹具经 dialClient 接缝注入）。

import (
	"context"

	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func dialFromEnv(ctx context.Context) (*fleetly.Client, error) {
	var cf connFlags
	// 连接旗标已在动词 SetFlags 挂载；resolve 从环境取缺省。
	cf.addr = envOr(envAddr, defaultAddr)
	cf.token = envOr(envToken, "")
	return dialClient(cf.addr)
}
