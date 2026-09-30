package cmd

import (
	"context"
	"fmt"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// newStatusCmd 查询控制面服务状态（远程命令；`fleetly doctor` 随安装
// 引导批次消费同一面并叠加本地诊断）。
func newStatusCmd() commands.Command {
	const name = "status"
	var conn connFlags
	return &flaggedVerb{
		name:     name,
		synopsis: "query the fleetlyd control plane status",
		usage:    "status",
		setFlags: conn.register,
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			client, err := conn.dial()
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck // 进程退出路径

			callCtx, cancel := context.WithTimeout(ctx, fleetly.DefaultTimeout)
			defer cancel()
			resp, err := client.System.GetStatus(fleetly.WithToken(callCtx, conn.token), nil)
			if err != nil {
				return fmt.Errorf("query fleetlyd status at %s: %w", conn.addr, err)
			}
			if jsonOut {
				return writeProtoJSON(env.Stdout, resp)
			}
			_, err = fmt.Fprintf(env.Stdout, "%s  server=%s\n", statusStateText(resp.GetState()), serverVersionOrUnknown(resp.GetVersion()))
			return err
		},
	}
}

// statusStateText 把枚举渲染为小写人读形态（healthy/degraded）。
func statusStateText(s systemv1.StatusState) string {
	switch s {
	case systemv1.StatusState_STATUS_STATE_HEALTHY:
		return "healthy"
	case systemv1.StatusState_STATUS_STATE_DEGRADED:
		return "degraded"
	default:
		return "unknown"
	}
}

// serverVersionOrUnknown 服务端未注入版本时（go run 开发形态）诚实显示。
func serverVersionOrUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
