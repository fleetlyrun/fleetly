package api

// B5 批次评审整改的 api 面回归：
//   - M4-6 RevokeToken 最后管理员守卫（唯一 admin 吊销 → E_TOKEN_LAST_ADMIN
//     409；存在第二枚 admin 时可吊销）；
//   - M4-8 TriggerBuild 入队审计的调用方归因（build.create actor=human
//     而非 system——H14 敏感写面的行为人记录）；
//   - X-5 业务冲突信封在 gateway 渲染核（EnvelopeFromGRPCStatus）上保留
//     原文（REST 409 不再被 B1 脱敏误伤成 "internal error"）。

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// TestRevokeTokenLastAdminGuard M4-6：唯一 admin token 的吊销被守卫拒绝
// （E_TOKEN_LAST_ADMIN、HTTP 409、token 仍在册）；创建第二枚 admin 后
// 原枚可正常吊销。
func TestRevokeTokenLastAdminGuard(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	tokens := serverv1.NewTokensServiceClient(env.conn)

	adminID := func() string {
		rows, err := env.st.ListTokens(ctx)
		if err != nil {
			t.Fatalf("ListTokens: %v", err)
		}
		for _, r := range rows {
			if r.Scopes == "admin" {
				return r.ID
			}
		}
		t.Fatal("seeded admin token not found")
		return ""
	}()
	first := adminID

	// 唯一 admin（read/deploy token 不解除守卫）：吊销被拒。
	_, err := tokens.RevokeToken(authCtx(ctx, env.admTok), &serverv1.RevokeTokenRequest{Id: first})
	if err == nil {
		t.Fatal("revoking the only admin token must be rejected")
	}
	ae, ok := apperr.FromError(err)
	if !ok || ae.Code() != "E_TOKEN_LAST_ADMIN" {
		t.Fatalf("revoke last admin err = %v, want E_TOKEN_LAST_ADMIN envelope", err)
	}
	if ae.HTTPStatus() != 409 {
		t.Fatalf("E_TOKEN_LAST_ADMIN HTTP = %d, want 409", ae.HTTPStatus())
	}
	// token 仍在册（守卫回滚了吊销）。
	still, err := env.st.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens after guard: %v", err)
	}
	found := false
	for _, r := range still {
		if r.ID == first && r.RevokedAt.IsZero() {
			found = true
		}
	}
	if !found {
		t.Fatal("guarded admin token must remain registered (revoke rolled back)")
	}

	// 创建第二枚 admin 后：原枚可正常吊销。
	created, err := tokens.CreateToken(authCtx(ctx, env.admTok), &serverv1.CreateTokenRequest{
		Scopes: []string{"admin"}, Note: "second admin",
	})
	if err != nil {
		t.Fatalf("CreateToken second admin: %v", err)
	}
	if _, err := tokens.RevokeToken(authCtx(ctx, env.admTok), &serverv1.RevokeTokenRequest{Id: first}); err != nil {
		t.Fatalf("revoke with second admin present: %v", err)
	}
	_ = created
}

// TestTriggerBuildAuditAttribution M4-8：admin token 触发构建 → build.create
// 审计行 actor=human（调用方面）而非 system；actor_token_id 归因在 state 层
// 测试（TestCreateBuildAsAuditAttribution）经行内查询钉死，此处断言投影面。
func TestTriggerBuildAuditAttribution(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	builds := serverv1.NewBuildsServiceClient(env.conn)

	resp, err := builds.TriggerBuild(authCtx(ctx, env.admTok), &serverv1.TriggerBuildRequest{
		Compose: triggerCompose("attrib-app", "."),
	})
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if len(resp.GetBuilds()) != 1 {
		t.Fatalf("builds = %d, want 1", len(resp.GetBuilds()))
	}
	buildID := resp.GetBuilds()[0].GetId()

	audits, err := env.st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action != "build.create" || !strings.Contains(a.DiffSummary, buildID) {
			continue
		}
		found = true
		if a.Actor != "human" {
			t.Fatalf("build.create audit actor = %q, want human (调用方归因，M4-8)", a.Actor)
		}
	}
	if !found {
		t.Fatal("build.create audit row for triggered build missing")
	}
}

// TestConflictEnvelopePreservedOnREST X-5：业务冲突通道（conflict()——
// 生命周期冲突等 409）经 gateway 渲染核 EnvelopeFromGRPCStatus 后保留业务
// 文案与 409 状态，不再命中 B1 脱敏（无 detail 才脱）误伤成固定文案。
func TestConflictEnvelopePreservedOnREST(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	apps := serverv1.NewAppsServiceClient(env.conn)

	if _, err := env.st.CreateApp(ctx, "", "conflict-app"); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	// 第一拍：active → deleting（成功）。
	if _, err := apps.DeleteApp(authCtx(ctx, env.admTok), &serverv1.DeleteAppRequest{Name: "conflict-app"}); err != nil {
		t.Fatalf("first DeleteApp: %v", err)
	}
	// 第二拍：deleting 重复删除 → 业务冲突 409。
	_, err := apps.DeleteApp(authCtx(ctx, env.admTok), &serverv1.DeleteAppRequest{Name: "conflict-app"})
	if err == nil {
		t.Fatal("second DeleteApp must conflict")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("conflict err is not a status: %v", err)
	}
	envl, httpStatus := apperr.EnvelopeFromGRPCStatus(st)
	if httpStatus != 409 {
		t.Fatalf("conflict HTTP = %d, want 409", httpStatus)
	}
	if envl.GetMessage() == apperr.RedactedDegradedMessage || !strings.Contains(envl.GetMessage(), "not deletable") {
		t.Fatalf("conflict message = %q, want business copy preserved (X-5)", envl.GetMessage())
	}
}
