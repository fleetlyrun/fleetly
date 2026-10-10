package zot

// RegistryContent 代理测试（IA v3 二期⑤b）：httptest 上游钉请求形态
//（basic auth/路径/Accept）与解析面（catalog 序、manifest 事实、index
// 诚实留空、上游 4xx 报文带 snippet）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// contentFixture 起一个假 zot v2 上游（mux 记录请求头；catalog/tags/
// manifest/blobs 四路），返回指向它的端点。
type contentFixture struct {
	*httptest.Server
	authUser, authPass string
	manifestAccept     string
}

func newContentFixture(t *testing.T, catalog []string) *contentFixture {
	t.Helper()
	f := &contentFixture{authUser: "proj-user", authPass: "proj-pass"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/_catalog", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != f.authUser || pass != f.authPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": catalog}) //nolint:errcheck // 测试上游
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != f.authUser || pass != f.authPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/tags/list"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "proj/app", "tags": []string{"r0", "r1", "latest"}}) //nolint:errcheck // 测试上游
		case strings.Contains(path, "/manifests/"):
			f.manifestAccept = r.Header.Get("Accept")
			if r.Header.Get("Accept") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
			_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfg","size":120},"layers":[{"size":300},{"size":500}]}`))
		case strings.Contains(path, "/blobs/sha256:cfg"):
			_, _ = w.Write([]byte(`{"created":"2026-10-10T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func TestContentCatalog(t *testing.T) {
	f := newContentFixture(t, []string{"b/proj/app", "a/proj/other", "unrelated/repo"})
	p, err := New(f.Listener.Addr().String(), t.TempDir())
	require.NoError(t, err)
	ep := capability.RegistryEndpoint{
		Addr: f.Listener.Addr().String(),
		Cred: capability.RegistryCredential{Username: f.authUser, Secret: f.authPass},
	}
	repos, err := p.Catalog(context.Background(), ep)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/proj/other", "b/proj/app", "unrelated/repo"}, repos, "catalog returns the upstream list sorted (project filtering is the engine layer's responsibility)")
}

func TestContentTags(t *testing.T) {
	f := newContentFixture(t, nil)
	p, err := New(f.Listener.Addr().String(), t.TempDir())
	require.NoError(t, err)
	ep := capability.RegistryEndpoint{
		Addr: f.Listener.Addr().String(),
		Cred: capability.RegistryCredential{Username: f.authUser, Secret: f.authPass},
	}
	tags, err := p.Tags(context.Background(), ep, "proj/app")
	require.NoError(t, err)
	require.Len(t, tags, 3)
	for _, tag := range tags {
		assert.Equal(t, "sha256:deadbeef", tag.Digest)
		assert.Equal(t, int64(920), tag.SizeBytes, "size = config + layers (compressed form)")
		assert.Equal(t, "2026-10-10T00:00:00Z", tag.PushedAt)
	}
	assert.Contains(t, f.manifestAccept, "application/vnd.oci.image.manifest.v1+json")
}

func TestContentUpstreamAuthRejected(t *testing.T) {
	f := newContentFixture(t, nil)
	p, err := New(f.Listener.Addr().String(), t.TempDir())
	require.NoError(t, err)
	ep := capability.RegistryEndpoint{
		Addr: f.Listener.Addr().String(),
		Cred: capability.RegistryCredential{Username: "wrong", Secret: "nope"},
	}
	_, err = p.Catalog(context.Background(), ep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")

	_, err = p.Tags(context.Background(), ep, "proj/app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}
