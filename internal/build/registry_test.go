package build

// registry 模式镜像管线测试（E1-5；E1 多节点设计 §2.5/D-MN-11）：
//   - 命名契约：repo/tag/digest 三形态 + IsRegistryImageRef 判定（host 为
//     空/本地引用不命中——v0.1 等价的谓词面）；
//   - 凭据：load-or-parse 单行 user:password（损坏/缺失显式失败）；
//   - 前哨双模式 registry 腿：PreflightRegistry 的三分归因（命中/manifest
//     缺→E_IMAGE_UNAVAILABLE 复用/不可达→E_REGISTRY_UNAVAILABLE 503）；
//   - 推送失败归因：ClassifyRegistryPushError（buildkit 推送标记 →
//     E_REGISTRY_PUSH_FAILED；构建本体错误 → E_BUILD_FAILED 原语义）；
//   - 端到端记账：registry 模式 Execute（假 solve 执行器）落
//     registry.<base>/apps/<app>@sha256:<digest>；solve 无 digest、凭据
//     缺失、推送失败三条失败路径的信封与 builds 行终态；
//   - 本地模式等价：同构假执行器下 Execute 仍落 fleetly-local 引用 + 本机
//     digest（run 的 digest 返回值不改变本地记账语义）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/errcode"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func TestRegistryRefForms(t *testing.T) {
	repo, err := RegistryRepo("registry.example.com", "Demo")
	if err != nil || repo != "registry.example.com/apps/demo" {
		t.Fatalf("RegistryRepo = %q, %v", repo, err)
	}
	tagRef, err := RegistryBuildRef("registry.example.com", "demo", "01HQ")
	if err != nil || tagRef != "registry.example.com/apps/demo:b01HQ" {
		t.Fatalf("RegistryBuildRef = %q, %v", tagRef, err)
	}
	digestRef, err := RegistryDigestRef("registry.example.com", "demo", "abc123")
	if err != nil || digestRef != "registry.example.com/apps/demo@sha256:abc123" {
		t.Fatalf("RegistryDigestRef = %q, %v", digestRef, err)
	}
	// 已带 sha256: 前缀的 digest 不重复加前缀。
	prefixed, err := RegistryDigestRef("registry.example.com", "demo", "sha256:abc123")
	if err != nil || prefixed != "registry.example.com/apps/demo@sha256:abc123" {
		t.Fatalf("RegistryDigestRef(prefixed) = %q, %v", prefixed, err)
	}
	// 空 host（本地模式）/空 digest/空 build id 显式失败。
	if _, err := RegistryRepo("", "demo"); err == nil {
		t.Fatal("empty host must be rejected")
	}
	if _, err := RegistryDigestRef("registry.example.com", "demo", ""); err == nil {
		t.Fatal("empty digest must be rejected")
	}
	if _, err := RegistryBuildRef("registry.example.com", "demo", ""); err == nil {
		t.Fatal("empty build id must be rejected")
	}
}

