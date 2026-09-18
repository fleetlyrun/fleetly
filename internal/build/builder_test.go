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
