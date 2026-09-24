package api

// 审计留存设置面测试（v0.3 W3-S3，rbac-teams §6 裁决 D-W0-6 收口）：
// GetAuditRetention / SetAuditRetention 挂 UsersService（平台面写语义与
// SetRegistration 同族）——双门矩阵（非平台管理员 403 / 机具 admin 200 /
// 平台管理员 200）、值域门（days ≥ 1，进程内侧显式 InvalidArgument）、
// 未设置的诚实投影（set=false，days 不投影缺省值）与写后审计行
//（audit.retention_changed，actor=user:<id>——state 层同事务落档）。

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func newRetentionEnv(t *testing.T) (*state.Store, serverv1.UsersServiceClient) {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateUser(context.Background(), state.UserWrite{Email: "root@example.com", Password: "pw-123456"}); err != nil {
		t.Fatalf("CreateUser root: %v", err)
	}
	if _, err := st.CreateUser(context.Background(), state.UserWrite{Email: "mate@example.com", Password: "pw-123456"}); err != nil {
		t.Fatalf("CreateUser mate: %v", err)
	}
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterUsersServiceServer(srv, NewUsersService(st))
	conn := serveBufconn(t, srv)
	return st, serverv1.NewUsersServiceClient(conn)
}

func TestAuditRetentionFaces(t *testing.T) {
	st, client := newRetentionEnv(t)
	ctx := context.Background()
	root := mustUserByEmail(t, st, "root@example.com")
	mate := mustUserByEmail(t, st, "mate@example.com")

	// 未设置形态：set=false、days 不投影（不谎报缺省值存在）。
	resp, err := client.GetAuditRetention(authCtx(ctx, seedUserPAT(t, st, root.ID, ScopeAdmin)), &serverv1.GetAuditRetentionRequest{})
	if err != nil {
		t.Fatalf("GetAuditRetention (unset): %v", err)
	}
	if resp.GetSet() || resp.GetDays() != 0 {
		t.Fatalf("unset projection = set=%v days=%d, want set=false days=0", resp.GetSet(), resp.GetDays())
	}

	// 双门：非平台管理员用户（声明 admin scope）403——平台面判定在 handler。
	tokUser := seedUserPAT(t, st, mate.ID, ScopeAdmin)
	if _, err := client.GetAuditRetention(authCtx(ctx, tokUser), &serverv1.GetAuditRetentionRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin GetAuditRetention err = %v, want PermissionDenied", err)
	}
	if _, err := client.SetAuditRetention(authCtx(ctx, tokUser), &serverv1.SetAuditRetentionRequest{Days: 30}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin SetAuditRetention err = %v, want PermissionDenied", err)
	}

	// 值域门（进程内侧）：days=0/负数显式 InvalidArgument——不把「关掉清理」
	// 伪装成合法设置（buf.validate 拦 HTTP 面，此处为第二道）。
	tokRoot := seedUserPAT(t, st, root.ID, ScopeAdmin)
	for _, bad := range []int32{0, -7} {
		if _, err := client.SetAuditRetention(authCtx(ctx, tokRoot), &serverv1.SetAuditRetentionRequest{Days: bad}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("SetAuditRetention(days=%d) err = %v, want InvalidArgument", bad, err)
		}
	}

	// 平台管理员用户 PAT 写：保存即回显；读回投影显式值与 updated_at。
	setResp, err := client.SetAuditRetention(authCtx(ctx, tokRoot), &serverv1.SetAuditRetentionRequest{Days: 30})
	if err != nil || setResp.GetDays() != 30 {
		t.Fatalf("admin SetAuditRetention = %d err %v, want 30/nil", setResp.GetDays(), err)
	}
	resp, err = client.GetAuditRetention(authCtx(ctx, tokRoot), &serverv1.GetAuditRetentionRequest{})
	if err != nil || !resp.GetSet() || resp.GetDays() != 30 || resp.GetUpdatedAt() == nil {
		t.Fatalf("GetAuditRetention after set = %+v err %v, want set=true days=30 with timestamp", resp, err)
	}

	// 机具 admin 令牌等价放行（平台级凭据，rbac-teams §2.3）——改 45 再读回。
	tokMachine := seedTokenPlain(t, st, ScopeAdmin)
	if _, err := client.SetAuditRetention(authCtx(ctx, tokMachine), &serverv1.SetAuditRetentionRequest{Days: 45}); err != nil {
		t.Fatalf("machine SetAuditRetention: %v", err)
	}
	if resp, err = client.GetAuditRetention(authCtx(ctx, tokMachine), &serverv1.GetAuditRetentionRequest{}); err != nil || resp.GetDays() != 45 {
		t.Fatalf("machine readback = %+v err %v, want days=45", resp, err)
	}

	// 写侧审计：audit.retention_changed 行在档，actor=user:<id>（用户操作）。
	rows, _, err := st.ListAudits(ctx, state.AuditQuery{Action: "audit.retention_changed", Limit: 100})
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Actor == "user:"+root.ID && r.Target == "platform:audit" && r.Result == "ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit.retention_changed row with actor=user:%s missing (%d rows)", root.ID, len(rows))
	}
}
