package assembly

// Browse ForwardAuth 会话绑定单测（H4，R04-H1）：authorize 面必须把目标会话
// （Auth.Address 的 ?session=）与 cookie 内 sid 绑定——任一有效 browse cookie
// 否则可穿过他人 browse host 的门禁（工具容器本身无认证）。httptest 驱动
// serveBrowseAuthorizeWith 的校验函数缝，401 语义保持"匿名面只呈现未授权"。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authorizeRequest 构造 authorize 请求：cookie 空 = 不携带 Cookie 头。
func authorizeRequest(t *testing.T, cookie, session string) *http.Request {
	t.Helper()
	target := "/v1/browse/authorize"
	if session != "" {
		target += "?session=" + session
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req
}

// TestBrowseAuthorizeSessionBinding：cookie 与目标会话不一致 → 401 且不进
// grant 校验；一致 + grant 有效 → 200。
func TestBrowseAuthorizeSessionBinding(t *testing.T) {
	t.Run("cookie for another session is rejected without validating the grant", func(t *testing.T) {
		validated := 0
		validate := func(string, string) bool {
			validated++
			return true
		}
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "flt_browse=sidA.grantA", "sidB"), validate)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Zero(t, validated, "cross-session requests must not reach the grant registry")
	})

	t.Run("matching session with a valid grant is authorized", func(t *testing.T) {
		var gotSID, gotGrant string
		validate := func(sid, grant string) bool {
			gotSID, gotGrant = sid, grant
			return true
		}
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "flt_browse=sidA.grantA", "sidA"), validate)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "sidA", gotSID)
		assert.Equal(t, "grantA", gotGrant)
	})

	t.Run("matching session with a rejected grant is unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "flt_browse=sidA.stale", "sidA"),
			func(string, string) bool { return false })
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("missing target session is unauthorized", func(t *testing.T) {
		validated := 0
		validate := func(string, string) bool {
			validated++
			return true
		}
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "flt_browse=sidA.grantA", ""), validate)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Zero(t, validated, "a session-less authorize call must fail closed")
	})

	t.Run("missing cookie is unauthorized", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "", "sidA"),
			func(string, string) bool { return true })
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("malformed cookie is unauthorized", func(t *testing.T) {
		validated := 0
		validate := func(string, string) bool {
			validated++
			return true
		}
		rec := httptest.NewRecorder()
		serveBrowseAuthorizeWith(rec, authorizeRequest(t, "flt_browse=not-a-cookie-shape", "sidA"), validate)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Zero(t, validated)
	})
}
