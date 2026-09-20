package substrate

// 平台 registry 前哨与凭据分发适配测试（E1-4/E1-5；httptest 代演 zot 的
// registry v2 面——真机 push/pull 验收由多节点演练票据承载）：
//   - ManifestHead：方法/路径/Accept/Basic Auth 断言 + Docker-Content-Digest
//     提取 + 404 归一 build.ErrImageNotFound + 非 200/传输错误原样失败；
//   - ImageDigest registry 分支：平台 registry 引用走 HEAD（底座镜像端口
//     不触达）；返回 manifest digest 供引擎 digest 钉定；
//   - registryAuthForImage / encodedRegistryAuth：--with-registry-auth 编码
//     只对平台 registry 引用产出，本地/外部镜像零凭据面。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/build"
)

// newRegistryTestClient 构造装配了平台 registry 的最小 Client（registry 面
// 指向 httptest 基址——携带 "://" 前缀即按绝对基址消费，生产形态无 scheme
// 默认 https；本测试不触 docker daemon，cli 为零值不被 registry 路径使用）。
func newRegistryTestClient(t *testing.T, base string, creds build.RegistryCredentials) *Client {
	t.Helper()
	return (&Client{}).WithPlatformRegistry(base, func() (build.RegistryCredentials, error) {
		return creds, nil
	})
}

func TestManifestHeadHitsV2PathWithBasicAuth(t *testing.T) {
	const wantDigest = "sha256:feedc0de0000000000000000000000000000000000000000000000000000feed"
	var (
		gotMethod string
		gotPath   string
		gotUser   string
		gotPass   string
		gotAccept bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		gotAccept = r.Header.Get("Accept") != ""
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newRegistryTestClient(t, srv.URL, build.RegistryCredentials{User: "u1", Password: "p1"})
	ref := srv.URL + "/apps/demo@sha256:abc" // host 位携带 scheme 的测试形态
	digest, err := c.ManifestHead(context.Background(), ref)
	if err != nil {
		t.Fatalf("manifest head: %v", err)
	}
	if digest != wantDigest {
		t.Fatalf("digest = %q, want %q", digest, wantDigest)
	}
	if gotMethod != http.MethodHead || gotPath != "/v2/apps/demo/manifests/sha256:abc" {
		t.Fatalf("request = %s %s, want HEAD /v2/apps/demo/manifests/sha256:abc", gotMethod, gotPath)
	}
	if gotUser != "u1" || gotPass != "p1" {
		t.Fatalf("basic auth = %s/%s", gotUser, gotPass)
	}
	if !gotAccept {
		t.Fatal("manifest HEAD must carry Accept manifest types")
	}
}

func TestManifestHeadMissingMapsToImageNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newRegistryTestClient(t, srv.URL, build.RegistryCredentials{User: "u1", Password: "p1"})
	_, err := c.ManifestHead(context.Background(), srv.URL+"/apps/ghost@sha256:abc")
	if !errors.Is(err, build.ErrImageNotFound) {
		t.Fatalf("err = %v, want build.ErrImageNotFound (manifest-missing sentinel)", err)
	}
	// 经 build.PreflightRegistry 归一后 = E_IMAGE_UNAVAILABLE 复用（D-MN-11）。
	_, perr := build.PreflightRegistry(context.Background(), c, srv.URL+"/apps/ghost@sha256:abc")
	var ae interface{ Error() string }
	if !errors.As(perr, &ae) || !strings.Contains(perr.Error(), "E_IMAGE_UNAVAILABLE") {
		t.Fatalf("preflight err = %v, want E_IMAGE_UNAVAILABLE envelope", perr)
	}
}

func TestManifestHeadUnreachableStaysRawForEnvelopeMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srvURL := srv.URL
	srv.Close() // 立即关闭：拨号失败 = 传输层错误（registry 不可达形态）

	c := newRegistryTestClient(t, srvURL, build.RegistryCredentials{User: "u1", Password: "p1"})
	_, err := c.ManifestHead(context.Background(), srvURL+"/apps/demo@sha256:abc")
	if err == nil || errors.Is(err, build.ErrImageNotFound) {
		t.Fatalf("err = %v, want raw transport error (sentinel only for missing manifests)", err)
	}
	// build.PreflightRegistry 把传输错误归一为 E_REGISTRY_UNAVAILABLE（503）。
	_, perr := build.PreflightRegistry(context.Background(), c, srvURL+"/apps/demo@sha256:abc")
	if perr == nil || !strings.Contains(perr.Error(), "E_REGISTRY_UNAVAILABLE") {
		t.Fatalf("preflight err = %v, want E_REGISTRY_UNAVAILABLE envelope", perr)
	}
}

func TestManifestHeadWithoutWiringFails(t *testing.T) {
	c := &Client{}
	if _, err := c.ManifestHead(context.Background(), "registry.example.com/apps/demo@sha256:abc"); err == nil {
		t.Fatal("unwired client must refuse registry preflight (single-node has no platform registry)")
	}
}

func TestImageDigestRegistryBranchUsesHead(t *testing.T) {
	const wantDigest = "sha256:deadbeef0000000000000000000000000000000000000000000000000000beef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/apps/demo/manifests/sha256:abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newRegistryTestClient(t, srv.URL, build.RegistryCredentials{User: "u1", Password: "p1"})
	digest, err := c.ImageDigest(context.Background(), srv.URL+"/apps/demo@sha256:abc")
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	if digest != wantDigest {
		t.Fatalf("digest = %q, want %q (manifest digest feeds the engine digest pin)", digest, wantDigest)
	}
}

func TestRegistryAuthForImageGating(t *testing.T) {
	creds := build.RegistryCredentials{User: "u1", Password: "p1"}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := newRegistryTestClient(t, srv.URL, creds)

	// 平台 registry 引用 → X-Registry-Auth 编码头。
	auth, err := c.registryAuthForImage(srv.URL + "/apps/demo@sha256:abc")
	if err != nil || auth == "" {
		t.Fatalf("auth = %q, %v; want encoded header for platform registry ref", auth, err)
	}
	raw, derr := base64.URLEncoding.DecodeString(auth)
	if derr != nil {
		t.Fatalf("auth is not base64url: %v", derr)
	}
	var decoded struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatalf("auth payload is not AuthConfig JSON: %v", uerr)
	}
	if decoded.Username != "u1" || decoded.Password != "p1" {
		t.Fatalf("decoded auth = %+v", decoded)
	}

	// 本地/外部镜像 → 零凭据面（不向全集群广播）。
	for _, ref := range []string{"fleetly-local/demo:demo-b01", "nginx:1.27", ""} {
		auth, err := c.registryAuthForImage(ref)
		if err != nil || auth != "" {
			t.Fatalf("registryAuthForImage(%q) = %q, %v; want empty", ref, auth, err)
		}
	}

	// 凭据读取失败显式报错（fail-closed：部署中途拉取失败比入队失败更难定位）。
	broken := (&Client{}).WithPlatformRegistry(srv.URL, func() (build.RegistryCredentials, error) {
		return build.RegistryCredentials{}, fmt.Errorf("auth file missing")
	})
	if _, err := broken.registryAuthForImage(srv.URL + "/apps/demo@sha256:abc"); err == nil {
		t.Fatal("unreadable credentials must fail the service write")
	}
}
