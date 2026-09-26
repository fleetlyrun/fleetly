package substrate

// IMPL-T1-2/DT-2 镜像代拉与凭证下发适配层测试（纯注入缝，无 daemon/无网络）：
//   - tag registry-first：解析命中即返回（本机 inspect 不触达）；
//   - 设置凭证命中/不命中两态（含 docker.io 归一匹配）；
//   - airgap 回归（守卫④）：registry 不可达 + 本机 inspect 命中 → 返回本机
//     digest 且回落留痕；
//   - 双失败：ErrImageMissing 包装三因（registry 原因 + 本机状态）；
//   - digest 直通与 fleetly-local 跳过解析腿（旧语义零网络）；
//   - registryAuthForImage 外部 host 命中的 X-Registry-Auth 编码。

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
	"time"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/imageregistry"
)

// traceRecorder 收集回落留痕。
type traceRecorder struct {
	entries []string
}

func (r *traceRecorder) trace(msg string, args ...any) {
	r.entries = append(r.entries, fmt.Sprintf(msg, args...))
}

// testImageClient 构造零值 Client 并装上注入缝（不触 docker daemon）。
func testImageClient(setup func(*Client)) *Client {
	c := &Client{}
	if setup != nil {
		setup(c)
	}
	return c
}

func TestImageDigestTagRegistryFirstUsesResolver(t *testing.T) {
	const want = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"
	var (
		resolverCalls int
		inspectCalls  int
		gotCreds      *imageregistry.Credentials
	)
	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(_ context.Context, ref imageregistry.Reference, creds *imageregistry.Credentials) (string, error) {
			resolverCalls++
			gotCreds = creds
			if ref.Host != "ghcr.io" || ref.Repository != "owner/app" || ref.Tag != "1.2.3" {
				t.Fatalf("resolver ref = %+v, want ghcr.io/owner/app:1.2.3", ref)
			}
			return want, nil
		}
		c.inspectDigest = func(context.Context, string) (string, error) {
			inspectCalls++
			return "", fmt.Errorf("must not inspect locally when registry resolution hits")
		}
	})
	got, err := c.ImageDigest(context.Background(), "ghcr.io/owner/app:1.2.3")
	if err != nil {
		t.Fatalf("ImageDigest: %v", err)
	}
	if got != want || resolverCalls != 1 || inspectCalls != 0 {
		t.Fatalf("digest/resolver/inspect = %q/%d/%d, want %q/1/0", got, resolverCalls, inspectCalls, want)
	}
	if gotCreds != nil {
		t.Fatalf("unconfigured host must resolve anonymously, got creds %+v", gotCreds)
	}

	// Redeploy 对可变 tag 重解析（DT-2）：每次调用都走 registry（不缓存
	// digest——同一 tag 应随 registry 内容演化重新钉定）。
	second, err := c.ImageDigest(context.Background(), "ghcr.io/owner/app:1.2.3")
	if err != nil || second != want || resolverCalls != 2 {
		t.Fatalf("second resolve = %q, %v (resolver calls %d), want re-resolution", second, err, resolverCalls)
	}
}

