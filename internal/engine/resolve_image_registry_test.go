package engine

// 补线（W2 阶段 4，multi-node §5.2/D-MN-11 前哨码面保真）单测：
// resolveImage 对已是 apperr 信封的 E_REGISTRY_UNAVAILABLE 原样透传，不被
// E_RUNTIME_UNAVAILABLE 包装——deploy 路径上「registry 错 → 查 zot/网络/
// 凭据」的修复建议分层直达调用方；引擎其余错误包装语义不变。

import (
	"errors"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// resolveRegistryFixture 构造最小 resolveImage 输入（真实 compose 解析；
// state 行仅为签名完整性——image 模式不读 builds 表）。
func resolveRegistryFixture(t *testing.T, h *harness, image string) (state.DeployRecord, *compose.Service) {
	t.Helper()
	path := h.writeCompose("name: demo\nservices:\n  web:\n    image: " + image + "\n")
	spec, _, err := compose.Load(h.t.Context(), path)
	if err != nil {
		t.Fatalf("load compose: %v", err)
	}
	rec := state.DeployRecord{AppID: "app-x", AppName: "demo", SpecHash: spec.SpecHash}
	// builds 行有 apps 外键：build 模式用例先落真实应用行（enqueue 同款）。
	app, err := ensureAppForTest(h.t.Context(), h.store, "demo")
	if err != nil {
		t.Fatalf("ensure app: %v", err)
	}
	rec.AppID = app.ID
	return rec, &spec.Services[0]
}

// TestResolveImagePassesThroughRegistryPreflightEnvelope：image 模式（build
// 为 nil）与 build 模式（builds 行 digest 复核）两条腿都透传前哨信封。
func TestResolveImagePassesThroughRegistryPreflightEnvelope(t *testing.T) {
	cases := []struct {
		name  string
		image string
		build bool
	}{
		{name: "image mode", image: "registry.example.test/apps/demo@sha256:" + strings.Repeat("a", 64)},
		{name: "build mode", image: "registry.example.test/apps/demo:latest", build: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.images.registryErrs = map[string]bool{
				"registry.example.test/apps/demo@sha256:" + strings.Repeat("a", 64): true,
				"registry.example.test/apps/demo:latest":                            true,
			}
			rec, svc := resolveRegistryFixture(t, h, tc.image)

			// build 模式先落一条匹配 spec_hash 的成功构建（builds 行 digest
			// 复核路径触达同一镜像端口）：queued 行经 FinishBuildSucceeded
			// 推进为 succeeded（生产同路径）。
			if tc.build {
				b, err := h.store.CreateBuild(t.Context(), state.BuildRecord{
					AppID: rec.AppID, Service: svc.Name, Driver: state.DriverRailpack,
					Request: `{"spec_hash":"` + rec.SpecHash + `"}`,
				})
				if err != nil {
					t.Fatalf("seed build: %v", err)
				}
				if err := h.store.ClaimBuild(t.Context(), b.ID); err != nil {
					t.Fatalf("claim build: %v", err)
				}
				if err := h.store.FinishBuildSucceeded(t.Context(), b.ID,
					tc.image, "sha256:"+strings.Repeat("b", 64), "", ""); err != nil {
					t.Fatalf("finish build: %v", err)
				}
			}

			_, err := h.eng.resolveImage(t.Context(), rec, svc)
			if err == nil {
				t.Fatal("resolveImage err = nil, want registry preflight envelope")
			}
			var ae *apperr.Error
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want apperr envelope", err)
			}
			if ae.Code() != "E_REGISTRY_UNAVAILABLE" {
				t.Fatalf("code = %s, want E_REGISTRY_UNAVAILABLE passthrough (got wrapped: %v)", ae.Code(), err)
			}
			if ae.Envelope().GetStage() != "preflight" {
				t.Fatalf("stage = %q, want preflight (envelope must pass through verbatim)", ae.Envelope().GetStage())
			}
		})
	}
}

// TestResolveImageStillWrapsOtherErrors：非前哨信封的传输类失败维持既有
// E_RUNTIME_UNAVAILABLE 包装（补线只放行一种码，语义面零扩张）。
func TestResolveImageStillWrapsOtherErrors(t *testing.T) {
	h := newHarness(t)
	h.images.registryErrs = map[string]bool{}
	h.images.missing = map[string]bool{"plain.example.test/img:1": true}
	// missing 命中 ErrImageMissing → E_IMAGE_PULL_FAILED（既有语义不变）。
	rec, svc := resolveRegistryFixture(t, h, "plain.example.test/img:1")
	_, err := h.eng.resolveImage(t.Context(), rec, svc)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_IMAGE_PULL_FAILED" {
		t.Fatalf("err = %v, want E_IMAGE_PULL_FAILED (existing semantics)", err)
	}

	// 传输类失败（非 missing、非前哨信封）→ 既有 E_RUNTIME_UNAVAILABLE 包装。
	h.images.missing = map[string]bool{}
	h.images.failOther = map[string]bool{"plain.example.test/img:1": true}
	_, err = h.eng.resolveImage(t.Context(), rec, svc)
	if !errors.As(err, &ae) || ae.Code() != "E_RUNTIME_UNAVAILABLE" {
		t.Fatalf("err = %v, want E_RUNTIME_UNAVAILABLE wrap (existing semantics)", err)
	}
}
