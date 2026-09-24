package api

// AuditService 读面双门测试（v0.3 W3-S1，rbac-teams §4.2 第 3 条 + §6
// D-W0-6）：scope 门（机具令牌须 admin——read/deploy 拒）+ 平台面门
//（用户 principal 须 is_platform_admin——非管理员 403、平台管理员 200、
// admin 机具令牌等价放行）+ 过滤/分页参数的服务端透传。

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newAuditEnv 起一个只挂审计读面的鉴权链 server（bufconn）+ 种子用户与
// 审计行。库内首用户 = 平台管理员（CreateUser 首用户强制标志）；mate =
// 第二用户（非管理员）；种子行三行（actor/action/result 互异）。
func newAuditEnv(t *testing.T) (*state.Store, serverv1.AuditServiceClient) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), dir+"/test.db")
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

	base := time.Now().UTC().Add(-time.Hour)
	for _, row := range []state.AuditEntry{
		{ID: "01AUDITAPIOkROW0000000000", At: base, Actor: "user:01USER", Action: "api.AppsService.DeleteApp", Target: "app:01APP", Result: "ok"},
		{ID: "01AUDITLOGINFAIL0000000000", At: base.Add(time.Minute), Actor: "human", Action: "auth.login_failed", Target: "login:ada@example.com", Result: "error", ErrorCode: "E_AUTH_INVALID_CREDENTIALS"},
		{ID: "01AUDITSYSTEMROW0000000000", At: base.Add(2 * time.Minute), Actor: "system", Action: "audit.retention_changed", Target: "platform:audit", Result: "ok"},
	} {
		if err := st.InTx(context.Background(), func(tx *state.Tx) error {
			return tx.WriteAudit(context.Background(), row)
		}); err != nil {
			t.Fatalf("seed audit %s: %v", row.Action, err)
		}
	}

	auth := NewAuthenticator(st)
	srv := newAuthServer(auth)
	serverv1.RegisterAuditServiceServer(srv, NewAuditService(st))
	conn := serveBufconn(t, srv)
	return st, serverv1.NewAuditServiceClient(conn)
}

// mustUserByEmail 按 email 取用户行（夹具回读）。
func mustUserByEmail(t *testing.T, st *state.Store, email string) *state.User {
	t.Helper()
	users, err := st.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for i := range users {
		if users[i].Email == email {
			return &users[i]
		}
	}
	t.Fatalf("user %s not found", email)
	return nil
}

// TestAuditGateMatrix 双门矩阵（凭据形态 × 预期，rbac-teams §4.2 第 3 条）：
// 非平台管理员用户 403（admin scope 也拒——scope 门不是平台面的充分条件）；
// 平台管理员用户 200；admin 机具令牌 200（平台管理员等价）；read scope
// 机具令牌被 scope 门拒。
func TestAuditGateMatrix(t *testing.T) {
	st, client := newAuditEnv(t)
	mate := mustUserByEmail(t, st, "mate@example.com")
	root := mustUserByEmail(t, st, "root@example.com")
	list := func(token string) error {
		_, err := client.ListAudit(authCtx(context.Background(), token), &serverv1.ListAuditRequest{})
		return err
	}

	// 非平台管理员用户（admin scope PAT 也不行——平台面判定在 handler）。
	if err := list(seedUserPAT(t, st, mate.ID, ScopeAdmin)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin user ListAudit err = %v, want PermissionDenied", err)
	}
	// 非平台管理员用户、read scope：scope 门先拒（同 403 形态）。
	if err := list(seedUserPAT(t, st, mate.ID, ScopeRead)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin read-scope ListAudit err = %v, want PermissionDenied", err)
	}
	// 平台管理员用户 PAT：200 + 种子行全在（表内另有 CreateUser 自身的
	// user.created 行——只断言种子行存在，不锚绝对总数）。
	tokRoot := seedUserPAT(t, st, root.ID, ScopeAdmin)
	resp, err := client.ListAudit(authCtx(context.Background(), tokRoot), &serverv1.ListAuditRequest{})
	if err != nil {
		t.Fatalf("platform admin ListAudit: %v", err)
	}
	seeded := map[string]bool{
		"01AUDITAPIOkROW0000000000": false, "01AUDITLOGINFAIL0000000000": false, "01AUDITSYSTEMROW0000000000": false,
	}
	for _, a := range resp.GetAudits() {
		if _, ok := seeded[a.GetId()]; ok {
			seeded[a.GetId()] = true
		}
	}
	for id, seen := range seeded {
		if !seen {
			t.Fatalf("seeded row %s missing from the platform-admin page (total %d)", id, resp.GetTotal())
		}
	}
	// admin 机具令牌：等价放行（平台级凭据的设计语义，rbac-teams §2.3）。
	tokMachine := seedTokenPlain(t, st, ScopeAdmin)
	if _, err := client.ListAudit(authCtx(context.Background(), tokMachine), &serverv1.ListAuditRequest{}); err != nil {
		t.Fatalf("machine admin token ListAudit: %v", err)
	}
	// read scope 机具令牌：scope 门拒。
	if err := list(seedTokenPlain(t, st, ScopeRead)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("machine read-scope ListAudit err = %v, want PermissionDenied", err)
	}
}