func TestImageDigestUsesSettingsCredentialsOnlyForMatchingHost(t *testing.T) {
	cases := []struct {
		name        string
		image       string
		settings    ExternalRegistrySettings
		wantCreds   bool
		wantHost    string
	}{
		{
			name:      "matching host",
			image:     "ghcr.io/owner/app:1.2.3",
			settings:  ExternalRegistrySettings{Host: "ghcr.io", Username: "robot", Password: "pw"},
			wantCreds: true,
			wantHost:  "ghcr.io",
		},
		{
			name:      "matching host case and scheme normalized",
			image:     "ghcr.io/owner/app:1.2.3",
			settings:  ExternalRegistrySettings{Host: "https://GHCR.io/", Username: "robot", Password: "pw"},
			wantCreds: true,
			wantHost:  "ghcr.io",
		},
		{
			name:      "docker hub alias matches the v2 endpoint",
			image:     "docker.io/someuser/app:edge",
			settings:  ExternalRegistrySettings{Host: "docker.io", Username: "robot", Password: "pw"},
			wantCreds: true,
			wantHost:  "registry-1.docker.io",
		},
		{
			name:      "non-matching host stays anonymous",
			image:     "quay.io/owner/app:1.2.3",
			settings:  ExternalRegistrySettings{Host: "ghcr.io", Username: "robot", Password: "pw"},
			wantCreds: false,
		},
		{
			name:      "host match without credentials stays anonymous",
			image:     "ghcr.io/owner/app:1.2.3",
			settings:  ExternalRegistrySettings{Host: "ghcr.io"},
			wantCreds: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCreds *imageregistry.Credentials
			var gotHost string
			c := testImageClient(func(c *Client) {
				c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
					return tc.settings, nil
				})
				c.resolveTagDigest = func(_ context.Context, ref imageregistry.Reference, creds *imageregistry.Credentials) (string, error) {
					gotCreds = creds
					gotHost = ref.Host
					return "sha256:" + strings.Repeat("2", 64), nil
				}
			})
			if _, err := c.ImageDigest(context.Background(), tc.image); err != nil {
				t.Fatalf("ImageDigest: %v", err)
			}
			if tc.wantCreds {
				if gotCreds == nil || gotCreds.Username != "robot" || gotCreds.Password != "pw" {
					t.Fatalf("resolver creds = %+v, want configured credentials", gotCreds)
				}
			} else if gotCreds != nil {
				t.Fatalf("resolver creds = %+v, want anonymous", gotCreds)
			}
			if tc.wantHost != "" && gotHost != tc.wantHost {
				t.Fatalf("resolver host = %q, want %q", gotHost, tc.wantHost)
			}
		})
	}
}

// TestImageDigestFallsBackToLocalInspectAirgap 守卫④（airgap 不回归）：
// registry 不可达 + 本机 inspect 命中 → 仍返回本机 digest，且回落有留痕。
func TestImageDigestFallsBackToLocalInspectAirgap(t *testing.T) {
	localDigest := "sha256:" + strings.Repeat("3", 64)
	trace := &traceRecorder{}
	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
			return "", fmt.Errorf("dial tcp: connection refused")
		}
		c.inspectDigest = func(_ context.Context, ref string) (string, error) {
			if ref != "nginx:1.27" {
				t.Fatalf("inspect ref = %q", ref)
			}
			return localDigest, nil
		}
		c.WithImageRegistryTrace(trace.trace)
	})
	got, err := c.ImageDigest(context.Background(), "nginx:1.27")
	if err != nil {
		t.Fatalf("ImageDigest (airgap): %v", err)
	}
	if got != localDigest {
		t.Fatalf("digest = %q, want local %q", got, localDigest)
	}
	if len(trace.entries) == 0 || !strings.Contains(trace.entries[0], "nginx:1.27") ||
		!strings.Contains(trace.entries[0], "connection refused") {
		t.Fatalf("fallback trace = %v, want image + registry reason", trace.entries)
	}
}

// TestImageDigestLocalBuildImageKeepsEmptyDigest 本机构建镜像（fleetly-local/…）
// 跳过解析腿且 inspect 无清单摘要 → 空串（旧语义：引擎按 tag 直用）。
func TestImageDigestLocalBuildImageKeepsEmptyDigest(t *testing.T) {
	var resolverCalls int
	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
			resolverCalls++
			return "", fmt.Errorf("must not resolve platform-local build refs")
		}
		c.inspectDigest = func(context.Context, string) (string, error) {
			return "", nil // 本机构建镜像无 RepoDigests 清单摘要
		}
	})
	got, err := c.ImageDigest(context.Background(), "fleetly-local/demo:demo-b01")
	if err != nil {
		t.Fatalf("ImageDigest (local build): %v", err)
	}
	if got != "" {
		t.Fatalf("digest = %q, want empty (local build tag kept by the engine)", got)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0 (platform-local namespace skips the registry leg)", resolverCalls)
	}
}

