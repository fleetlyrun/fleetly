package engine

// 构建期 Git 访问材料解析的单测（ADR-0073 决策 1）：遮蔽律/host 匹配/
// 精确失败语义走 httptest 假 GitHub + 真 client；argv 组装转义与错误
// 脱敏走纯函数与真 git 二进制。

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/github"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state/githubapp"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func ghKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// ghFake 是 engine 侧的假 GitHub（与 apitest 内联版同形——installation
// id 通配、app_id 恒 182345、铸 token 可注入失败）。
type ghFake struct {
	mu     sync.Mutex
	mintOK bool
	srv    *httptest.Server
}

func newGhFake(t *testing.T) *ghFake {
	t.Helper()
	f := &ghFake{mintOK: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 51790921, "app_id": 182345, "account": map[string]string{"login": "acme"},
		})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.mintOK
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Installation is suspended"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		//nolint:gosec // 测试夹具假 token
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "ghs_engine_fake", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	// TLS 形态：匹配律要求仓库 host == 实体 git host（api_base 派生）——
	// 测试里仓库 URL 直接指向夹具，故夹具必须 https（gitHostOwner 只收
	// https）。engine 客户端注入信任夹具证书的 transport。
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 测试夹具自签证书
}

// TestResolveGitMaterialShadowing：遮蔽律（Project → Global → 无）+ host
// 匹配 + 精确失败（命中而铸造失败不静默降级匿名）。
func TestResolveGitMaterialShadowing(t *testing.T) {
	ctx := context.Background()
	db, clock := statetest.New(t)
	root := t.TempDir()
	cipher, err := material.LoadCipher(root)
	require.NoError(t, err)
	fake := newGhFake(t)
	ghes := newGhFake(t)

	e := &Engine{db: db, cipher: cipher, ghApps: githubapp.New(clock), ghClient: github.NewClient(&http.Client{Transport: &http.Transport{TLSClientConfig: insecureTLS()}}, nil)}
	sealed, err := cipher.Produce(ghKeyPEM(t))
	require.NoError(t, err)
	require.NoError(t, e.ghApps.Upsert(ctx, db.Runner(), &githubapp.App{
		ProjectID: "proj-a", AppID: 182345, InstallationID: 51790921,
		AccountLogin: "acme", APIBase: fake.srv.URL,
		PrivateKeyCiphertext: sealed.Ciphertext, PrivateKeyFingerprint: sealed.Fingerprint,
	}))
	gseal, err := cipher.Produce(ghKeyPEM(t))
	require.NoError(t, err)
	require.NoError(t, e.ghApps.Upsert(ctx, db.Runner(), &githubapp.App{
		ProjectID: "", AppID: 182345, InstallationID: 51790921,
		AccountLogin: "giant-robots", APIBase: fake.srv.URL,
		PrivateKeyCiphertext: gseal.Ciphertext, PrivateKeyFingerprint: gseal.Fingerprint,
	}))

	// Project 级命中：仓库 host 指向夹具（api_base 派生同域）。
	mat, err := e.resolveGitMaterial(ctx, "proj-a", fake.srv.URL+"/acme/web")
	require.NoError(t, err)
	require.NotNil(t, mat)
	assert.Equal(t, installTokenUser, mat.username)
	assert.NotEmpty(t, mat.token)

	// Project 未命中 → Global 回落（同域）。
	mat, err = e.resolveGitMaterial(ctx, "proj-b", fake.srv.URL+"/giant-robots/kit")
	require.NoError(t, err)
	require.NotNil(t, mat, "global row is the fallback")

	// 两态都未命中 → 无材料（匿名合法）。
	mat, err = e.resolveGitMaterial(ctx, "proj-a", fake.srv.URL+"/nobody/x")
	require.NoError(t, err)
	assert.Nil(t, mat)

	// host 失配：github.com 仓 vs 夹具域实体 → 无材料（投影不越域）。
	mat, err = e.resolveGitMaterial(ctx, "proj-a", "https://github.com/acme/web")
	require.NoError(t, err)
	assert.Nil(t, mat, "a fake-host row must not match a github.com repo")

	// GHES 形态：第二个夹具当独立域，(proj-d, acme) 行 + 该域仓 → 命中。
	ghesSeal, err := cipher.Produce(ghKeyPEM(t))
	require.NoError(t, err)
	require.NoError(t, e.ghApps.Upsert(ctx, db.Runner(), &githubapp.App{
		ProjectID: "proj-d", AppID: 182345, InstallationID: 51790921,
		AccountLogin: "acme", APIBase: ghes.srv.URL,
		PrivateKeyCiphertext: ghesSeal.Ciphertext, PrivateKeyFingerprint: ghesSeal.Fingerprint,
	}))
	mat, err = e.resolveGitMaterial(ctx, "proj-d", ghes.srv.URL+"/acme/web")
	require.NoError(t, err)
	require.NotNil(t, mat, "ghes repo matches the row via derived host")

	// 命中而铸造失败 = 精确失败（不静默降级匿名）。先轮换新钥——换指纹
	// 即换缓存键，确保真的打到铸造调用而不是吃上一个 token 的缓存。
	rotSeal, err := cipher.Produce(ghKeyPEM(t))
	require.NoError(t, err)
	require.NoError(t, e.ghApps.Upsert(ctx, db.Runner(), &githubapp.App{
		ProjectID: "proj-d", AppID: 182345, InstallationID: 51790921,
		AccountLogin: "acme", APIBase: ghes.srv.URL,
		PrivateKeyCiphertext: rotSeal.Ciphertext, PrivateKeyFingerprint: rotSeal.Fingerprint,
	}))
	ghes.mu.Lock()
	ghes.mintOK = false
	ghes.mu.Unlock()
	_, err = e.resolveGitMaterial(ctx, "proj-d", ghes.srv.URL+"/acme/web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint installation token for acme")

	// 非法/非 https 仓 URL → 无材料（防御轨，clone 原样失败）。
	mat, err = e.resolveGitMaterial(ctx, "proj-a", "git@github.com:acme/web")
	require.NoError(t, err)
	assert.Nil(t, mat)
}

