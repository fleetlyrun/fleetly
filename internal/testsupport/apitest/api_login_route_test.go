package apitest_test

// API 公网发布登录路径漂移守卫（ADR-0079 验收锚）：engine.APILoginMethodPath
// 是 identity.proto UsersService.Login 的 gRPC method 路径同源物化——公网
// API 路由（ADR-0079）按此常量铸登录限速路由（Host && PathPrefix）。
// proto 改包名/服务名/方法名而常量未同批改的漂移 = 限速路由指空路径
//（gRPC 登录端点公网无限速裸奔——密码形态经 Users.Login 可达）。本守卫
// 把常量锚到生成码的 ServiceDesc 与 Login method 描述符：proto 任何一侧
// 改名即红。
//
// 与 console_login_route_test.go 成对：REST 面（/v1/auth/login 注解）与
// gRPC 面（method 路径）是同一后端方法的两个公网入口，两侧限速路由
// 必须同批漂移。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
)

func TestAPILoginMethodPathAnchoredToProto(t *testing.T) {
	assert.Equal(t, "/fleetly.identity.v1.UsersService/Login", engine.APILoginMethodPath)

	// 锚：常量必须等于生成码 ServiceDesc 的服务全名 + Login 方法名——
	// proto 侧改包名/服务名（ServiceName 变）或方法名（Methods 集变）
	// 都会让字面值断言或本锚红，双向钉死。
	require.Equal(t, "fleetly.identity.v1.UsersService", identityv1.UsersService_ServiceDesc.ServiceName)

	var loginFound bool
	for _, m := range identityv1.UsersService_ServiceDesc.Methods {
		if m.MethodName == "Login" {
			loginFound = true
		}
	}
	require.True(t, loginFound, "identity.proto UsersService 必须保有 Login 方法——丢失即 API 公网登录限速路由失去目标")
	assert.Equal(t, "/"+identityv1.UsersService_ServiceDesc.ServiceName+"/Login", engine.APILoginMethodPath)
}
