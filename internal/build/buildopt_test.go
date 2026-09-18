package build

// buildopt 层单测（T2.8 验收「provenance 关闭参数断言」）：两路 solve 选项
// 的纯构造断言——导出恒为 docker 导出器（结构性无 attestation）、frontend
// attrs 恒无 attest:* 请求（显式面）、缓存条目、secrets-hash 戳。这些是
// Spike A #9 硬约束（attestation manifest list 与 swarm 本地 digest 引用
// 不兼容）的回归防线。测试与被测对象同包，可直接断言 buildkit 选项类型
// （选项构造属适配层行为）。

import (
	"io"
	"strings"
	"testing"

	bkclient "github.com/moby/buildkit/client"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// composeService 构造裁决用服务。
func composeService(hasBuild bool, dockerfile, image string) compose.Service {
	svc := compose.Service{Name: "web", Image: image}
	if hasBuild {
		svc.Build = &compose.Build{Context: ".", Dockerfile: dockerfile}
	}
	return svc
}

// testRequest 构造合法请求（ContextDir 由 fsutil.NewFS 在构造时校验存在；
// 测试用 t.TempDir 满足）。
func testRequest(t *testing.T, driver state.Driver) Request {
	t.Helper()
	return Request{
		BuildID:    "01TESTBUILD0000000000000000",
		AppID:      "01TESTAPP000000000000000000",
		AppName:    "my-api",
		Service:    "web",
		Driver:     driver,
		ContextDir: t.TempDir(),
		Dockerfile: "Dockerfile",
		SpecHash:   "spec-hash-1",
	}
}

// assertNoAttestationRequest 断言 solve 选项不含任何 attestation 请求面：
// frontend attrs 无 attest:*/provenance/sbom 键；导出恰为一条 docker 导出
// 器条目且未在构造层接 Output 管道。
func assertNoAttestationRequest(t *testing.T, opts bkclient.SolveOpt) {
	t.Helper()
	for k := range opts.FrontendAttrs {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "attest") || strings.Contains(lower, "provenance") || strings.Contains(lower, "sbom") {
			t.Fatalf("frontend attr %q requests attestation behavior (Spike A #9: must stay off)", k)
		}
	}
	if len(opts.Exports) != 1 {
		t.Fatalf("exports = %d, want exactly 1 (docker exporter)", len(opts.Exports))
	}
	if opts.Exports[0].Type != exporterDocker {
		t.Fatalf("exporter = %q, want %q（docker 导出器 + 客户端管道结构性无 attestation）",
			opts.Exports[0].Type, exporterDocker)
	}
	if opts.Exports[0].Output != nil {
		t.Fatal("export Output must be wired only at run time（buildopt 层保持纯构造）")
	}
	for _, e := range opts.Exports {
		for k := range e.Attrs {
			if strings.Contains(strings.ToLower(k), "attestation") {
				t.Fatalf("export attr %q is attestation-related", k)
			}
		}
	}
}

// TestDockerfileSolveOptionsProvenanceOff Dockerfile 前端路径：provenance/
// sbom 关闭 + filename 注入 + secrets-hash 戳 + 本地缓存 + secrets session。
func TestDockerfileSolveOptionsProvenanceOff(t *testing.T) {
	req := testRequest(t, state.DriverDockerfile)
	req.Secrets = map[string]string{"NPM_TOKEN": "tok-a"}

	opts, err := dockerfileSolveOptions(req, "fleetly-local/my-api:tag1", "/cache")
	if err != nil {
		t.Fatalf("construct solve options: %v", err)
	}
	assertNoAttestationRequest(t, opts)

	if opts.Frontend != "dockerfile.v0" {
		t.Fatalf("frontend = %q, want dockerfile.v0", opts.Frontend)
	}
	if opts.FrontendAttrs["filename"] != "Dockerfile" {
		t.Fatalf("filename attr = %q", opts.FrontendAttrs["filename"])
	}
	// secrets-hash 戳（Spike A E2c）：凭证变化即失效；内容稳定即命中。
	stamp := opts.FrontendAttrs["build-arg:"+secretStampBuildArg]
	if stamp == "" || stamp != SecretsHash(req.Secrets) {
		t.Fatalf("secrets stamp attr missing/mismatched: %q", stamp)
	}
	if len(opts.CacheImports) != 1 || opts.CacheImports[0].Attrs["src"] != "/cache" {
		t.Fatalf("cache imports wrong: %+v", opts.CacheImports)
	}
	if len(opts.CacheExports) != 1 || opts.CacheExports[0].Attrs["dest"] != "/cache" {
		t.Fatalf("cache exports wrong: %+v", opts.CacheExports)
	}
	if len(opts.LocalMounts) != 2 {
		t.Fatalf("local mounts = %d, want 2 (context + dockerfile)", len(opts.LocalMounts))
	}
	if len(opts.Session) != 1 {
		t.Fatalf("session attachables = %d, want 1 (secrets provider)", len(opts.Session))
	}
}