// TestImageDigestBothLegsFailWrapsImageMissing 双失败：registry 原因与本机
// 状态都进错误文本，errors.Is 链路命中 engine.ErrImageMissing。
func TestImageDigestBothLegsFailWrapsImageMissing(t *testing.T) {
	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
			return "", fmt.Errorf("registry.example.test: manifest missing")
		}
		c.inspectDigest = func(_ context.Context, ref string) (string, error) {
			return "", fmt.Errorf("%w: %s", engine.ErrImageMissing, ref)
		}
	})
	_, err := c.ImageDigest(context.Background(), "registry.example.test/private/app:v1")
	if !errors.Is(err, engine.ErrImageMissing) {
		t.Fatalf("err = %v, want engine.ErrImageMissing", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "registry.example.test") || !strings.Contains(msg, "manifest missing") ||
		!strings.Contains(msg, "local image lookup failed") {
		t.Fatalf("wrapped error must name image + registry reason + local state: %v", err)
	}
}

// TestImageDigestSettingsReadFailureFallsBackExplicitly 设置读取/解密失败
// 不得静默按匿名解析：原因入留痕；本机亦缺失时原因进最终错误。
func TestImageDigestSettingsReadFailureFallsBackExplicitly(t *testing.T) {
	localDigest := "sha256:" + strings.Repeat("4", 64)
	trace := &traceRecorder{}
	var resolverCalls int
	c := testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{}, fmt.Errorf("master key mismatch")
		})
		c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
			resolverCalls++
			return "sha256:" + strings.Repeat("5", 64), nil
		}
		c.inspectDigest = func(context.Context, string) (string, error) { return localDigest, nil }
		c.WithImageRegistryTrace(trace.trace)
	})
	got, err := c.ImageDigest(context.Background(), "ghcr.io/owner/app:1")
	if err != nil || got != localDigest {
		t.Fatalf("ImageDigest = %q, %v; want local fallback", got, err)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0 (settings failure must not silently resolve anonymously)", resolverCalls)
	}
	if len(trace.entries) == 0 || !strings.Contains(trace.entries[0], "master key mismatch") {
		t.Fatalf("trace = %v, want settings failure reason", trace.entries)
	}

	// 本机亦缺失：最终错误点名设置读取失败。
	c = testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{}, fmt.Errorf("master key mismatch")
		})
		c.inspectDigest = func(_ context.Context, ref string) (string, error) {
			return "", fmt.Errorf("%w: %s", engine.ErrImageMissing, ref)
		}
	})
	_, err = c.ImageDigest(context.Background(), "ghcr.io/owner/app:1")
	if err == nil || !strings.Contains(err.Error(), "master key mismatch") {
		t.Fatalf("err = %v, want settings failure inside the wrapped reason", err)
	}
}

// TestImageDigestDigestRefShortCircuits digest 钉定引用免网络免 inspect 直通。
func TestImageDigestDigestRefShortCircuits(t *testing.T) {
	digest := "sha256:" + strings.Repeat("6", 64)
	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
			t.Fatal("digest refs must not hit the registry")
			return "", nil
		}
		c.inspectDigest = func(context.Context, string) (string, error) {
			t.Fatal("digest refs must not hit the local inspect")
			return "", nil
		}
	})
	got, err := c.ImageDigest(context.Background(), "ghcr.io/owner/app:1.2.3@"+digest)
	if err != nil || got != digest {
		t.Fatalf("ImageDigest = %q, %v; want direct digest passthrough", got, err)
	}
}

