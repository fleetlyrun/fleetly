package cmd

// GitHubApps 动词 golden 双形态（ADR-0073 阶段 B CLI 面）：put（受理即
// 验证 + 属主自动解析）→ list（project）→ repos（installation 健康面）→
// put --global（平台态）→ list --global → delete。指纹是明文的函数——
// 夹具 PEM 固定，golden 逐字稳定；fprRe 归一兜底。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/github"
)

// ghGoldenFake 是 golden 夹具的假 GitHub（TLS——validateAPIBase 只收
// https；两套 installation id 各归各 app，错配不触发）。
type ghGoldenFake struct {
	srv *httptest.Server
	pem []byte
}

func newGoldenFakeGitHub(t *testing.T, h *apitest.Harness) *ghGoldenFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &ghGoldenFake{pem: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})}
	install := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 51790921, "app_id": 182345, "account": map[string]string{"login": "acme"},
		})
	}
	mint := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "ghs_golden", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations/{id}", install)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", mint)
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "repositories": []map[string]any{
			{"full_name": "acme/web", "private": true},
			{"full_name": "acme/oss-templates", "private": false},
		}})
	})
	f.srv = httptest.NewTLSServer(mux)
	// 夹具信任自签证书：换装 Services.GitHub（api_base 指向夹具即端到端；
	// 生产客户端零改动）。
	h.Services.GitHub = github.NewClient(&http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // 测试夹具自签证书
	}, nil)
	t.Cleanup(f.srv.Close)
	return f
}

func TestGoldenGitHubAppsVerbs(t *testing.T) {
	h := newGoldenHarness(t)
	fake := newGoldenFakeGitHub(t, h)

	keyFile := filepath.Join(t.TempDir(), "fleetly.pem")
	require.NoError(t, os.WriteFile(keyFile, fake.pem, 0o600))

	// 夹具项目（Golden 项目流之外独立——本组动词自足）。
	code, out, stderr := runCLI(t, "projects", "create", "private")
	if code != 0 || stderr != "" {
		t.Fatalf("projects create: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)

	type step struct {
		verb string
		args []string
	}
	var ghappID string
	steps := []step{
		{"github-apps put", []string{"github-apps", "put", "--project", projectID, "--app-id", "182345",
			"--installation-id", "51790921", "--private-key-file", keyFile, "--api-base", fake.srv.URL}},
		{"github-apps list", []string{"github-apps", "list", "--project", projectID}},
		{"github-apps repos", []string{"github-apps", "repos", "--id", "GOLDEN_GHAPP"}},
		{"github-apps put global", []string{"github-apps", "put", "--global", "--app-id", "182345",
			"--installation-id", "51790921", "--private-key-file", keyFile, "--api-base", fake.srv.URL}},
		{"github-apps list global", []string{"github-apps", "list", "--global"}},
		{"github-apps delete", []string{"github-apps", "delete", "--id", "GOLDEN_GHAPP"}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				if a == "GOLDEN_GHAPP" {
					args[i] = ghappID
				}
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			if st.verb == "github-apps put" {
				ghappID = extractGitHubAppID(t, out)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮：list/repos 幂等直跑；put 幂等键重放同响应；delete
			// 幂等（软删行再删归 nil）。
			code, out, stderr = runCLI(t, append(args, "--json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// extractGitHubAppID 取 put 人类形态 "app ghapp-N" 首行的行 ID（put 输出
// 无 "(id X)" 尾巴——printGitHubApp 块状形态）。
func extractGitHubAppID(t *testing.T, out string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^app ([a-z0-9][a-z0-9-]*)$`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract github app id from output: %q", out)
	}
	return m[1]
}
