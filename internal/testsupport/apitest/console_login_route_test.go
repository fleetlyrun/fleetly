package apitest_test

// Console 登录路径漂移守卫（ADR-0061 验收锚 4）：engine.ConsoleLoginPath
// 是 identity.proto Login REST 注解的同源物化——公网发布的登录限速路由
//（Host && PathPrefix）按此常量铸路径。注解改路径而常量未同批改的漂移 =
// 限速路由指空路径（登录端点公网无限速裸奔）或主路由吞掉登录（限速失效
// 静默化）。本守卫钉死三件事：常量字面值、gateway 实际服务该路径、相近
// 路径不误配。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestConsoleLoginPathAnchoredToGateway(t *testing.T) {
	// 锚 1：常量字面值（proto 注解 post: "/v1/auth/login" 的同源物化）。
	assert.Equal(t, "/v1/auth/login", engine.ConsoleLoginPath)

	h := apitest.New(t)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 锚 2：gateway 实际服务该路径——空凭证登录回 401 错误信封（PUBLIC
	// 端点语义），绝非 404。
	rec := post(engine.ConsoleLoginPath, `{"name":"drift-probe","password":"x"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "<html")

	// 锚 3：限速路由是精确 PathPrefix 形态的登录路径——相近但不同的路径
	// 仍是 gateway 404（不存在误配的中间形态）。
	rec = post("/v1/auth/loginX", `{}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