// TestRailpackSolveOptionsProvenanceOff railpack LLB 路径：同样的关闭断言
// + 镜像配置随导出携带 + 无前端 attrs（LLB 直驱）。
func TestRailpackSolveOptionsProvenanceOff(t *testing.T) {
	req := testRequest(t, state.DriverRailpack)
	opts, err := railpackSolveOptions(req, "fleetly-local/my-api:tag1", `{"os":"linux"}`, "/cache")
	if err != nil {
		t.Fatalf("construct solve options: %v", err)
	}
	assertNoAttestationRequest(t, opts)

	if opts.Frontend != "" {
		t.Fatalf("frontend = %q, want empty (LLB direct)", opts.Frontend)
	}
	if opts.Exports[0].Attrs["containerimage.config"] == "" {
		t.Fatal("railpack export must carry containerimage.config（镜像配置已知）")
	}
	if opts.Exports[0].Attrs["name"] != "fleetly-local/my-api:tag1" {
		t.Fatalf("export name = %q", opts.Exports[0].Attrs["name"])
	}
}

// TestWireDockerExport 管道接入：docker 导出条目接 Output，返回 true。
func TestWireDockerExport(t *testing.T) {
	opts, err := railpackSolveOptions(testRequest(t, state.DriverRailpack), "t", "{}", "")
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	if !wireDockerExport(&opts, pw) {
		t.Fatal("docker export entry not found")
	}
	if opts.Exports[0].Output == nil {
		t.Fatal("Output not wired")
	}
	w, err := opts.Exports[0].Output(nil)
	if err != nil {
		t.Fatalf("output func: %v", err)
	}
	if w != io.WriteCloser(pw) {
		t.Fatal("output func must return the pipe writer verbatim")
	}
}

// TestCacheDisabledIsExplicitEmpty 缓存关闭形态：路径空 → 无缓存条目
// （显式关闭，而不是静默不设）。
func TestCacheDisabledIsExplicitEmpty(t *testing.T) {
	req := testRequest(t, state.DriverDockerfile)
	opts, err := dockerfileSolveOptions(req, "t", "")
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if len(opts.CacheImports) != 0 || len(opts.CacheExports) != 0 {
		t.Fatalf("cache entries = %v/%v, want none when path empty", opts.CacheImports, opts.CacheExports)
	}
}

// TestSecretsHashStableAndSensitive SecretsHash 契约（E2a 形态）：内容稳定
// → 哈希稳定；任一值/键变化 → 哈希变化；与顺序无关（修正上游 map 序缺陷）。
func TestSecretsHashStableAndSensitive(t *testing.T) {
	base := map[string]string{"A": "1", "B": "2"}
	if SecretsHash(base) != SecretsHash(map[string]string{"B": "2", "A": "1"}) {
		t.Fatal("hash must be order-independent")
	}
	if SecretsHash(base) == SecretsHash(map[string]string{"A": "1", "B": "3"}) {
		t.Fatal("value change must change hash")
	}
	if SecretsHash(base) == SecretsHash(map[string]string{"A": "1"}) {
		t.Fatal("key set change must change hash")
	}
	if SecretsHash(nil) != SecretsHash(map[string]string{}) {
		t.Fatal("empty sets must hash equal")
	}
}

// TestImageRefTraceable 镜像命名契约：`fleetly-local/<app>:<app>-<buildid>`
// 可追溯 tag（小写化），D9 部署引用形态。
func TestImageRefTraceable(t *testing.T) {
	ref, err := ImageRef("My-Api", "01TESTBUILD0000000000000000")
	if err != nil {
		t.Fatalf("image ref: %v", err)
	}
	if ref != "fleetly-local/my-api:my-api-01testbuild0000000000000000" {
		t.Fatalf("image ref = %q", ref)
	}
	if _, err := ImageRef("bad/app", "x"); err == nil {
		t.Fatal("app name with slash must be rejected")
	}
}

// TestDriverForSelection 驱动裁决：dockerfile → Dockerfile；无 dockerfile
// 的 build → Railpack；仅 image → 直通（无构建）。
func TestDriverForSelection(t *testing.T) {
	cases := []struct {
		name      string
		hasBuild  bool
		dockerfil string
		image     string
		wantDrv   state.Driver
		wantBld   bool
	}{
		{"dockerfile mode", true, "deploy.Dockerfile", "", state.DriverDockerfile, true},
		{"railpack mode", true, "", "", state.DriverRailpack, true},
		{"image passthrough", false, "", "nginx:1", state.DriverPassthrough, false},
	}
	for _, tc := range cases {
		var svc = composeService(tc.hasBuild, tc.dockerfil, tc.image)
		drv, buildable, err := DriverFor(svc)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if drv != tc.wantDrv || buildable != tc.wantBld {
			t.Fatalf("%s: got (%s,%t), want (%s,%t)", tc.name, drv, buildable, tc.wantDrv, tc.wantBld)
		}
	}
}

// TestRequestRoundTrip 请求编解码往返（builds.request 列契约）。
func TestRequestRoundTrip(t *testing.T) {
	req := testRequest(t, state.DriverDockerfile)
	raw, err := req.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BuildID != req.BuildID || got.AppName != req.AppName || got.Service != req.Service ||
		got.Driver != req.Driver || got.ContextDir != req.ContextDir || got.Dockerfile != req.Dockerfile ||
		got.SpecHash != req.SpecHash || len(got.Secrets) != len(req.Secrets) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, req)
	}
	if _, err := DecodeRequest(`{"build_id":""}`); err == nil {
		t.Fatal("missing build_id must be rejected")
	}
}
