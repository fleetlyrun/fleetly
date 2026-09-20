package build

// Builder 失败路径测试（T2.8 验收「失败 → E_BUILD_FAILED 带 stderr 与建议」）：
// 请求损坏 → E_BUILD_FAILED 信封（log_tail/log_path 进 context）+ builds 行
// failed 终态；正常构建失败路径由实机验收覆盖（本文件不触网）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// errImages 是失败路径测试的镜像端口（成功构建后的 inspect 分支不会被
// 这些用例触达；执行器契约要求实现完整端口）。
type errImages struct{}

func (errImages) InspectImage(_ context.Context, _ string) (ImageInfo, error) {
	return ImageInfo{}, ErrImageNotFound
}

func (errImages) LoadImage(_ context.Context, _ io.Reader) error { return nil }

// TestBuilderExecuteCorruptRequestFailsWithEnvelope 请求损坏：立即终态
// failed + E_BUILD_FAILED 信封（无日志可附时 context 不带 log 键）。
func TestBuilderExecuteCorruptRequestFailsWithEnvelope(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "builder-fail")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	b := NewBuilder(Config{ArtifactsDir: artifacts}, st, errImages{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
		Request: "not-json",
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	// 认领到 building（队列已认领的契约前提），并重读行内状态。
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("reload claimed record: %v", err)
	}

	_, execErr := b.Execute(context.Background(), rec)
	if execErr == nil {
		t.Fatal("corrupt request must fail execute")
	}
	var appErr *apperr.Error
	if !errors.As(execErr, &appErr) {
		t.Fatalf("error = %T, want *apperr.Error", execErr)
	}
	if appErr.Code() != "E_BUILD_FAILED" {
		t.Fatalf("code = %s, want E_BUILD_FAILED", appErr.Code())
	}
	if appErr.Envelope().GetSuggestion() == "" {
		t.Fatal("envelope must carry registry suggestion（验收：失败带建议）")
	}
	row, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if row.Status != state.BuildFailed || row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("row = %s/%s, want failed/E_BUILD_FAILED", row.Status, row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("failed row must stamp finished_at")
	}
}

// TestBuilderExecuteUnclaimedRejected 未认领（非 building）记录拒绝执行。
func TestBuilderExecuteUnclaimedRejected(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "builder-claim-guard")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	b := NewBuilder(Config{}, st, errImages{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: "web", Driver: state.DriverDockerfile,
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	if _, err := b.Execute(context.Background(), rec); err == nil {
		t.Fatal("queued record must not be executable（claim 是契约前提）")
	}
}

// TestBuilderTerminalWriteSurvivesCancelledContext （MG-A3 crashpoint：执行
// 中取消）终态写去取消化：已取消的执行 ctx 下失败终态仍落库——优雅关停的
// 取消落在 solve 完成后/失败处理中时，builds 行不得停留 building
// （WithoutCancel 生效；run/solve 用原 ctx，关停即取消，正确）。
func TestBuilderTerminalWriteSurvivesCancelledContext(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "builder-cancel")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	b := NewBuilder(Config{ArtifactsDir: filepath.Join(t.TempDir(), "artifacts")}, st, errImages{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
		Request: "not-json",
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("reload claimed record: %v", err)
	}

	// 模拟 daemon 优雅关停：ctx 在终态落库前已取消。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, execErr := b.Execute(ctx, rec)
	if execErr == nil {
		t.Fatal("corrupt request must fail execute")
	}
	var appErr *apperr.Error
	if !errors.As(execErr, &appErr) || appErr.Code() != "E_BUILD_FAILED" {
		t.Fatalf("error = %v, want E_BUILD_FAILED envelope", execErr)
	}
	row, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if row.Status != state.BuildFailed || row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("row = %s/%s, want failed/E_BUILD_FAILED（终态写不得随 ctx 取消丢失）", row.Status, row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("cancelled-context failure must stamp finished_at")
	}
}

// TestBuilderExecuteContextOutsideManagedRootsFails H14 执行侧校验：
// ContextDir 指向受管根外（卷根/根目录——绝对且 Clean，但位于一切受管
// 根之外）→ Execute 失败（E_BUILD_FAILED 信封，信息注明上下文目录越界）
// 且 builds 行落 failed 终态（复用既有 fail 路径，不静默放宽）。
func TestBuilderExecuteContextOutsideManagedRootsFails(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "ctx-escape")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	b := NewBuilder(Config{ArtifactsDir: filepath.Join(t.TempDir(), "artifacts")}, st, errImages{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 卷根（Windows "C:\" / POSIX "/"）：受管根（归一后含系统 temp 根）
	// 之外的绝对 Clean 路径。
	outside := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	raw, err := Request{
		BuildID: "ctx-escape-1", AppID: app.ID, AppName: "ctx-escape",
		Service: "web", Driver: state.DriverRailpack, ContextDir: outside,
	}.Encode()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: "web", Driver: state.DriverRailpack, Request: raw,
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("reload claimed record: %v", err)
	}

	_, execErr := b.Execute(context.Background(), rec)
	if execErr == nil {
		t.Fatal("out-of-managed-roots context must fail execute")
	}
	var appErr *apperr.Error
	if !errors.As(execErr, &appErr) || appErr.Code() != "E_BUILD_FAILED" {
		t.Fatalf("error = %v, want E_BUILD_FAILED envelope", execErr)
	}
	if !strings.Contains(execErr.Error(), "上下文目录越界") {
		t.Fatalf("error = %v, want out-of-managed-roots message", execErr)
	}
	row, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if row.Status != state.BuildFailed || row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("row = %s/%s, want failed/E_BUILD_FAILED", row.Status, row.ErrorCode)
	}
}