// TestRegistryAuthForImageExternalHostMatching registryAuthForImage 外部
// host 命中/不命中两态：命中 = 设置凭证编码（ServerAddress = 引用 host）；
// 不命中/无凭证/未装配 = 空串；设置读取失败显式报错。
func TestRegistryAuthForImageExternalHostMatching(t *testing.T) {
	c := testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{Host: "ghcr.io", Username: "robot", Password: "s3cret"}, nil
		})
	})
	auth, err := c.registryAuthForImage("ghcr.io/owner/app:1.2.3")
	if err != nil || auth == "" {
		t.Fatalf("auth = %q, %v; want encoded header for the configured host", auth, err)
	}
	raw, derr := base64.URLEncoding.DecodeString(auth)
	if derr != nil {
		t.Fatalf("auth is not base64url: %v", derr)
	}
	var decoded struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		ServerAddress string `json:"serveraddress"`
	}
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatalf("auth payload is not AuthConfig JSON: %v", uerr)
	}
	if decoded.Username != "robot" || decoded.Password != "s3cret" || decoded.ServerAddress != "ghcr.io" {
		t.Fatalf("decoded auth = %+v", decoded)
	}

	// 不命中（其它 registry / fleetly-local / 未配置）→ 零凭据面。
	for _, ref := range []string{"quay.io/owner/app:1", "fleetly-local/demo:b1", "alpine:3"} {
		if got, err := c.registryAuthForImage(ref); err != nil || got != "" {
			t.Fatalf("registryAuthForImage(%q) = %q, %v; want empty", ref, got, err)
		}
	}
	// host 命中但无凭证 → 空串。
	anon := testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{Host: "ghcr.io"}, nil
		})
	})
	if got, err := anon.registryAuthForImage("ghcr.io/owner/app:1"); err != nil || got != "" {
		t.Fatalf("credential-less host match = %q, %v; want empty", got, err)
	}
	// docker.io 家族归一后命中。
	dockerHub := testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{Host: "docker.io", Username: "hub", Password: "pw"}, nil
		})
	})
	if got, err := dockerHub.registryAuthForImage("docker.io/someuser/app:edge"); err != nil || got == "" {
		t.Fatalf("docker.io auth = %q, %v; want encoded header via the v2 endpoint", got, err)
	}
	// 设置读取失败显式报错（fail-closed）。
	broken := testImageClient(func(c *Client) {
		c.WithImageRegistryCredentials(func() (ExternalRegistrySettings, error) {
			return ExternalRegistrySettings{}, fmt.Errorf("box unavailable")
		})
	})
	if _, err := broken.registryAuthForImage("ghcr.io/owner/app:1"); err == nil {
		t.Fatal("unreadable registry settings must fail the service write")
	}
}

// TestImageDigestRegistryLegBoundedByBudget 解析腿预算（D2 同口径）：黑洞
// registry（永不响应）在 externalRegistryResolveTimeout 内终结，不挂满
// 调用方 ctx；双失败仍落 ErrImageMissing 包装（原因含 deadline）。
func TestImageDigestRegistryLegBoundedByBudget(t *testing.T) {
	orig := externalRegistryResolveTimeout
	externalRegistryResolveTimeout = time.Second
	t.Cleanup(func() { externalRegistryResolveTimeout = orig })

	c := testImageClient(func(c *Client) {
		c.resolveTagDigest = func(ctx context.Context, _ imageregistry.Reference, _ *imageregistry.Credentials) (string, error) {
			<-ctx.Done() // 黑洞：只在预算耗尽时返回
			return "", ctx.Err()
		}
		c.inspectDigest = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("%w: no local image", engine.ErrImageMissing)
		}
	})
	start := time.Now()
	_, err := c.ImageDigest(context.Background(), "ghcr.io/owner/app:1")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("registry leg not bounded by its budget: elapsed %v", elapsed)
	}
	if !errors.Is(err, engine.ErrImageMissing) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want ErrImageMissing wrapping the deadline reason", err)
	}
}

// TestImageDigestPlatformRegistryBranchKeepsManifestHeadPrecedence 平台
// registry 腿次序不变（前哨优先于 digest 直通与外部解析——引用形态含 @
// 也必须走 manifest HEAD 现场核验）。
func TestImageDigestPlatformRegistryBranchKeepsManifestHeadPrecedence(t *testing.T) {
	wantDigest := "sha256:" + strings.Repeat("7", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/apps/demo/manifests/sha256:abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", wantDigest)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newRegistryTestClient(t, srv.URL, build.RegistryCredentials{User: "u", Password: "p"})
	c.resolveTagDigest = func(context.Context, imageregistry.Reference, *imageregistry.Credentials) (string, error) {
		t.Fatal("platform registry refs must keep the manifest HEAD preflight")
		return "", nil
	}
	c.inspectDigest = func(context.Context, string) (string, error) {
		t.Fatal("platform registry refs must not fall back to local inspect")
		return "", nil
	}
	digest, err := c.ImageDigest(context.Background(), srv.URL+"/apps/demo@sha256:abc")
	if err != nil {
		t.Fatalf("ImageDigest: %v", err)
	}
	if digest != wantDigest {
		t.Fatalf("digest = %q, want manifest HEAD digest %q", digest, wantDigest)
	}
}
