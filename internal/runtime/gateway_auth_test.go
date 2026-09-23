package runtime

import (
	"context"
	"encoding/json"
	"io"
	gohttp "net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"
	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestGatewayAuthSessionCookieFlow（v0.3 W1 验收：会话 cookie 认证走
// gateway 形态——rbac-teams §2.2 端到端）：生产同构装配（gRPC 拦截链 +
// newGatewayMux，含 OutgoingHeaderMatcher 的 set-cookie 还原）下走 REST 面：
//   - POST /v1/auth/register（豁免鉴权）→ 200 + Set-Cookie 下发会话 +
//     用户投影（首用户 = 平台管理员）；
//   - GET /v1/auth/registration 无凭据 → 200（登录页开关注册入口）；
//   - Cookie 头携带会话 → GET /v1/auth/me 200（gateway 透传 Cookie →
//     metadata grpcgateway-cookie → 会话认证分支）；
//   - 会话凭据走平台面 POST /v1/users → 200（平台管理员门）+ 一次性临时
//     口令；无凭据 POST /v1/users → 401；
//   - POST /v1/auth/logout → 200 + 清除 cookie；旧会话 Me → 401。
func TestGatewayAuthSessionCookieFlow(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gs := lynxgrpc.NewServer(
		lynxgrpc.WithAddr("127.0.0.1:0"),
		lynxgrpc.WithLogger(discardLogger()),
		lynxgrpc.WithHealthCheckers(noCheckers),
		lynxgrpc.WithInterceptors(api.NewAuthenticator(st).UnaryAuthInterceptor()),
	)
	g := gs.GetServer()
	serverv1.RegisterAuthServiceServer(g, api.NewAuthService(st))
	serverv1.RegisterUsersServiceServer(g, api.NewUsersService(st))
	if err := gs.Init(nil); err != nil {
		t.Fatalf("grpc Init: %v", err)
	}
	gsErr := make(chan error, 1)
	go func() { gsErr <- gs.Start(context.Background()) }()
	t.Cleanup(func() {
		_ = gs.Stop(context.Background())
		<-gsErr
	})
	select {
	case <-gs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("grpc server not ready within 5s")
	}

	gw, err := newGatewayMux(gs.Addr())
	if err != nil {
		t.Fatalf("newGatewayMux: %v", err)
	}
	hs := lynxhttp.NewServer(gw,
		lynxhttp.WithAddr("127.0.0.1:0"),
		lynxhttp.WithHealthCheckers(noCheckers),
		lynxhttp.WithLogger(discardLogger()),
	)
	if err := hs.Init(nil); err != nil {
		t.Fatalf("http Init: %v", err)
	}
	hsErr := make(chan error, 1)
	go func() { hsErr <- hs.Start(context.Background()) }()
	t.Cleanup(func() {
		_ = hs.Stop(context.Background())
		<-hsErr
	})
	select {
	case <-hs.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("http server not ready within 5s")
	}
	base := "http://" + hs.Addr()
	client := &gohttp.Client{Timeout: 5 * time.Second}

	// ── 注册（豁免面）：200 + Set-Cookie + 平台管理员投影。──
	req, err := gohttp.NewRequest(gohttp.MethodPost, base+"/v1/auth/register",
		strings.NewReader(`{"email":"founder@example.com","password":"pw-founder-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/auth/register: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("register status = %d body = %s", resp.StatusCode, body)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 || !strings.HasPrefix(cookies[0], "fleetly_session=") {
		t.Fatalf("register Set-Cookie = %v, want fleetly_session=... (HttpOnly/SameSite attrs)", cookies)
	}
	if !strings.Contains(cookies[0], "HttpOnly") || !strings.Contains(cookies[0], "SameSite=Lax") || !strings.Contains(cookies[0], "Path=/") {
		t.Fatalf("session cookie attrs missing: %q", cookies[0])
	}
	sessionValue := strings.Split(strings.TrimPrefix(cookies[0], "fleetly_session="), ";")[0]
	var reg struct {
		User struct {
			Id              string `json:"id"`
			Email           string `json:"email"`
			IsPlatformAdmin bool   `json:"is_platform_admin"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &reg); err != nil {
		t.Fatalf("decode register response %s: %v", body, err)
	}
	if !reg.User.IsPlatformAdmin || reg.User.Email != "founder@example.com" {
		t.Fatalf("register user = %+v, want platform admin (first user)", reg.User)
	}

	// ── 登录页开关面（豁免面）：无凭据 200 + 缺省 closed。──
	res, err := client.Get(base + "/v1/auth/registration")
	if err != nil {
		t.Fatalf("GET /v1/auth/registration: %v", err)
	}
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("registration state status = %d body = %s", res.StatusCode, body)
	}
	var rs struct {
		Open     bool `json:"open"`
		HasUsers bool `json:"has_users"`
	}
	if err := json.Unmarshal(body, &rs); err != nil {
		t.Fatalf("decode registration state %s: %v", body, err)
	}
	if !rs.HasUsers || rs.Open {
		t.Fatalf("registration state = %+v, want has_users=true open=false (default closed)", rs)
	}

	// ── 会话认证（gateway 透传形态）：Cookie 头 → Me。──
	getMe := func(cookie string) (int, string) {
		req, err := gohttp.NewRequest(gohttp.MethodGet, base+"/v1/auth/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != "" {
			req.Header.Set("Cookie", "fleetly_session="+cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /v1/auth/me: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return res.StatusCode, string(body)
	}
	code, meBody := getMe(sessionValue)
	if code != 200 {
		t.Fatalf("Me with session cookie = %d body = %s", code, meBody)
	}
	var me struct {
		User  struct{ Id string `json:"id"` } `json:"user"`
		Teams []struct {
			TeamSlug string `json:"team_slug"`
			Role     string `json:"role"`
		} `json:"teams"`
	}
	if err := json.Unmarshal([]byte(meBody), &me); err != nil {
		t.Fatalf("decode me %s: %v", meBody, err)
	}
	if len(me.Teams) != 1 || me.Teams[0].TeamSlug != "founder" || me.Teams[0].Role != "owner" {
		t.Fatalf("me teams = %s, want personal team owner projection", meBody)
	}
	// 无会话 Me → 401。
	if code, _ := getMe(""); code != 401 {
		t.Fatalf("Me without cookie = %d, want 401", code)
	}

	// ── 平台面（会话凭据 + 平台管理员门）：建用户 200 + 一次性临时口令；
	//    无凭据 401。──
	postUser := func(cookie string) (int, string) {
		req, err := gohttp.NewRequest(gohttp.MethodPost, base+"/v1/users",
			strings.NewReader(`{"email":"mate@example.com","display_name":"Mate"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", "fleetly_session="+cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/users: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return res.StatusCode, string(body)
	}
	code, cuBody := postUser(sessionValue)
	if code != 200 {
		t.Fatalf("admin CreateUser via session = %d body = %s", code, cuBody)
	}
	var cu struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	if err := json.Unmarshal([]byte(cuBody), &cu); err != nil || len(cu.TemporaryPassword) < 16 {
		t.Fatalf("create user response = %s (%v), want one-time temporary_password", cuBody, err)
	}
	if code, _ := postUser(""); code != 401 {
		t.Fatalf("CreateUser without credential = %d, want 401", code)
	}

	// ── 注销：200 + 清除 cookie；旧会话 Me → 401。──
	req, err = gohttp.NewRequest(gohttp.MethodPost, base+"/v1/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "fleetly_session="+sessionValue)
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/auth/logout: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("logout status = %d", res.StatusCode)
	}
	clearCookies := res.Header.Values("Set-Cookie")
	if len(clearCookies) == 0 || !strings.Contains(clearCookies[0], "Max-Age=0") {
		t.Fatalf("logout Set-Cookie = %v, want expired cookie", clearCookies)
	}
	if code, _ := getMe(sessionValue); code != 401 {
		t.Fatalf("Me after logout = %d, want 401", code)
	}
}