// TestBuilderFailedBuildRecordsLogPath M2-5：失败构建也落 log_path——产物
// 目录就位即回填（SetBuildLogPath），solve 失败后行携带 log_path、错误
// 信封 context 带 log_path 键（原先唯一写点在 FinishBuildSucceeded，失败
// 行恒空、失败取证落空）。buildkit 端点指向拒绝连接的本地端口：solve 在
// 连接门快速失败（失败注入面，不依赖 docker/buildkit 环境）。
func TestBuilderFailedBuildRecordsLogPath(t *testing.T) {
	st := newQueueTestStore(t)
	app, err := st.CreateApp(context.Background(), "", "builder-logpath")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	b := NewBuilder(Config{
		ArtifactsDir: artifacts,
		BuildkitHost: "tcp://127.0.0.1:1", // 连接拒绝：solve 连接门快速失败
	}, st, errImages{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	raw, err := Request{
		BuildID: "logpath-build-1", AppID: app.ID, AppName: "builder-logpath",
		Service: "web", Driver: state.DriverDockerfile,
		ContextDir: t.TempDir(), Dockerfile: "Dockerfile",
	}.Encode()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	rec, err := st.CreateBuild(context.Background(), state.BuildRecord{
		AppID: app.ID, Service: "web", Driver: state.DriverDockerfile, Request: raw,
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("reload claimed record: %v", err)
	}

	_, execErr := b.Execute(context.Background(), rec)
	if execErr == nil {
		t.Fatal("solve 必须失败（端点拒绝连接）")
	}
	var appErr *apperr.Error
	if !errors.As(execErr, &appErr) || appErr.Code() != "E_BUILD_FAILED" {
		t.Fatalf("error = %v, want E_BUILD_FAILED envelope", execErr)
	}
	wantLog := filepath.Join(artifacts, "builder-logpath", rec.ID, "build.log")
	if got := appErr.Context()["log_path"]; got != wantLog {
		t.Fatalf("envelope log_path = %q, want %q（失败信封必须携带日志路径）", got, wantLog)
	}
	row, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if row.Status != state.BuildFailed {
		t.Fatalf("row status = %s, want failed", row.Status)
	}
	if row.LogPath != wantLog {
		t.Fatalf("row log_path = %q, want %q（M2-5：失败行也必须携带 log_path）", row.LogPath, wantLog)
	}
	if _, err := os.Stat(wantLog); err != nil {
		t.Fatalf("build.log 未落盘: %v", err)
	}
}

// TestValidateContextDir 受管根校验的词法面：受管根内放行；相对路径、
// 未归一化（.. 逃逸词形）、受管根外拒绝。
func TestValidateContextDir(t *testing.T) {
	roots := normalizeContextRoots(nil) // 缺省受管根集合：至少含系统 temp 根
	if len(roots) == 0 {
		t.Fatal("normalized roots must be non-empty")
	}
	inside := filepath.Join(roots[0], "sub", "ctx")
	if err := validateContextDir(inside, roots); err != nil {
		t.Fatalf("inside root rejected: %v", err)
	}
	// 根本身（context == 受管根）视为在内。
	if err := validateContextDir(roots[0], roots); err != nil {
		t.Fatalf("root itself rejected: %v", err)
	}
	// 相对路径拒绝。
	if err := validateContextDir("relative/ctx", roots); err == nil {
		t.Fatal("relative path must be rejected")
	}
	// 未归一化词形（含 .. 段）拒绝——即使词法上落在受管根内。
	unclean := inside + string(filepath.Separator) + ".." + string(filepath.Separator) + "x"
	if err := validateContextDir(unclean, roots); err == nil {
		t.Fatal("unclean path must be rejected")
	}
	// 受管根外（卷根）拒绝。
	outside := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	if err := validateContextDir(outside, roots); err == nil {
		t.Fatal("outside root must be rejected")
	}
	// 多根集合：位于第二根内放行、两个根之外拒绝。
	extra := t.TempDir()
	multi := normalizeContextRoots([]string{extra})
	if err := validateContextDir(filepath.Join(extra, "ctx"), multi); err != nil {
		t.Fatalf("inside second root rejected: %v", err)
	}
	if err := validateContextDir(outside, multi); err == nil {
		t.Fatal("outside all roots must be rejected")
	}
}

// TestValidateContextDirSymlinkEscape H8 回归（MG-1 同族）：受管根内的
// symlink 指向根外目录——纯词法 containsPath 判「在内」，EvalSymlinks 解析
// 后的真源对全部根不成立 → 拒绝。对照：根内真实目录放行、指向根内的
// symlink 放行。Windows 上 os.Symlink 需要特权/开发者模式——创建失败即
// 跳过（Linux CI 上生效；注意 Windows junction 不是替代注入物：
// filepath.EvalSymlinks 不解析 junction（Lstat 不标 ModeSymlink），与
// fsutil.NewFS 的同款行为一致）。
func TestValidateContextDirSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(root, "real-ctx")
	if err := os.MkdirAll(inside, 0o750); err != nil {
		t.Fatalf("mkdir inside: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "secret"), 0o750); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	escapeLink := filepath.Join(root, "escape-link")
	if err := os.Symlink(filepath.Join(outside, "secret"), escapeLink); err != nil {
		t.Skipf("os.Symlink unavailable on this host (privilege required on Windows): %v", err)
	}
	inLink := filepath.Join(root, "inner-link")
	if err := os.Symlink(inside, inLink); err != nil {
		t.Skipf("os.Symlink unavailable on this host: %v", err)
	}
	// 受管根集只含 root（不带 normalizeContextRoots 的系统 temp 根——temp
	// 根恒在受管集内是设计事实，本用例的两个 TempDir 都会落在其中；此处
	// 验证的是包含性判定逻辑本身，用显式根集隔离该噪声）。
	roots := []string{filepath.Clean(root)}

	// 对照：根内真实目录放行（EvalSymlinks 解析后仍在根内）。
	if err := validateContextDir(inside, roots); err != nil {
		t.Fatalf("real dir inside root rejected: %v", err)
	}
	// 链接逃逸：拒绝（词法在内、真源在外）。
	if err := validateContextDir(escapeLink, roots); err == nil {
		t.Fatal("symlink escaping managed root must be rejected (H8)")
	}
	// 指向根内目录的链接放行——解析后仍在根内。
	if err := validateContextDir(inLink, roots); err != nil {
		t.Fatalf("symlink resolving inside root rejected: %v", err)
	}
}

// TestTailFileTruncatesAtLineBoundary stderr 尾部：超限按行边界截齐。
func TestTailFileTruncatesAtLineBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	var content string
	for i := 0; i < 1000; i++ {
		content += "line-9999-aaaa\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	tail := tailFile(path)
	if tail == "" || len(tail) > logTailBytes {
		t.Fatalf("tail len = %d, want <= %d and non-empty", len(tail), logTailBytes)
	}
	if !strings.HasPrefix(tail, "line-") {
		t.Fatalf("tail must start at a line boundary, got %q", tail[:20])
	}
	if missing := tailFile(filepath.Join(dir, "absent")); missing != "" {
		t.Fatalf("absent file tail = %q, want empty", missing)
	}
}
