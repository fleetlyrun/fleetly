package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/github"
)

// fakeGitHub 是假 GitHub API：记账铸 token 次数与收到的 Bearer JWT，
// installation/app_id 可注入错配，仓库列两页验证分页循环。
type fakeGitHub struct {
	mu          sync.Mutex
	mints       int
	lastAuth    string
	installApp  int64            // installation 响应的 app_id（错配注入点）
	suspendCode int              // 非 0 时铸 token 返回该状态码
	now         func() time.Time // expires_at 的时钟源（缺省真实时钟；缓存测试对齐假时钟）
	repos       []github.Repo
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/51790921/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.mints++
		f.lastAuth = r.Header.Get("Authorization")
		code := f.suspendCode
		clock := f.now
		f.mu.Unlock()
		if code != 0 {
			http.Error(w, `{"message":"Installation is suspended"}`, code)
			return
		}
		nowFn := time.Now
		if clock != nil {
			nowFn = clock
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_installation_token",
			"expires_at": nowFn().Add(time.Hour).Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET /app/installations/51790921", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      51790921,
			"app_id":  f.installApp,
			"account": map[string]string{"login": "acme"},
		})
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		// 执法（真机咬出口）：该端点只认 installation token（ghs_ 前缀）——
		// App JWT 打上来 = GitHub 401 Bad credentials 的形态。
		if auth := r.Header.Get("Authorization"); auth != "Bearer ghs_installation_token" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 103, "repositories": fullNames(f.repos[:100])})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 103, "repositories": fullNames(f.repos[100:])})
	})
	mux.HandleFunc("POST /app-manifests/one-shot/conversions", func(w http.ResponseWriter, r *http.Request) {
		// 执法（真机咬出口）：conversions 免鉴权——任何 Authorization 头
		// （含空 bearer）都会被 GitHub 解析成坏凭据 = 401 Bad credentials。
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		if f.lastAuth != "" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pemBody := "-----BEGIN " + "RSA PRIVATE KEY-----" // 假件占位串,非凭据
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 182345, "slug": "fleetly-abc123", "name": "fleetly-abc123", "pem": pemBody,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fullNames(rs []github.Repo) []map[string]any {
	out := make([]map[string]any, len(rs))
	for i, r := range rs {
		out[i] = map[string]any{"full_name": r.FullName, "private": r.Private}
	}
	return out
}

// testKey 生成密钥对并铸 PKCS#1 PEM（GitHub App 导出形态）。
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return key, pemBytes
}

func newApp(apiBase string, pemBytes []byte) *github.App {
	return &github.App{
		EntityID: "ghapp-1", KeyFingerprint: "0123456789abcdef",
		AppID: 182345, InstallationID: 51790921,
		APIBase: apiBase, PrivateKeyPEM: pemBytes,
	}
}

func TestResolveInstallation(t *testing.T) {
	ctx := context.Background()
	key, pemBytes := testKey(t)
	fake := &fakeGitHub{installApp: 182345}
	srv := fake.server(t)
	client := github.NewClient(srv.Client(), nil)

	inst, err := client.ResolveInstallation(ctx, newApp(srv.URL, pemBytes))
	require.NoError(t, err)
	assert.Equal(t, "acme", inst.AccountLogin, "owner is resolved from the installation")
	assert.Equal(t, int64(182345), inst.AppID)
	assert.True(t, strings.HasPrefix(fake.lastAuth, "Bearer "), "app JWT rides the Authorization header")

	// 三段式形态（签名验证在 TestAppJWTSignature 做完整校验）。
	parts := strings.Split(strings.TrimPrefix(fake.lastAuth, "Bearer "), ".")
	assert.Len(t, parts, 3)

	// 错配：installation 属于别的 app → ErrAppMismatch（受理面精确报错轴）。
	fake.installApp = 999
	_, err = client.ResolveInstallation(ctx, newApp(srv.URL, pemBytes))
	assert.ErrorIs(t, err, github.ErrAppMismatch)
	fake.installApp = 182345

	// installation 不存在 → 404 UpstreamError。
	missing := newApp(srv.URL, pemBytes)
	missing.InstallationID = 1
	_, err = client.ResolveInstallation(ctx, missing)
	var upstream *github.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusNotFound, upstream.Status)

	// 坏私钥 → 精确错误，不触网。
	corrupt := newApp(srv.URL, []byte("not a pem"))
	corrupt.InstallationID = 1 // 命中不存在路径也不会触网
	_, err = client.ResolveInstallation(ctx, corrupt)
	assert.ErrorContains(t, err, "must be a valid RSA PEM")
	_ = key
}