func TestIsRegistryImageRef(t *testing.T) {
	const host = "registry.example.com"
	cases := []struct {
		ref  string
		want bool
	}{
		{"registry.example.com/apps/demo@sha256:abc", true},
		{"registry.example.com/apps/demo:b01", false},              // tag 形态不是产物引用
		{"registry.example.com/other/demo@sha256:abc", false},      // 路径前缀不符
		{"other-registry.example.com/apps/demo@sha256:abc", false}, // host 不符
		{"fleetly-local/demo:demo-b01", false},                     // v0.1 本地引用
		{"nginx:1.27", false},                                      // 外部镜像
		{"", false},
	}
	for _, tc := range cases {
		if got := IsRegistryImageRef(tc.ref, host); got != tc.want {
			t.Errorf("IsRegistryImageRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
	// host 为空恒 false（本地模式无 registry 引用）。
	if IsRegistryImageRef("registry.example.com/apps/demo@sha256:abc", "") {
		t.Fatal("empty host must never match")
	}
}

func TestLoadRegistryCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleetly-registry.auth")
	if err := os.WriteFile(path, []byte("fleetly-ab12:s3cret-value\n"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	creds, err := LoadRegistryCredentials(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if creds.User != "fleetly-ab12" || creds.Password != "s3cret-value" {
		t.Fatalf("creds = %+v", creds)
	}
	// 缺失文件显式失败。
	if _, err := LoadRegistryCredentials(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing credentials file must fail")
	}
	// 损坏形态显式失败（无分隔/多行均拒）。
	badDir := t.TempDir()
	for _, content := range []string{"no-separator", "u:p\nu2:p2"} {
		bad := filepath.Join(badDir, "bad.auth")
		if werr := os.WriteFile(bad, []byte(content), 0o600); werr != nil {
			t.Fatalf("write bad creds: %v", werr)
		}
		if _, lerr := LoadRegistryCredentials(bad); lerr == nil {
			t.Fatalf("malformed credentials %q must fail", content)
		}
	}
}

// fakeRegistryHead 是 RegistryClient 的假实现（按注入 digest/错误响应）。
type fakeRegistryHead struct {
	digest string
	err    error
	calls  int
}

func (f *fakeRegistryHead) ManifestHead(context.Context, string) (string, error) {
	f.calls++
	return f.digest, f.err
}

func TestPreflightRegistryHit(t *testing.T) {
	head := &fakeRegistryHead{digest: "sha256:feedc0de"}
	res, err := PreflightRegistry(context.Background(), head, "registry.example.com/apps/demo@sha256:feedc0de")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !res.Available || res.Digest != "sha256:feedc0de" || res.Warning != "" {
		t.Fatalf("result = %+v", res)
	}
	if head.calls != 1 {
		t.Fatalf("head calls = %d, want 1", head.calls)
	}
}

func TestPreflightRegistryManifestMissingReusesImageUnavailable(t *testing.T) {
	head := &fakeRegistryHead{err: fmt.Errorf("wrapped: %w", ErrImageNotFound)}
	ref := "registry.example.com/apps/demo@sha256:feedc0de"
	_, err := PreflightRegistry(context.Background(), head, ref)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_IMAGE_UNAVAILABLE" {
		t.Fatalf("err = %v, want E_IMAGE_UNAVAILABLE envelope (D-MN-11: missing manifest reuses the rollback-target semantics)", err)
	}
	if ae.Context()["warning"] != WarningRollbackImageRisk {
		t.Fatalf("warning context = %v, want %s", ae.Context()["warning"], WarningRollbackImageRisk)
	}
}

func TestPreflightRegistryUnreachableFailsFastWith503(t *testing.T) {
	head := &fakeRegistryHead{err: errors.New("dial tcp 10.0.0.1:443: connection refused")}
	ref := "registry.example.com/apps/demo@sha256:feedc0de"
	_, err := PreflightRegistry(context.Background(), head, ref)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_REGISTRY_UNAVAILABLE" {
		t.Fatalf("err = %v, want E_REGISTRY_UNAVAILABLE envelope", err)
	}
	if got := errcode.HTTPStatus(ae.Code()); got != 503 {
		t.Fatalf("HTTP mapping of %s = %d, want 503 (design §5.2)", ae.Code(), got)
	}
	if ae.Envelope().GetSuggestion() == "" {
		t.Fatal("envelope must carry a registry-side suggestion (fix-path layering)")
	}
}

func TestClassifyRegistryPushError(t *testing.T) {
	push := ClassifyRegistryPushError(errors.New("failed to solve: failed to push registry.example.com/apps/demo: unauthorized"))
	var ae *apperr.Error
	if !errors.As(push, &ae) || ae.Code() != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("err = %v, want E_REGISTRY_PUSH_FAILED envelope", push)
	}
	if got := errcode.HTTPStatus(ae.Code()); got != 500 {
		t.Fatalf("HTTP mapping of %s = %d, want 500 (design §5.2)", ae.Code(), got)
	}
	// 构建本体错误不挪用推送码（修复建议分层：代码错 → 改代码）。
	plain := ClassifyRegistryPushError(errors.New("dockerfile parse error line 3"))
	var ae2 *apperr.Error
	if errors.As(plain, &ae2) && ae2.Code() == "E_REGISTRY_PUSH_FAILED" {
		t.Fatal("non-push solve error must not be classified as a push failure")
	}
	if ClassifyRegistryPushError(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
}

// fakeSolver 是 solveExecutor 的假实现（记录推送选项改写，返回注入
// digest/错误——registry 模式记账断言不触 buildkit）。
type fakeSolver struct {
	digest   string
	err      error
	lastOpts []bkclient.SolveOpt
}

func (f *fakeSolver) solveLLB(_ context.Context, opts bkclient.SolveOpt, _ *llb.Definition, _ io.Writer) (string, error) {
	f.lastOpts = append(f.lastOpts, opts)
	return f.digest, f.err
}

func (f *fakeSolver) solveFrontend(_ context.Context, opts bkclient.SolveOpt, _ io.Writer) (string, error) {
	f.lastOpts = append(f.lastOpts, opts)
	return f.digest, f.err
}

// okImages 是成功路径的镜像端口（本地模式 digest 取此端口；registry 模式
// 不触达——推送产物不装载本机）。
type okImages struct{ id string }

func (o okImages) InspectImage(context.Context, string) (ImageInfo, error) {
	return ImageInfo{ID: o.id}, nil
}

func (o okImages) LoadImage(context.Context, io.Reader) error { return nil }

// newRegistryTestBuilder 构造 registry 模式 Builder（假 solver + 独立凭据
// 文件 + 独立产物目录）。
func newRegistryTestBuilder(t *testing.T, st *state.Store, solver *fakeSolver) (*Builder, string) {
	t.Helper()
	dir := t.TempDir()
	authFile := filepath.Join(dir, "fleetly-registry.auth")
	if err := os.WriteFile(authFile, []byte("fleetly-test:push-pass\n"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	b := NewBuilder(Config{
		ArtifactsDir:     filepath.Join(dir, "artifacts"),
		RegistryHost:     "registry.example.com",
		RegistryAuthFile: authFile,
	}, st, okImages{id: "sha256:localconfig"}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.solver = solver
	return b, authFile
}

// enqueueClaimedBuild 建应用并入队认领一条 dockerfile 驱动构建（Execute 的
// 契约前置态；builds.app_id 外键要求应用行真实存在。dockerfile 驱动的
// solve 选项构造不触 railpack 计划生成——假执行器即可拦截，测试不触
// buildkit/网络）。
func enqueueClaimedBuild(t *testing.T, b *Builder, st *state.Store, appName, service string) state.BuildRecord {
	t.Helper()
	app, err := st.CreateApp(context.Background(), "", appName)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	raw, err := Request{
		BuildID: "rb01", AppID: app.ID, AppName: appName,
		Service: service, Driver: state.DriverDockerfile,
		ContextDir: t.TempDir(), Dockerfile: "Dockerfile",
	}.Encode()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: service, Driver: state.DriverDockerfile, Request: raw,
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim build: %v", err)
	}
	rec, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("reload build: %v", err)
	}
	return rec
}

func TestBuilderRegistryModeRecordsRegistryDigestRef(t *testing.T) {
	st := newQueueTestStore(t)
	solver := &fakeSolver{digest: "deadbeef"} // buildkit 回填形态允许无前缀
	b, _ := newRegistryTestBuilder(t, st, solver)
	rec := enqueueClaimedBuild(t, b, st, "demo", "web")

	updated, execErr := b.Execute(context.Background(), rec)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	// 记账契约（设计 §2.5）：image_ref = registry.<base>/apps/<app>@sha256:<digest>。
	if updated.ImageRef != "registry.example.com/apps/demo@sha256:deadbeef" {
		t.Fatalf("image_ref = %q, want registry digest ref", updated.ImageRef)
	}
	if updated.ImageDigest != "sha256:deadbeef" {
		t.Fatalf("image_digest = %q, want normalized manifest digest", updated.ImageDigest)
	}
	if updated.Status != state.BuildSucceeded {
		t.Fatalf("status = %s, want succeeded", updated.Status)
	}
	// solve 选项改写断言：导出替换为 image push=true 的 registry 构建引用，
	// 并附凭据 session；本机装载（docker 导出）不再发生。
	if len(solver.lastOpts) != 1 {
		t.Fatalf("solve calls = %d, want 1", len(solver.lastOpts))
	}
	opts := solver.lastOpts[0]
	if len(opts.Exports) != 1 || opts.Exports[0].Type != bkclient.ExporterImage {
		t.Fatalf("exports = %+v, want single image exporter", opts.Exports)
	}
	if opts.Exports[0].Attrs["name"] != "registry.example.com/apps/demo:brb01" {
		t.Fatalf("push name = %q, want registry build tag ref", opts.Exports[0].Attrs["name"])
	}
	if opts.Exports[0].Attrs["push"] != "true" {
		t.Fatalf("push attr = %q, want true", opts.Exports[0].Attrs["push"])
	}
	if len(opts.Session) == 0 {
		t.Fatal("registry push must attach a credential session")
	}
	if len(opts.CacheExports) == 0 {
		t.Fatal("local cache export must be preserved in registry mode (build cache stays local, design §2.5)")
	}
}

func TestBuilderRegistryModeNoDigestFailsAsPushFailure(t *testing.T) {
	st := newQueueTestStore(t)
	b, _ := newRegistryTestBuilder(t, st, &fakeSolver{digest: ""})
	rec := enqueueClaimedBuild(t, b, st, "nodigest", "web")

	_, execErr := b.Execute(context.Background(), rec)
	var ae *apperr.Error
	if !errors.As(execErr, &ae) || ae.Code() != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("err = %v, want E_REGISTRY_PUSH_FAILED (digest-less push is unverifiable accounting)", execErr)
	}
	row, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if row.Status != state.BuildFailed || row.ErrorCode != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("row = %s/%s, want failed/E_REGISTRY_PUSH_FAILED", row.Status, row.ErrorCode)
	}
}

func TestBuilderRegistryModeMissingCredentialsFailsAsPushFailure(t *testing.T) {
	st := newQueueTestStore(t)
	b, authFile := newRegistryTestBuilder(t, st, &fakeSolver{digest: "aa"})
	if err := os.Remove(authFile); err != nil {
		t.Fatalf("remove creds: %v", err)
	}
	rec := enqueueClaimedBuild(t, b, st, "nocreds", "web")

	_, execErr := b.Execute(context.Background(), rec)
	var ae *apperr.Error
	if !errors.As(execErr, &ae) || ae.Code() != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("err = %v, want E_REGISTRY_PUSH_FAILED (credentials are a push-side fault)", execErr)
	}
	if !strings.Contains(execErr.Error(), "credentials unavailable") {
		t.Fatalf("err = %v, want credentials attribution", execErr)
	}
}

func TestBuilderRegistryModePushErrorClassification(t *testing.T) {
	st := newQueueTestStore(t)
	b, _ := newRegistryTestBuilder(t, st, &fakeSolver{
		err: errors.New("failed to solve: failed to push: unexpected status 502"),
	})
	rec := enqueueClaimedBuild(t, b, st, "pushfail", "web")

	_, execErr := b.Execute(context.Background(), rec)
	var ae *apperr.Error
	if !errors.As(execErr, &ae) || ae.Code() != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("err = %v, want E_REGISTRY_PUSH_FAILED", execErr)
	}
	row, _ := st.GetBuild(context.Background(), rec.ID)
	if row.ErrorCode != "E_REGISTRY_PUSH_FAILED" {
		t.Fatalf("row error_code = %s, want E_REGISTRY_PUSH_FAILED", row.ErrorCode)
	}
}