// TestAuditListFilterPassThrough 过滤/分页参数到 state.AuditQuery 的透传
// （语义本体在 state 单测钉死，此处钉 API 面不吞参数）+ 投影全字段。
func TestAuditListFilterPassThrough(t *testing.T) {
	st, client := newAuditEnv(t)
	root := mustUserByEmail(t, st, "root@example.com")
	tok := seedUserPAT(t, st, root.ID, ScopeAdmin)
	call := func(req *serverv1.ListAuditRequest) (*serverv1.ListAuditResponse, error) {
		return client.ListAudit(authCtx(context.Background(), tok), req)
	}

	// action 前缀：auth. 族只命中 login_failed 行。
	resp, err := call(&serverv1.ListAuditRequest{Action: "auth."})
	if err != nil || resp.GetTotal() != 1 || resp.GetAudits()[0].GetAction() != "auth.login_failed" {
		t.Fatalf("action prefix = total %d err %v, want the login_failed row", resp.GetTotal(), err)
	}
	// result 精确：error 一行，投影全字段（error_code/actor/target/at）。
	resp, err = call(&serverv1.ListAuditRequest{Result: "error"})
	if err != nil || resp.GetTotal() != 1 {
		t.Fatalf("result=error = total %d err %v, want 1", resp.GetTotal(), err)
	}
	row := resp.GetAudits()[0]
	if row.GetErrorCode() != "E_AUTH_INVALID_CREDENTIALS" || row.GetActor() != "human" ||
		row.GetTarget() != "login:ada@example.com" || row.GetAt() == nil ||
		row.GetId() != "01AUDITLOGINFAIL0000000000" {
		t.Fatalf("projection = %+v", row)
	}
	// actor 子串：user: 前缀命中 api 行。
	resp, err = call(&serverv1.ListAuditRequest{Actor: "user:"})
	if err != nil || resp.GetTotal() != 1 || resp.GetAudits()[0].GetId() != "01AUDITAPIOkROW0000000000" {
		t.Fatalf("actor substring = total %d err %v, want the api row", resp.GetTotal(), err)
	}
	// target 子串。
	resp, err = call(&serverv1.ListAuditRequest{Target: "platform:audit"})
	if err != nil || resp.GetTotal() != 1 || resp.GetAudits()[0].GetActor() != "system" {
		t.Fatalf("target substring = total %d err %v, want the system row", resp.GetTotal(), err)
	}
	// since/until 闭区间（与 action 过滤组合锚定种子行——user.created 行
	// 的存在使无过滤绝对计数不可锚）。种子 login_failed 行 = base+1m。
	since := time.Now().UTC().Add(-time.Hour)
	resp, err = call(&serverv1.ListAuditRequest{Action: "auth.", Since: timestamppb.New(since)})
	if err != nil || resp.GetTotal() != 1 {
		t.Fatalf("since+action = total %d err %v, want 1", resp.GetTotal(), err)
	}
	resp, err = call(&serverv1.ListAuditRequest{Action: "auth.", Until: timestamppb.New(since.Add(time.Minute))})
	if err != nil || resp.GetTotal() != 1 {
		t.Fatalf("until+action = total %d err %v, want 1", resp.GetTotal(), err)
	}
	// 分页自洽：全量 N 行 → limit=2 offset=1 的页给 2 行、total=N、页首 =
	// 全量序第二行（总数与行集跨查询一致）。
	full, err := call(&serverv1.ListAuditRequest{Limit: 1000})
	if err != nil {
		t.Fatalf("full page: %v", err)
	}
	n := full.GetTotal()
	if n < 5 || len(full.GetAudits()) != int(n) {
		t.Fatalf("full page = %d rows / total %d (fixture must seed >4 audit rows)", len(full.GetAudits()), n)
	}
	resp, err = call(&serverv1.ListAuditRequest{Limit: 2, Offset: 1})
	if err != nil || resp.GetTotal() != n || len(resp.GetAudits()) != 2 ||
		resp.GetAudits()[0].GetId() != full.GetAudits()[1].GetId() {
		t.Fatalf("limit/offset page = total %d rows %d err %v, want %d/2/nil with page head = full[1]",
			resp.GetTotal(), len(resp.GetAudits()), err, n)
	}
	// 无匹配：空页不报错。
	resp, err = call(&serverv1.ListAuditRequest{Action: "no.such"})
	if err != nil || resp.GetTotal() != 0 || len(resp.GetAudits()) != 0 {
		t.Fatalf("no match = total %d rows %d err %v, want 0/0/nil", resp.GetTotal(), len(resp.GetAudits()), err)
	}
}
