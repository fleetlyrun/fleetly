package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestAuthMatrix T2.17 验收（鉴权矩阵，每条至少一 RPC）：
//   - 无 token → 401（Unauthenticated，读面亦拒——安全基线「无 token 全部 401」）
//   - 错 token → 401
//   - read   可读列表（ListApps）
//   - read   不可建 token（CreateToken → 403；W2 语义迁移后 TokensService
//            scope 门登记 read，本行 403 由 handler 收口——机具令牌造
//            token 属平台级写面须 admin scope）
//   - deploy 可部署（Deploy 入队）与写 env（SetEnv）
//   - deploy 不可建 token（403）、不可读 env 明文（GetEnv → 403）
//   - admin  全能（建 token / 读明文 / 删应用）
func TestAuthMatrix(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	apps := serverv1.NewAppsServiceClient(env.conn)
	tokens := serverv1.NewTokensServiceClient(env.conn)
	envSvc := serverv1.NewEnvServiceClient(env.conn)
	deploys := serverv1.NewDeploymentsServiceClient(env.conn)

	// 无 token：读面 401。
	_, err := apps.ListApps(ctx, &serverv1.ListAppsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token ListApps code = %v, want Unauthenticated (401)", status.Code(err))
	}
	// 错 token：401。
	_, err = apps.ListApps(authCtx(ctx, "flt_wrong"), &serverv1.ListAppsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong token ListApps code = %v, want Unauthenticated", status.Code(err))
	}

	// read：可读。
	if _, err := apps.ListApps(authCtx(ctx, env.readTok), &serverv1.ListAppsRequest{}); err != nil {
		t.Fatalf("read token ListApps: %v", err)
	}
	// read：不可建 token（403 PermissionDenied）。
	_, err = tokens.CreateToken(authCtx(ctx, env.readTok), &serverv1.CreateTokenRequest{
		Scopes: []string{"read"}, Note: "x",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token CreateToken code = %v, want PermissionDenied (403)", status.Code(err))
	}

	// deploy：可部署入队（返回 queued id）。
	dr, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{Project: env.projectRef(), 
		App:     "matrixapp",
		Compose: []byte("name: matrixapp\nservices:\n  web:\n    image: nginx:alpine\n"),
	})
	if err != nil {
		t.Fatalf("deploy token Deploy: %v", err)
	}
	if dr.GetStatus() != "queued" || dr.GetDeploymentId() == "" {
		t.Fatalf("deploy response = %+v", dr)
	}
	// deploy：可写 env（env-set = deploy scope）。
	if _, err := envSvc.SetEnv(authCtx(ctx, env.depTok), &serverv1.SetEnvRequest{
		App: "matrixapp", Key: "API_KEY", Value: "super-secret-value-42",
	}); err != nil {
		t.Fatalf("deploy token SetEnv: %v", err)
	}
	// deploy：不可建 token。
	_, err = tokens.CreateToken(authCtx(ctx, env.depTok), &serverv1.CreateTokenRequest{
		Scopes: []string{"deploy"}, Note: "x",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("deploy token CreateToken code = %v, want PermissionDenied", status.Code(err))
	}
	// deploy：不可读 env 明文（GetEnv = admin）。
	_, err = envSvc.GetEnv(authCtx(ctx, env.depTok), &serverv1.GetEnvRequest{App: "matrixapp", Key: "API_KEY"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("deploy token GetEnv code = %v, want PermissionDenied", status.Code(err))
	}

	// admin：建 token（明文仅此一次）+ 读明文 + 删应用。
	cr, err := tokens.CreateToken(authCtx(ctx, env.admTok), &serverv1.CreateTokenRequest{
		Scopes: []string{"read", "deploy"}, Note: "ci",
	})
	if err != nil {
		t.Fatalf("admin CreateToken: %v", err)
	}
	if !strings.HasPrefix(cr.GetToken(), "flt_") || len(cr.GetToken()) < 40 {
		t.Fatalf("created token = %q (prefix/len)", cr.GetToken())
	}
	// 新 token 明文可用（deploy scope 亦蕴含 read）。
	if _, err := apps.ListApps(authCtx(ctx, cr.GetToken()), &serverv1.ListAppsRequest{}); err != nil {
		t.Fatalf("new token ListApps: %v", err)
	}
	// admin：读 env 明文。
	gr, err := envSvc.GetEnv(authCtx(ctx, env.admTok), &serverv1.GetEnvRequest{App: "matrixapp", Key: "API_KEY"})
	if err != nil {
		t.Fatalf("admin GetEnv: %v", err)
	}
	if gr.GetValue() != "super-secret-value-42" || gr.GetStatus() != "pending" {
		t.Fatalf("GetEnv = %+v", gr)
	}
	// admin：删应用（tombstone 第一拍）。
	if _, err := apps.DeleteApp(authCtx(ctx, env.admTok), &serverv1.DeleteAppRequest{Name: "matrixapp"}); err != nil {
		t.Fatalf("admin DeleteApp: %v", err)
	}
	got, err := env.st.GetAppByName(ctx, "matrixapp")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	if got.Lifecycle != state.LifecycleDeleting {
		t.Fatalf("lifecycle = %s, want deleting", got.Lifecycle)
	}
}

// TestAuthPingExempt Ping 豁免（T2.17 契约：Ping 无 token 可用）。
func TestAuthPingExempt(t *testing.T) {
	// 直连被豁免的拦截器路径：Ping 不带 token 走 authExempt。
	//（SystemService 未注册进测试 server——这里直接断言豁免谓词与
	// scope 表，Ping 的端到端在 cmd/fleetlyd 双面测试覆盖。）
	if !authExempt("/fleetly.server.v1.SystemService/Ping") {
		t.Fatal("Ping must be auth-exempt")
	}
	if authExempt("/fleetly.server.v1.AppsService/ListApps") {
		t.Fatal("business method must not be exempt")
	}
	if s, ok := RequiredScope("/fleetly.server.v1.EnvService/GetEnv"); !ok || s != ScopeAdmin {
		t.Fatalf("GetEnv scope = %q ok=%v, want admin", s, ok)
	}
	if s, ok := RequiredScope("/fleetly.server.v1.LogsService/FollowLogs"); !ok || s != ScopeRead {
		t.Fatalf("FollowLogs scope = %q ok=%v, want read", s, ok)
	}
	if s, ok := RequiredScope("/fleetly.server.v1.DeploymentsService/RollbackDeployment"); !ok || s != ScopeDeploy {
		t.Fatalf("RollbackDeployment scope = %q ok=%v, want deploy", s, ok)
	}
	// E7 W5-S6：ExecService 整体 terminal scope（方法级登记）。
	for _, m := range []string{"CreateTerminalTicket", "GetTerminalStatus"} {
		if s, ok := RequiredScope("/fleetly.server.v1.ExecService/" + m); !ok || s != ScopeTerminal {
			t.Fatalf("ExecService.%s scope = %q ok=%v, want terminal", m, s, ok)
		}
	}
}

// TestScopeContainment scope 蕴含语义（admin ⊃ deploy ⊃ read ⊕ terminal
// ——terminal 独立：read/deploy 不蕴含，仅 admin 蕴含；E7 W5-S6）。
func TestScopeContainment(t *testing.T) {
	cases := []struct {
		scopes, need string
		want         bool
	}{
		{"admin", ScopeAdmin, true},
		{"admin", ScopeDeploy, true},
		{"admin", ScopeRead, true},
		{"deploy", ScopeDeploy, true},
		{"deploy", ScopeRead, true},
		{"deploy", ScopeAdmin, false},
		{"read", ScopeRead, true},
		{"read", ScopeDeploy, false},
		{"read,deploy", ScopeAdmin, false},
		{"", ScopeRead, false},
		// E7 terminal：默认仅 admin——read/deploy 不蕴含；显式授予可用。
		{"admin", ScopeTerminal, true},
		{"deploy", ScopeTerminal, false},
		{"read", ScopeTerminal, false},
		{"terminal", ScopeTerminal, true},
		{"terminal", ScopeRead, false},
		{"terminal,deploy", ScopeTerminal, true},
	}
	for _, c := range cases {
		if got := containsScope(c.scopes, c.need); got != c.want {
			t.Fatalf("containsScope(%q, %q) = %v, want %v", c.scopes, c.need, got, c.want)
		}
	}
}

// TestRateLimiter 令牌桶：突发耗尽后拒绝、时间推移恢复、桶按 token 隔离。
func TestRateLimiter(t *testing.T) {
	clk := &fakeClock{cur: time.Now()}
	l := &rateLimiter{buckets: map[string]*tokenBucket{}, rate: 1, burst: 3, now: clk.Now}
	// 3 个突发令牌可用，第 4 个拒绝。
	for i := 0; i < 3; i++ {
		if !l.allow("tok") {
			t.Fatalf("burst request %d rejected", i+1)
		}
	}
	if l.allow("tok") {
		t.Fatal("4th request should be rejected")
	}
	// 不同 token 独立桶。
	if !l.allow("other") {
		t.Fatal("other token should have its own bucket")
	}
	// 时间推进 1s 补充 1 个令牌。
	clk.cur = clk.cur.Add(time.Second)
	if !l.allow("tok") {
		t.Fatal("token should refill over time")
	}
}

// fakeClock 是可推进的测试时钟。
type fakeClock struct{ cur time.Time }

func (f *fakeClock) Now() time.Time { return f.cur }

// TestAuthenticateTouchThrottle A2（S18）：last_used_at 盖写节流——节流窗口
// 内多次认证只写一次（SQLite 写放大收口）；窗口过后再写；不同 token 独立
// 节流；写失败不落窗口（下次认证重试）。touch 端口注入计数假实现 + 假时钟
// 推进窗口。
func TestAuthenticateTouchThrottle(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tok := seedTokenPlain(t, st, "read")
	other := seedTokenPlain(t, st, "read")

	clk := &fakeClock{cur: time.Now()}
	auth := NewAuthenticator(st)
	auth.now = clk.Now
	auth.touchEvery = time.Minute
	var writes int
	auth.touch = func(_ context.Context, _ string) error {
		writes++
		return nil
	}
	ctx := context.Background()
	authOK := func(plaintext string) {
		t.Helper()
		if _, aerr := auth.Authenticate(ctx, "Bearer "+plaintext); aerr != nil {
			t.Fatalf("Authenticate: %v", aerr)
		}
	}

	// 窗口内 10 次认证：仅 1 次盖写。
	for i := 0; i < 10; i++ {
		authOK(tok)
	}
	if writes != 1 {
		t.Fatalf("10 authentications within the window overwrote %d times, want 1", writes)
	}
	// 窗口内推进（<60s）：仍跳过。
	clk.cur = clk.cur.Add(59 * time.Second)
	authOK(tok)
	if writes != 1 {
		t.Fatalf("advance within the window should still skip: overwrote %d times", writes)
	}
	// 窗口过后：再写。
	clk.cur = clk.cur.Add(2 * time.Second)
	authOK(tok)
	if writes != 2 {
		t.Fatalf("after the window it should write again: overwrote %d times, want 2", writes)
	}
	// 不同 token 独立节流（首次认证即写）。
	authOK(other)
	if writes != 3 {
		t.Fatalf("a different token should write on first authentication: overwrote %d times, want 3", writes)
	}

	// 写失败不落窗口：每次认证都重试写（两次认证 → 两次尝试）。先推进
	// 时钟出窗，否则上一段的成功写窗口仍会跳过。
	clk.cur = clk.cur.Add(time.Minute)
	auth.touch = func(_ context.Context, _ string) error {
		writes++
		return errors.New("db locked")
	}
	authOK(tok)
	authOK(tok)
	if writes != 5 {
		t.Fatalf("write failure must not open the window (retry on every authentication): %d write attempts, want 5", writes)
	}
}
