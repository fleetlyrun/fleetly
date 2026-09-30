package apitest_test

// F0.6 验收矩阵：scope 执法（member/admin/bootstrap 权限差异、匿名 401、
// 吊销后下一个调用即 401）。Token 经 repo 直铸（TokensService 面随服务批
// 落地；执法行为在此钉死）。

import (
	"context"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// mintToken 直铸一枚挂指定角色的平台 Token（测试接缝；明文只在本函数
// 返回值存活）。
func mintToken(t *testing.T, h *apitest.Harness, name, roleID string) string {
	t.Helper()
	material, err := identity.NewToken()
	require.NoError(t, err)
	repo := tokenrepo.New(h.DB.Clock())
	require.NoError(t, repo.Create(context.Background(), h.DB.Runner(), &tokenrepo.Token{
		ID: ulid.Make().String(), Name: name,
		TeamID: identity.DefaultTeamID, RoleID: roleID,
		SHA256: material.SHA256, Prefix: material.Prefix,
	}))
	return material.Secret
}

// wantCode 断言 gRPC 错误码并还原信封码。
func wantCode(t *testing.T, err error, grpcCode codes.Code, envelope string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, grpcCode, status.Code(err), "grpc code for %v", err)
	e, ok := apperr.FromError(err)
	require.True(t, ok, "envelope should decode")
	assert.Equal(t, envelope, e.Code())
}

func TestScopeEnforcementMatrix(t *testing.T) {
	h := apitest.New(t)
	anon := context.Background()
	owner := sdk.WithToken(anon, h.Token)
	memberTok := mintToken(t, h, "dev", identity.RoleMemberID)
	member := sdk.WithToken(anon, memberTok)

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	// 匿名：读面即 401。
	_, err := projects.ListProjects(anon, &structurev1.ListProjectsRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")

	// member：读面过、写面 403（projects:write 不在 member 面）。
	list, err := projects.ListProjects(member, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)
	assert.Empty(t, list.GetProjects())
	_, err = projects.CreateProject(member, &structurev1.CreateProjectRequest{Name: "nope"})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")

	// owner（bootstrap）：全权。
	_, err = projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)

	// member：member 面内的写（deployments:write）拿到的是业务错误而非
	// 401/403（App 不存在 → E_NOT_FOUND）。
	_, err = deployments.Deploy(member, &deliveryv1.DeployRequest{AppId: "01APPDOESNOTEXIST000000000", Image: "nginx:1.27"})
	require.Error(t, err)
	assert.NotEqual(t, codes.PermissionDenied, status.Code(err), "member may deploy")
	assert.NotEqual(t, codes.Unauthenticated, status.Code(err))

	// 伪造/未知 token：401。
	_, err = projects.ListProjects(sdk.WithToken(anon, "flt_notarealtoken"), &structurev1.ListProjectsRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")

	// 邀请前缀 token 不在 API 面兑换：401。
	invMaterial, err := identity.NewInvitation()
	require.NoError(t, err)
	_, err = projects.ListProjects(sdk.WithToken(anon, invMaterial.Secret), &structurev1.ListProjectsRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")
}

func TestRevokedTokenNextCallIs401(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)

	// 吊销前：owner 全权可用。
	_, err := projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)

	// 吊销 bootstrap（repo 直吊；TokensService 面随服务批）。
	repo := tokenrepo.New(h.DB.Clock())
	row, err := repo.GetBySHA256(ctx, h.DB.Runner(), identity.HashToken(h.Token))
	require.NoError(t, err)
	_, err = repo.Revoke(context.Background(), h.DB.Runner(), row.ID)
	require.NoError(t, err)

	// 验收锚：进行中请求的下一个调用即 401（逐请求查表，无缓存宽限）。
	_, err = projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	wantCode(t, err, codes.Unauthenticated, "E_UNAUTHENTICATED")
}
