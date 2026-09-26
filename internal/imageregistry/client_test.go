package imageregistry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient 构造指向 httptest 明文基址的解析客户端（生产形态 https；
// 测试注入 http 基址与零等待——退避次数仍真实计数）。
func newTestClient(serverURL string) *Client {
	c := NewClient()
	c.baseURL = func(string) string { return serverURL }
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// testReference 构造指向 httptest 实例的引用。
func testReference(serverURL, repository, tag string) Reference {
	return Reference{
		Host:       strings.TrimPrefix(serverURL, "http://"),
		Repository: repository,
		Tag:        tag,
	}
}

const testDigest = "sha256:feedc0de0000000000000000000000000000000000000000000000000000feed"

// TestResolveAnonymousBearerFlow ghcr 匿名 token flow：manifest 401 挑战 →
// token 服务匿名换 token（无 Basic）→ Bearer 重试命中 → Docker-Content-Digest。
func TestResolveAnonymousBearerFlow(t *testing.T) {
	var (
		server        *httptest.Server
		tokenCalls    int
		manifestCalls int
		tokenHadBasic bool
		gotScope      string
		gotService    string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/owner/app/manifests/1.2.3", func(w http.ResponseWriter, r *http.Request) {
		manifestCalls++
		if r.Header.Get("Authorization") != "Bearer anonymous-token" {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+server.URL+`/token",service="test-registry",scope="repository:owner/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_, _, tokenHadBasic = r.BasicAuth()
		gotScope = r.URL.Query().Get("scope")
		gotService = r.URL.Query().Get("service")
		_, _ = io.WriteString(w, `{"token":"anonymous-token"}`)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	c := newTestClient(server.URL)
	got, err := c.Resolve(context.Background(), testReference(server.URL, "owner/app", "1.2.3"), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != testDigest {
		t.Fatalf("digest = %q, want %q", got, testDigest)
	}
	if manifestCalls != 2 || tokenCalls != 1 {
		t.Fatalf("manifest/token calls = %d/%d, want 2/1 (challenge then Bearer retry)", manifestCalls, tokenCalls)
	}
	if tokenHadBasic {
		t.Fatal("anonymous resolution must not send Basic credentials to the token service")
	}
	if gotScope != "repository:owner/app:pull" || gotService != "test-registry" {
		t.Fatalf("token query scope/service = %q/%q, want challenge values", gotScope, gotService)
	}
}

// TestResolveCredentialBearerFlow 凭证形态：token 服务要求 Basic（平台设置
// 凭证），manifest 以换得的 Bearer 命中。
func TestResolveCredentialBearerFlow(t *testing.T) {
	var (
		server        *httptest.Server
		tokenHadBasic bool
		manifestCalls int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/private/app/manifests/v1", func(w http.ResponseWriter, r *http.Request) {
		manifestCalls++
		if r.Header.Get("Authorization") != "Bearer private-token" {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+server.URL+`/token",service="ghcr.io",scope="repository:private/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		tokenHadBasic = ok && user == "robot" && password == "s3cret"
		if !tokenHadBasic {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"private-token"}`)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	c := newTestClient(server.URL)
	got, err := c.Resolve(context.Background(),
		testReference(server.URL, "private/app", "v1"),
		&Credentials{Username: "robot", Password: "s3cret"})
	if err != nil {
		t.Fatalf("Resolve with credentials: %v", err)
	}
	if got != testDigest || !tokenHadBasic || manifestCalls != 2 {
		t.Fatalf("digest/basic/manifestCalls = %q/%t/%d", got, tokenHadBasic, manifestCalls)
	}
}

// TestResolveBasicChallenge 无 token 服务的自建 registry（registry:2
// htpasswd 同族）：401 挑战 Basic → Basic 重试命中。
func TestResolveBasicChallenge(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/team/app/manifests/v2", func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "robot" || password != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="self-hosted"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	c := newTestClient(server.URL)
	got, err := c.Resolve(context.Background(),
		testReference(server.URL, "team/app", "v2"),
		&Credentials{Username: "robot", Password: "s3cret"})
	if err != nil {
		t.Fatalf("Resolve with basic challenge: %v", err)
	}
	if got != testDigest {
		t.Fatalf("digest = %q, want %q", got, testDigest)
	}
}

// TestResolveNotFoundMapsSentinel 404 → ErrManifestNotFound（调用点据此
// 回落本机 inspect）。
func TestResolveNotFoundMapsSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.Resolve(context.Background(), testReference(server.URL, "owner/ghost", "1.0.0"), nil)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("err = %v, want ErrManifestNotFound sentinel", err)
	}
}

// TestResolveRetriesRateLimitAnd5xx 429/5xx 退避重试：前两次失败第三次命中；
// 全部失败时按预算收敛并点名重试次数。
func TestResolveRetriesRateLimitAnd5xx(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("Docker-Content-Digest", testDigest)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	got, err := c.Resolve(context.Background(), testReference(server.URL, "owner/app", "1.0.0"), nil)
	if err != nil {
		t.Fatalf("Resolve with retries: %v", err)
	}
	if got != testDigest || calls != 3 {
		t.Fatalf("digest/calls = %q/%d, want %q/3 (two retries then hit)", got, calls, testDigest)
	}

	// 恒 5xx：预算耗尽（3 次尝试）后失败，错误点名重试。
	calls = 0
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	c = newTestClient(failing.URL)
	_, err = c.Resolve(context.Background(), testReference(failing.URL, "owner/app", "1.0.0"), nil)
	if err == nil || !strings.Contains(err.Error(), "retried 3 times") {
		t.Fatalf("err = %v, want retry-budget exhaustion naming 3 attempts", err)
	}
	if calls != 3 {
		t.Fatalf("attempts = %d, want 3 (bounded backoff retries)", calls)
	}
}

// TestResolveDigestReferencePassesThrough digest 引用免网络直通（无 HTTP
// 调用；服务器闭死也不影响）。
func TestResolveDigestReferencePassesThrough(t *testing.T) {
	c := NewClient()
	ref := Reference{Host: "ghcr.io", Repository: "owner/app", Digest: testDigest}
	got, err := c.Resolve(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Resolve digest reference: %v", err)
	}
	if got != testDigest {
		t.Fatalf("digest = %q, want passthrough %q", got, testDigest)
	}
}

// TestResolveNeverLeaksCredentials 负面锚：token 服务拒绝/匿名 401/传输
// 失败的错误文本绝不出现凭证材料。
func TestResolveNeverLeaksCredentials(t *testing.T) {
	const password = "registry-SECRET-MARKER-xyz"
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/private/app/manifests/v1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="ghcr.io"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.Resolve(context.Background(),
		testReference(server.URL, "private/app", "v1"),
		&Credentials{Username: "robot", Password: password})
	if err == nil {
		t.Fatal("rejected credentials must fail resolution")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error text leaks the credential: %v", err)
	}

	// 匿名 401 形态同样不出现任何凭证材料。
	_, err = c.Resolve(context.Background(), testReference(server.URL, "private/app", "v1"), nil)
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatalf("anonymous unauthorized err = %v", err)
	}
}

// TestResolveUnauthorizedWithoutChallengeIsExplicit 无挑战 401：显式失败
// （点名 unauthorized），凭证材料零出现。
func TestResolveUnauthorizedWithoutChallengeIsExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	_, err := c.Resolve(context.Background(), testReference(server.URL, "owner/app", "1.0.0"), nil)
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v, want explicit unauthorized failure", err)
	}
}

// TestParseChallengeForms 挑战解析形态（带引号/不带引号/大小写 scheme）。
func TestParseChallengeForms(t *testing.T) {
	scheme, params := parseChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:o/a:pull"`)
	if scheme != "Bearer" || params["realm"] != "https://ghcr.io/token" ||
		params["service"] != "ghcr.io" || params["scope"] != "repository:o/a:pull" {
		t.Fatalf("parsed challenge = %q %v", scheme, params)
	}
	scheme, params = parseChallenge(`basic realm="self"`)
	if scheme != "basic" || params["realm"] != "self" {
		t.Fatalf("basic challenge = %q %v", scheme, params)
	}
	if scheme, params := parseChallenge(""); scheme != "" || len(params) != 0 {
		t.Fatalf("empty challenge = %q %v", scheme, params)
	}
	// 畸形头不 panic、不吞前缀（解析尽力而为）。
	if scheme, _ := parseChallenge("Bearer realm=unquoted,service=svc"); scheme != "Bearer" {
		t.Fatalf("unquoted challenge scheme = %q", scheme)
	}
}

// TestClientDefaults 缺省预算生效（口径断言，防止零值字段改变语义）。
func TestClientDefaults(t *testing.T) {
	c := NewClient()
	if c.maxAttempts() != defaultMaxAttempts || c.retryDelay() != defaultRetryDelay {
		t.Fatalf("defaults = %d/%s, want %d/%s", c.maxAttempts(), c.retryDelay(), defaultMaxAttempts, defaultRetryDelay)
	}
	if base := c.base("ghcr.io"); base != "https://ghcr.io" {
		t.Fatalf("production base = %q, want https://ghcr.io", base)
	}
	if _, err := c.Resolve(context.Background(), Reference{}, nil); err == nil {
		t.Fatal("incomplete reference must fail explicitly")
	}
}