// TestAssembleCloneURL：userinfo 转义（token 含保留字符安全）+ 畸形输入。
func TestAssembleCloneURL(t *testing.T) {
	u, err := assembleCloneURL("https://github.com/acme/web", "x-access-token", "ghs:tok/en@+")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(u, "https://x-access-token:ghs"), "userinfo rides the URL")
	assert.NotContains(t, u, "ghs:tok/en@+", "reserved characters are percent-escaped")
	assert.True(t, strings.HasSuffix(u, "github.com/acme/web"), u)

	_, err = assembleCloneURL("not a url", "u", "t")
	assert.Error(t, err)
}

// TestCloneErrRedaction：错误输出脱敏端到端——带凭据 URL 与 token 字面都
// 不出现在返回错误里；认证类失败带指向提示。
func TestCloneErrRedaction(t *testing.T) {
	credentialed := "https://x-access-token:ghs_sup3rs3cret@github.com/acme/web" //nolint:gosec // 测试夹具假材料
	out := []byte("fatal: unable to access '" + credentialed + "': Authentication failed")
	secrets := []string{credentialed, "ghs_sup3rs3cret"}

	errText := cloneErr(errFake{}, out, credentialed, secrets).Error()
	assert.NotContains(t, errText, "ghs_sup3rs3cret", "token never enters error text")
	assert.NotContains(t, errText, "x-access-token", "userinfo never enters error text")
	assert.Contains(t, errText, "<repo-url>", "the URL collapses to a placeholder")
	assert.Contains(t, errText, "material was used but rejected", "matched-material auth failure points at the installation")

	errText = cloneErr(errFake{}, out, credentialed, nil).Error()
	assert.Contains(t, errText, "hint: private repository?", "anonymous failure hints the fix")
}

type errFake struct{}

func (errFake) Error() string { return "exit status 128" }

// TestResolveGitSecretMaterial：解析序第三位（ADR-0073 阶段 A）——
// `git:<host>` Project Secret（镜像 registry:<host> 定律）。GitHub App
// 两态命中时 Secret 被遮蔽；Secret 值 JSON username 缺省 oauth2；坏 JSON
// 精确失败；Secret 名防逃逸白名单天然容住 host 形态（字母数字点冒号）。
func TestResolveGitSecretMaterial(t *testing.T) {
	ctx := context.Background()
	db, clock := statetest.New(t)
	root := t.TempDir()
	cipher, err := material.LoadCipher(root)
	require.NoError(t, err)
	fake := newGhFake(t)

	e := &Engine{db: db, cipher: cipher, secrets: secret.New(clock), ghApps: githubapp.New(clock), ghClient: github.NewClient(fake.srv.Client(), nil)}
	proj := "proj-s"
	repo := fake.srv.URL + "/acme/web" // host = 127.0.0.1:<port>
	host, _, ok := gitHostOwner(repo)
	require.True(t, ok)

	mustPutSecret := func(name, value string) {
		sealed, serr := cipher.Produce([]byte(value))
		require.NoError(t, serr)
		require.NoError(t, e.secrets.Upsert(ctx, db.Runner(), &secret.Secret{
			ID: "sec-" + name, ProjectID: proj, Name: name,
			Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
		}))
	}

	// 无材料 = 匿名。
	mat, err := e.resolveGitMaterial(ctx, proj, repo)
	require.NoError(t, err)
	assert.Nil(t, mat)

	// Secret 命中：username/secret 进材料；username 空 → oauth2 兜底。
	mustPutSecret(gitSecretPrefix+host, `{"username":"","secret":"glpat-x"}`)
	mat, err = e.resolveGitMaterial(ctx, proj, repo)
	require.NoError(t, err)
	require.NotNil(t, mat)
	assert.Equal(t, defaultGitUser, mat.username)
	assert.Equal(t, "glpat-x", mat.token)

	// username 在场原样用。
	mustPutSecret(gitSecretPrefix+host, `{"username":"gitlab+ci","secret":"glpat-y"}`)
	mat, err = e.resolveGitMaterial(ctx, proj, repo)
	require.NoError(t, err)
	require.NotNil(t, mat)
	assert.Equal(t, "gitlab+ci", mat.username)

	// 坏 JSON = 精确失败（不静默匿名——私有仓会死在无诊断 401 上）。
	mustPutSecret(gitSecretPrefix+host, `not json`)
	_, err = e.resolveGitMaterial(ctx, proj, repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad JSON")

	// GitHub App 遮蔽 Secret：命中的行先返回，Secret 不参与。
	sealed, serr := cipher.Produce(ghKeyPEM(t))
	require.NoError(t, serr)
	require.NoError(t, e.ghApps.Upsert(ctx, db.Runner(), &githubapp.App{
		ProjectID: proj, AppID: 182345, InstallationID: 51790921,
		AccountLogin: "acme", APIBase: fake.srv.URL,
		PrivateKeyCiphertext: sealed.Ciphertext, PrivateKeyFingerprint: sealed.Fingerprint,
	}))
	mustPutSecret(gitSecretPrefix+host, `{"username":"gitlab+ci","secret":"glpat-y"}`)
	mat, err = e.resolveGitMaterial(ctx, proj, repo)
	require.NoError(t, err)
	require.NotNil(t, mat)
	assert.Equal(t, installTokenUser, mat.username, "the matched github app shadows the secret channel")
}
