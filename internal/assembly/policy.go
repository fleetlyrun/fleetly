package assembly

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lynx-go/grpcapi/authz"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
)

// authzFiles 是 authz 策略收集的业务 proto 清单（单一事实源；新增服务
// proto 只登记此处，NewGRPCServer 的 fail-closed 断言经 PolicySet 覆盖
// 同一集合）。
func authzFiles() []protoreflect.FileDescriptor {
	return []protoreflect.FileDescriptor{
		systemv1.File_fleetly_system_v1_system_proto,
	}
}

// NewPolicySet 从 proto 注解收集全量方法授权策略（authz.Build）。
//
// Vocabulary 登记 fleetly 的 scope 资源词表（`resource:action`，write 蕴含
// read，见 CONTEXT.md Scope 词条）；SERVER 面方法声明 api_key_scope 时
// 资源必须在词表内，违例启动即红（fail-closed）。账号体系批次
// （F0.5~F0.7）引入首个 scoped 方法时在此登记词表；当前 N0 骨架只有
// public 面方法，词表为空是合法态（authz.Build 内置断言：存在 scope
// 声明而词表为空才报错）。
func NewPolicySet() (*authz.PolicySet, error) {
	return authz.Build(authzFiles(), authz.Options{
		Vocabulary: authz.Vocabulary{
			// ScopeResources 随首个 SERVER 面方法登记（accounts 批次）。
		},
	})
}
