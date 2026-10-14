package console

// Console 静态面行为锚（F2.6/ADR-0044 验收锚 1/2）：embed 产物在
// httptest 级钉死——入口与 SPA fallback、hashed asset 不可变缓存、
// /v1/* 旁路零变化、非 GET 动词 405。

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(handler http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil))
	return rec
}

// firstAsset 从 embed 产物里取一个真实 hashed asset 路径（断言不写死
// 构建哈希——文件名随内容变）。
func firstAsset(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(distFS, "dist/assets/*")
	require.NoError(t, err)
	require.NotEmpty(t, matches, "console dist carries no hashed assets — run mise run console:build")
	return "/" + strings.TrimPrefix(path.Dir(matches[0])+"/"+path.Base(matches[0]), "dist/")
}

func TestRootServesIndexHTML(t *testing.T) {
	rec := serve(Handler(), "/")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "<title>fleetly console</title>")
}

func TestSPAFallbackServesIndexHTML(t *testing.T) {
	// hash 路由（#/logs 等）不经服务端；深链与未命中路径一律回入口。
	for _, target := range []string{"/deployments", "/logs", "/some/unknown/path"} {
		rec := serve(Handler(), target)
		require.Equal(t, http.StatusOK, rec.Code, target)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), target)
		assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"), target)
		assert.Contains(t, rec.Body.String(), "<title>fleetly console</title>", target)
	}
}

func TestHashedAssetImmutableCache(t *testing.T) {
	rec := serve(Handler(), firstAsset(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
}

func TestIndexHTMLDirectIsNoCache(t *testing.T) {
	rec := serve(Handler(), "/index.html")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestMountPassesV1Through(t *testing.T) {
	passed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		passed = true
		w.WriteHeader(http.StatusTeapot)
	})
	handler := Mount(next)
	for _, target := range []string{"/v1/users", "/v1/deployments", "/v1/events/follow?ticket=x"} {
		rec := serve(handler, target)
		require.Equal(t, http.StatusTeapot, rec.Code, target)
		assert.True(t, passed, target)
		passed = false
	}
	// 非 /v1 路径仍走静态面（SPA 入口），不透传。
	rec := serve(handler, "/")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<title>fleetly console</title>")
}

func TestNonGetIs405(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
