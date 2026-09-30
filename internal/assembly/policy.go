package assembly

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lynx-go/grpcapi/authz"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// authzFiles 是 authz 策略收集的业务 proto 清单（单一事实源；新增服务
// proto 只登记此处，NewGRPCServer 的 fail-closed 断言经 PolicySet 覆盖
// 同一集合）。
func authzFiles() []protoreflect.FileDescriptor {
	return []protoreflect.FileDescriptor{
		systemv1.File_fleetly_system_v1_system_proto,
		structurev1.File_fleetly_structure_v1_structure_proto,
		deliveryv1.File_fleetly_delivery_v1_delivery_proto,
		runtimev1.File_fleetly_runtime_v1_runtime_proto,
		edgev1.File_fleetly_edge_v1_edge_proto,
		telemetryv1.File_fleetly_telemetry_v1_telemetry_proto,
		identityv1.File_fleetly_identity_v1_identity_proto,
	}
}

// scopeResources 是 Scope 词表（CONTEXT.md Scope 词条：resource:action，
// write 蕴含 read）。注解先行、执法随账号批（F0.5~F0.7）接管——词表现在
// 登记保证 authz.Build 对 scope 声明的资源校验即刻生效（fail-closed）。
func scopeResources() []string {
	return []string{
		"projects", "apps", "secrets", "configs", "volumes", "networks",
		"deployments", "revisions", "builds", "nodes", "routes", "events", "logs",
		"users", "teams", "roles", "tokens", "invitations", "audit",
	}
}

// NewPolicySet 从 proto 注解收集全量方法授权策略（authz.Build；流式面
// logs/events 显式放行——拦截器按流首元数据执法，随账号批接入）。
func NewPolicySet() (*authz.PolicySet, error) {
	return authz.Build(authzFiles(), authz.Options{
		Vocabulary:     authz.Vocabulary{ScopeResources: scopeResources()},
		AllowStreaming: true,
	})
}
