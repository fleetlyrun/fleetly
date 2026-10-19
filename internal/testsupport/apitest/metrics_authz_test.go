package apitest_test

// H8 阶段 A 验收（设计文档 docs/design/2026-10-19-security-backlog-design.md
// §5.2）：raw QueryMetrics 收归平台管理员诊断面。
//   - 非平台身份（他队 builtin-admin、default Team 的 builtin-admin、普通
//     member）一律 E_FORBIDDEN；夹具未配置 metrics.addr，仍必须是 403——
//     证明门先于 Metrics nil 的 503，调用方无法用状态差异探测存储配置。
//   - 平台 owner（bootstrap token）越过门，落到门后的存储状态分支（本机
//     应为 503；断言"不是 PermissionDenied"即可，避免环境依赖）。
//
// 身份链全程走产品面（CreateUser/Login），不直插 token 表造特权 Token。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// TestQueryMetricsRequiresPlatformAdmin 钉 H8 阶段 A 的平台门：非平台
// 管理员一律 403（门在 store 状态之前），平台 owner 不在 403 档。
func TestQueryMetricsRequiresPlatformAdmin(t *testing.T) {
	h := apitest.New(t)
	metrics := telemetryv1.NewMetricsServiceClient(h.Conn)

	// 他队 builtin-admin：角色 admin 也不是平台管理员（平台管理员 =
	// default Team 的 builtin-owner）。
	_, teamAdmin := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")
	// default Team 的 builtin-admin：与 owner 同队同资源面，仍不是平台
	// 管理员（判定是 role+team 双条件，不是 role 单条件）。
	createUserAs(t, h, "default-admin", identity.DefaultTeamID, identity.RoleAdminID, "default-admin-pass")
	defaultAdmin, who := loginSession(t, h, "default-admin", "default-admin-pass")
	require.Equal(t, identity.RoleAdminID, who.GetRoleId())
	// default Team 的普通 member：metrics:read 的 scope 门放行，仍须被本
	// 门拦下（scope 答"能不能用这个面"，平台门答"能不能用 raw 面"）。
	createUserAs(t, h, "default-member", identity.DefaultTeamID, identity.RoleMemberID, "default-member-pass")
	member, who := loginSession(t, h, "default-member", "default-member-pass")
	require.Equal(t, identity.RoleMemberID, who.GetRoleId())

	for name, ctx := range map[string]context.Context{
		"other team builtin-admin":   teamAdmin,
		"default team builtin-admin": defaultAdmin,
		"default team member":        member,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := metrics.QueryMetrics(ctx, &telemetryv1.QueryMetricsRequest{Query: "up"})
			// 夹具 Metrics 为 nil（未配置 metrics.addr）：非管理员仍必须是
			// 403 而不是 503——门先于存储状态，拒绝零副作用。
			wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
		})
	}

	// 平台 owner（bootstrap token）过门：本机未配 metrics.addr 时应落精确
	// E_METRICS_STORE_UNAVAILABLE；只钉"不是 PermissionDenied"，不把测试
	// 绑死在当前夹具的 store 配置上。
	_, err := metrics.QueryMetrics(h.Ctx(), &telemetryv1.QueryMetricsRequest{Query: "up"})
	require.Error(t, err)
	assert.NotEqual(t, codes.PermissionDenied, status.Code(err), "platform owner must pass the raw metrics gate")
	if status.Code(err) == codes.Unavailable {
		env, ok := apperrstatus.FromError(err)
		require.True(t, ok)
		assert.Equal(t, "E_METRICS_STORE_UNAVAILABLE", env.Code())
	}
}
