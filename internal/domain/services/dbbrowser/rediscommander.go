package dbbrowser

import (
	"fmt"
)

// redisCommanderTemplate 承接 redis（ADR-0051 决策 3；方言实证 2026-10-07：
// ghcr.io/joeferner/redis-commander:0.9.1）：
//
//   - 凭证注入 = env REDIS_HOSTS=label:host:port:dbIndex:password——**密码
//     在 env**（方言边界：工具只吃 env 连接串，ADR-0051 后果节记档；平台
//     侧密码仍不出 Secret 单真源，但落在 inspect 可见面，比文件面弱一档）；
//   - 只读 = env READ_ONLY=true——工具 HTTP API 层执法（变更路由 403
//     中间件 + CLI 白名单，源码核对），非 redis 服务端；
//   - 端口 = env PORT（custom-environment-variables 映射 server.port）。
type redisCommanderTemplate struct{}

const (
	redisCommanderTag    = "ghcr.io/joeferner/redis-commander:0.9.1"
	redisCommanderDigest = "sha256:2c2404630820613c7e540ec26a8d1dd24b671d1a80373c3dfad287c7a1374ad1"
)

func (redisCommanderTemplate) Name() string                     { return "redis-commander" }
func (redisCommanderTemplate) Image() string                    { return redisCommanderTag + "@" + redisCommanderDigest }
func (redisCommanderTemplate) ImageDigest() string              { return redisCommanderDigest }
func (redisCommanderTemplate) Port() int32                      { return 8080 }
func (redisCommanderTemplate) ReadOnlyEnforcement() Enforcement { return EnforcementTool }

func (t redisCommanderTemplate) Render(conn Conn, readonly bool) (Rendering, error) {
	if err := validateConn(conn); err != nil {
		return Rendering{}, err
	}
	env := map[string]string{
		// label:host:port:dbIndex:password（入口脚本 sed 逐字段拆——密码
		// 域 [0-9A-Za-z] 无冒号转义面）。
		"REDIS_HOSTS": fmt.Sprintf("db:%s:%d:0:%s", conn.Host, conn.Port, conn.Password),
		"PORT":        "8080",
	}
	if readonly {
		env["READ_ONLY"] = "true"
	}
	return Rendering{Env: env}, nil
}
