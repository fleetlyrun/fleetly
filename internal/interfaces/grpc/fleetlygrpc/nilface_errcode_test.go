package fleetlygrpc

// nil 面错误码律的单测断言（P4-3 批 3「未配置」族码律收尾）：cipher-nil →
// E_MASTER_KEY_UNAVAILABLE（503 族，显式关态）、runtime 可选子面缺席 →
// E_RUNTIME_FACE_UNSUPPORTED（501 族，honest failure）。apitest 夹具恒装
// 真 cipher 与全面 FakeRuntime，这两个 nil 形态在 bufconn 面不可达——
// 单测以最小 Services 构造（delivery_test.go testVarServices 先例）直达
// 分支；REST 503/501 的映射断言由 assembly errcodeToHTTP 全量覆盖守卫与
// metrics/logs 两码的 apitest 同批承载。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// apperrOf 断言 err 是 *apperr.Error 并原样返回（nil 面分支恒产信封错误）。
func apperrOf(t *testing.T, err error) *apperr.Error {
	t.Helper()
	require.Error(t, err)
	ae, ok := err.(*apperr.Error)
	require.True(t, ok, "nil-face branches must produce an apperr envelope, got %T", err)
	return ae
}

// TestCipherNilRefusedPrecise 钉 cipher 面缺席的精确拒绝（两代表点位：
// PutSecret 密面第一守卫、CreateNotificationChannel 形校后守卫）——
// E_MASTER_KEY_UNAVAILABLE / Unavailable，文案面向用户（off 态非内部故障）。
func TestCipherNilRefusedPrecise(t *testing.T) {
	ctx := context.Background()

	// PutSecret：cipher 检查是受理链第一守卫（行级授权在其后），零依赖直达。
	_, err := (&SecretsService{s: &Services{}}).PutSecret(ctx, &structurev1.PutSecretRequest{
		ProjectId: "01JD0PROJ00000000000000000", Name: "tls_cert", Value: "v",
	})
	ae := apperrOf(t, err)
	assert.Equal(t, "E_MASTER_KEY_UNAVAILABLE", ae.Code())
	st := apperrstatus.ToGRPCStatus(ae)
	assert.Equal(t, "Unavailable", st.Code().String())
	assert.Contains(t, st.Message(), "no master key", "off-state message is user-facing by design")
	assert.NotEmpty(t, ae.Suggestion())

	// CreateNotificationChannel：kind 形校先过（webhook 带 url），随后
	// cipher 守卫精确拒绝——通道配置 age 信封没有明文回退路径。
	_, err = (&AlertingService{s: &Services{}}).CreateNotificationChannel(ctx,
		&telemetryv1.CreateNotificationChannelRequest{Name: "ops", Kind: "webhook", Url: "http://127.0.0.1:9/x"})
	ae = apperrOf(t, err)
	assert.Equal(t, "E_MASTER_KEY_UNAVAILABLE", ae.Code())
	assert.Equal(t, "Unavailable", apperrstatus.ToGRPCStatus(ae).Code().String())
}

// TestRuntimeFaceAbsentRefusedPrecise 钉 runtime 可选子面缺席的精确拒绝
// （三代表点位：nodeAdmin 节点管理子面、streamLogsDualPath 容器日志子面、
// ListNodeContainers 节点观测子面）——E_RUNTIME_FACE_UNSUPPORTED /
// Unimplemented，honest failure 无降级路径。
func TestRuntimeFaceAbsentRefusedPrecise(t *testing.T) {
	// Runtime 零值（FacesOf 全空）——nil 接口经 FacesOf 协商即全缺席。
	// S2 起 nodeAdmin 的平台管理员门先于子面协商，故注入平台 owner 身份
	// 直达 501 分支（handler 级单测不经拦截器）。
	platformCtx := authn.WithIdentity(context.Background(), &authn.Identity{
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID,
	})
	svc := &NodesService{s: &Services{}}
	err := svc.nodeAdmin(platformCtx, "01JD0NODE00000000000000000", "node.drain",
		func(capability.RuntimeAdmin, string) error { return nil })
	ae := apperrOf(t, err)
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "does not expose node administration")
	assert.NotEmpty(t, ae.Suggestion())

	logs := &LogsService{s: &Services{}}
	// 流面传 nil：子面缺席分支在首次写帧前返回，stream 永不触达。
	ae = apperrOf(t, logs.streamLogsDualPath(capability.LogQuery{}, &telemetryv1.StreamLogsRequest{}, nil))
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "does not expose container logs")

	// ListNodeContainers 自 S16-c7 起与读面同门（平台管理员门先于子面协商；
	// R08-M1）——注入平台 owner 身份直达 501 分支。
	_, err = svc.ListNodeContainers(platformCtx,
		&runtimev1.ListNodeContainersRequest{NodeId: "01JD0NODE00000000000000000"})
	ae = apperrOf(t, err)
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "does not expose node inspection")
}
