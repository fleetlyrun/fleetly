package api

// 会话 TTL 策略与 cookie Secure 位测试（v0.3 W2-S4 收口，rbac-teams §2.2
// 「7 天滑动 + 30 天绝对上限；Secure 随 TLS 模式」）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/grpc/credentials/insecure"
	"net"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestSlideSessionSlidingAndAbsoluteCap：SlideSession 把 expires_at 延长到
// min(last_seen + ttl, created_at + 绝对上限)——滑动生效、绝对封顶、节流
// 窗口内零写。
func TestSlideSessionSlidingAndAbsoluteCap(t *testing.T) {
	st := openStateForSessionTest(t)
	ctx := context.Background()

	u, err := st.CreateUser(ctx, state.UserWrite{Email: "slide@example.com", Password: "pw-123456"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, plaintext, err := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, err := st.AuthenticateSession(ctx, plaintext)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	original := sess.ExpiresAt

	// 滑动：ttl 24h > 剩余 1h → expires 延长到 ≈ now+24h（越过原值）。
	if err := st.SlideSession(ctx, sess.ID, 24*time.Hour); err != nil {
		t.Fatalf("SlideSession: %v", err)
	}
	sess, err = st.AuthenticateSession(ctx, plaintext)
	if err != nil {
		t.Fatalf("AuthenticateSession after slide: %v", err)
	}
	if !sess.ExpiresAt.After(original.Add(2 * time.Hour)) {
		t.Fatalf("sliding renewal did not extend past the original expiry: %v → %v", original, sess.ExpiresAt)
	}

	// 绝对上限：超大 ttl 的续期被封顶在 created_at + 30d（±1min 误差）。
	// 独立会话——同一会话 60s 节流窗内的第二次续期零写（设计行为）。
	_, plaintext2, err := st.CreateSession(ctx, u.ID, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateSession(2): %v", err)
	}
	sess2, err := st.AuthenticateSession(ctx, plaintext2)
	if err != nil {
		t.Fatalf("AuthenticateSession(2): %v", err)
	}
	if err := st.SlideSession(ctx, sess2.ID, state.MaxSessionLifetime+48*time.Hour); err != nil {
		t.Fatalf("SlideSession(huge): %v", err)
	}
	sess2, err = st.AuthenticateSession(ctx, plaintext2)
	if err != nil {
		t.Fatalf("AuthenticateSession after capped slide: %v", err)
	}
	cap := sess2.CreatedAt.Add(state.MaxSessionLifetime)
	if diff := sess2.ExpiresAt.Sub(cap); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("expires = %v, want capped at created+30d (%v, diff %v)", sess2.ExpiresAt, cap, diff)
	}
}

// TestCreateSessionAbsoluteCap：api 登录面的创建拍同样封顶——配置 TTL 超过
// 绝对寿命上限时 expires = min(now+ttl, now+上限)。
func TestCreateSessionAbsoluteCap(t *testing.T) {
	st := openStateForSessionTest(t)
	auth := NewAuthService(st).WithSessionSecurity(false, state.MaxSessionLifetime+100*time.Hour)
	if _, err := st.CreateUser(context.Background(), state.UserWrite{Email: "cap@example.com", Password: "pw-123456"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	users, err := st.ListUsers(context.Background())
	if err != nil || len(users) == 0 {
		t.Fatalf("ListUsers: %v", err)
	}
	plaintext, err := auth.createSession(context.Background(), users[0].ID)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	sess, err := st.AuthenticateSession(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("AuthenticateSession: %v", err)
	}
	if remaining := time.Until(sess.ExpiresAt); remaining > state.MaxSessionLifetime+time.Minute {
		t.Fatalf("session lifetime %v exceeds the 30d absolute cap", remaining)
	}
}

// TestSessionCookieSecureBit：Secure 位随控制面 TLS 模式注入（platform/
// manual 带、off 不带——装配面 WithSessionSecurity）。
func TestSessionCookieSecureBit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secure bool
	}{
		{"tls_off", false},
		{"tls_platform_or_manual", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openStateForSessionTest(t)
			auth := NewAuthService(st).WithSessionSecurity(tc.secure, 0)
			srv := grpc.NewServer()
			serverv1.RegisterAuthServiceServer(srv, auth)
			lis := bufconn.Listen(1024 * 1024)
			go func() { _ = srv.Serve(lis) }()
			t.Cleanup(srv.Stop)
			conn, err := grpc.NewClient("passthrough:///bufnet",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
					return lis.DialContext(ctx)
				}),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				t.Fatalf("grpc.NewClient: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			var hdr metadata.MD
			client := serverv1.NewAuthServiceClient(conn)
			if _, err := client.Register(context.Background(), &serverv1.RegisterRequest{
				Email: "first@example.com", Password: "pw-123456",
			}, grpc.Header(&hdr)); err != nil {
				t.Fatalf("Register: %v", err)
			}
			raw, merr := json.Marshal(hdr)
			if merr != nil {
				t.Fatalf("marshal header: %v", merr)
			}
			hasSecure := strings.Contains(string(raw), "Secure")
			if tc.secure && !strings.Contains(string(raw), sessionCookieName+"=") {
				t.Fatalf("no set-cookie header captured: %s", raw)
			}
			if hasSecure != tc.secure {
				t.Fatalf("set-cookie Secure bit = %v, want %v (%s)", hasSecure, tc.secure, raw)
			}
		})
	}
}

// openStateForSessionTest 起独立临时库。
func openStateForSessionTest(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