func TestInstallationTokenCache(t *testing.T) {
	ctx := context.Background()
	_, pemBytes := testKey(t)
	fake := &fakeGitHub{}
	srv := fake.server(t)

	now := time.Unix(1_760_000_000, 0)
	clock := now
	// expires_at 与客户端共用同一假时钟——缓存过期语义才可比。
	fake.now = func() time.Time { return clock }
	client := github.NewClient(srv.Client(), func() time.Time { return clock })
	app := newApp(srv.URL, pemBytes)

	tok, err := client.InstallationToken(ctx, app)
	require.NoError(t, err)
	assert.Equal(t, "ghs_installation_token", tok)
	assert.Equal(t, 1, fake.mints)

	// 缓存命中：到期前重复取不铸新。
	for i := 0; i < 3; i++ {
		clock = clock.Add(10 * time.Minute)
		tok, err = client.InstallationToken(ctx, app)
		require.NoError(t, err)
		assert.Equal(t, "ghs_installation_token", tok)
	}
	assert.Equal(t, 1, fake.mints)

	// 到期前 5 分钟安全余量越过 → 重铸（expires_at = 首铸时刻 + 1h；
	// 时钟推进 55 分钟即越线）。
	clock = now.Add(55 * time.Minute)
	_, err = client.InstallationToken(ctx, app)
	require.NoError(t, err)
	assert.Equal(t, 2, fake.mints, "past the safety margin a fresh token is minted")

	// 私钥轮换（指纹变）即失效缓存 → 立即重铸。
	rotated := newApp(srv.URL, pemBytes)
	rotated.KeyFingerprint = "ffffffffffffffff"
	_, err = client.InstallationToken(ctx, rotated)
	require.NoError(t, err)
	assert.Equal(t, 3, fake.mints, "key rotation busts the cache")
}

func TestInstallationTokenSuspended(t *testing.T) {
	ctx := context.Background()
	_, pemBytes := testKey(t)
	fake := &fakeGitHub{suspendCode: http.StatusForbidden}
	srv := fake.server(t)
	client := github.NewClient(srv.Client(), nil)

	_, err := client.InstallationToken(ctx, newApp(srv.URL, pemBytes))
	var upstream *github.UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusForbidden, upstream.Status)
	assert.Contains(t, upstream.Error(), "suspended", "upstream message rides the error, not the response body")
}

// App JWT 签名与声明的完整校验（RS256 + 公钥验签 + iss/iat/exp 窗）。
func TestAppJWTSignature(t *testing.T) {
	ctx := context.Background()
	key, pemBytes := testKey(t)
	fake := &fakeGitHub{}
	srv := fake.server(t)

	now := time.Unix(1_760_000_000, 0)
	client := github.NewClient(srv.Client(), func() time.Time { return now })
	_, err := client.InstallationToken(ctx, newApp(srv.URL, pemBytes))
	require.NoError(t, err)

	raw := strings.TrimPrefix(fake.lastAuth, "Bearer ")
	parsed, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	assert.Equal(t, "182345", claims["iss"])
	iat, err := claims.GetIssuedAt()
	require.NoError(t, err)
	assert.Equal(t, now.Add(-60*time.Second).Unix(), iat.Unix())
	exp, err := claims.GetExpirationTime()
	require.NoError(t, err)
	assert.Equal(t, now.Add(10*time.Minute).Unix(), exp.Unix())
}

func TestListReposPagination(t *testing.T) {
	ctx := context.Background()
	_, pemBytes := testKey(t)
	repos := []github.Repo{{FullName: "acme/web", Private: true}}
	for i := 0; i < 102; i++ {
		repos = append(repos, github.Repo{FullName: fmt.Sprintf("acme/repo-%03d", i)})
	}
	fake := &fakeGitHub{repos: repos}
	srv := fake.server(t)
	client := github.NewClient(srv.Client(), nil)

	got, err := client.ListRepos(ctx, newApp(srv.URL, pemBytes))
	require.NoError(t, err)
	assert.Len(t, got, 103)
	assert.Equal(t, "acme/web", got[0].FullName)
	assert.True(t, got[0].Private)
}

// conversions 免鉴权执法（真机咬出口）：code 换凭据端点必须零 Authorization
// 头——空 bearer 原样发出即 GitHub 401 Bad credentials。
func TestConvertManifestCodeUnauthenticated(t *testing.T) {
	ctx := context.Background()
	fake := &fakeGitHub{}
	srv := fake.server(t)
	client := github.NewClient(srv.Client(), nil)

	converted, err := client.ConvertManifestCode(ctx, srv.URL, "one-shot")
	require.NoError(t, err)
	assert.Equal(t, int64(182345), converted.AppID)
	assert.Equal(t, "fleetly-abc123", converted.Slug)
	assert.Empty(t, fake.lastAuth, "conversions must not carry any Authorization header")
}
