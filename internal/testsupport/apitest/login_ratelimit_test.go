package apitest_test

// Login 口令面限速（R14-M1 限速半边，S13）端到端验收：失败尝试累计、
// 成功登录清零、超限统一 E_RATE_LIMITED（gRPC 带 RetryInfo；REST 429
// 带 retry_after_seconds 上下文），且超限发生在凭证校验之前（正确口令
// 同样被拒，窗口内不可绕过）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// loginAttempt 以 PUBLIC Login 面自证（失败形态由调用方断言）。
func loginAttempt(h *apitest.Harness, name, password string) error {
	_, err := identityv1.NewUsersServiceClient(h.Conn).Login(context.Background(),
		&identityv1.LoginRequest{Name: name, Password: password})
	return err
}

// TestLoginRateLimitClearsOnSuccess：4 次失败不触发限速；成功登录清零后
// 重新获得满预算（5 次失败），第 6 次失败才被限速。
func TestLoginRateLimitClearsOnSuccess(t *testing.T) {
	h := apitest.New(t)
	_, err := identityv1.NewUsersServiceClient(h.Conn).CreateUser(h.Ctx(), &identityv1.CreateUserRequest{
		Name: "rate-user", RoleId: identity.RoleMemberID, Password: "correct-pass",
	})
	require.NoError(t, err)

	for i := 0; i < 4; i++ {
		wantCode(t, loginAttempt(h, "rate-user", "wrong-pass"), codes.Unauthenticated, "E_UNAUTHENTICATED")
	}
	// 成功登录不受影响，并把两桶清零。
	require.NoError(t, loginAttempt(h, "rate-user", "correct-pass"))
	for i := 0; i < 5; i++ {
		wantCode(t, loginAttempt(h, "rate-user", "wrong-pass"), codes.Unauthenticated, "E_UNAUTHENTICATED")
	}
	wantCode(t, loginAttempt(h, "rate-user", "wrong-pass"), codes.ResourceExhausted, "E_RATE_LIMITED")

	// 超限门先于凭证校验：窗口内正确口令同样被拒（不可用成功登录探测）。
	limited := loginAttempt(h, "rate-user", "correct-pass")
	wantCode(t, limited, codes.ResourceExhausted, "E_RATE_LIMITED")
	// gRPC status 携带 RetryInfo（Retry-After 语义的机器面真源）。
	var retryInfo *errdetails.RetryInfo
	for _, d := range status.Convert(limited).Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			retryInfo = ri
		}
	}
	require.NotNil(t, retryInfo, "E_RATE_LIMITED must carry a RetryInfo detail")
	assert.GreaterOrEqual(t, retryInfo.GetRetryDelay().AsDuration(), time.Second)

	// REST 面同日口径：429 + 信封 context 的 retry_after_seconds。
	rec := h.REST().Post("/v1/auth/login", `{"name":"rate-user","password":"wrong-pass"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_RATE_LIMITED"`)
	assert.Contains(t, rec.Body.String(), `"retry_after_seconds"`)
}