// TestBuilderLocalModeAccountingUnchanged 本地模式金样：同构假执行器下
// digest 取本机 inspect（run 的 solve digest 返回值不得污染本地记账），
// image_ref 维持 fleetly-local 形态（v0.1 逐字等价）。
func TestBuilderLocalModeAccountingUnchanged(t *testing.T) {
	st := newQueueTestStore(t)
	dir := t.TempDir()
	b := NewBuilder(Config{ArtifactsDir: filepath.Join(dir, "artifacts")}, st,
		okImages{id: "sha256:configdigest"}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.solver = &fakeSolver{digest: "deadbeef"} // registry 语义的 digest 在本地模式必须被忽略
	rec := enqueueClaimedBuild(t, b, st, "localmode", "web")

	updated, execErr := b.Execute(context.Background(), rec)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	// 本地引用形态（ImageRef 公式：小写 app + 小写 rec.ID 组合 tag）。
	wantRef := "fleetly-local/localmode:localmode-" + strings.ToLower(rec.ID)
	if updated.ImageRef != wantRef {
		t.Fatalf("image_ref = %q, want v0.1 local ref form %q", updated.ImageRef, wantRef)
	}
	if updated.ImageDigest != "sha256:configdigest" {
		t.Fatalf("image_digest = %q, want local inspect id (v0.1 semantics)", updated.ImageDigest)
	}
}
