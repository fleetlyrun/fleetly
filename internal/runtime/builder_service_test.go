package runtime

// builderService 装配测试（T2.8）：队列服务必须满足 lynx run.Group actor
// 契约（Start 阻塞到关停）；底座（buildkitd/镜像端口）不可用只降级预热、
// 不阻塞服务启动；入队构建被假执行器收敛终态（队列→服务壳→store 全链）。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// quickExecutor 立即收敛 succeeded 终态。
type quickExecutor struct{ store *state.Store }

func (e quickExecutor) Execute(ctx context.Context, rec state.BuildRecord) (state.BuildRecord, error) {
	if err := e.store.FinishBuildSucceeded(ctx, rec.ID, "ref", "sha256:quick", "", ""); err != nil {
		return rec, err
	}
	return e.store.GetBuild(ctx, rec.ID)
}

// noopImages 满足 build.ImageSource 的空实现。
type noopImages struct{}

func (noopImages) InspectImage(context.Context, string) (build.ImageInfo, error) {
	return build.ImageInfo{}, build.ErrImageNotFound
}
func (noopImages) LoadImage(context.Context, io.Reader) error { return nil }

// TestBuilderServiceLifecycleAndQueueFlow 服务壳生命周期 + 入队执行全链：
// CLI 语义的入队（queued 行）→ 服务内队列扫描认领 → 假执行器收敛终态。
func TestBuilderServiceLifecycleAndQueueFlow(t *testing.T) {
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "builder-svc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	app, err := st.CreateApp(context.Background(), "", "builder-svc")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builder := build.NewBuilder(build.Config{ManageDaemon: false}, st, noopImages{}, nil, logger)
	queue := build.NewQueue(st, quickExecutor{store: st}, 2, 50*time.Millisecond, 0 /*超时取缺省*/, logger)

	// os.Args/CWD 隔离（lynx Runner 进程内构造的既定写法，见
	// state_services_test.go）。
	savedArgs, savedWd := os.Args, ""
	if wd, err := os.Getwd(); err == nil {
		savedWd = wd
	}
	os.Args = []string{"fleetlyd-builder-lifecycle-test"}
	_ = os.Chdir(t.TempDir())
	defer func() {
		os.Args = savedArgs
		if savedWd != "" {
			_ = os.Chdir(savedWd)
		}
	}()

	var app_ lynx.App
	runner := lynx.NewRunner(func(a lynx.App) error {
		app_ = a
		a.Register(newBuilderService(queue, builder, logger))
		return nil
	}, lynx.WithName("fleetlyd-builder-lifecycle-test"))

	runErr := make(chan error, 1)
	started := time.Now()
	go func() { runErr <- runner.RunE() }()

	select {
	case err := <-runErr:
		t.Fatalf("app exited during observation window after %s (Start must block): %v",
			time.Since(started), err)
	case <-time.After(1500 * time.Millisecond):
	}

	// 入队（CLI 同路径）→ 服务内队列应收敛终态。
	rec, err := queue.Enqueue(context.Background(), state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for {
		row, err := st.GetBuild(context.Background(), rec.ID)
		if err != nil {
			t.Fatalf("get build: %v", err)
		}
		if row.Status == state.BuildSucceeded {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("build stuck at %s (in-process queue never picked it up?)", row.Status)
		case <-time.After(50 * time.Millisecond):
		}
	}

	app_.Close()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("RunE after Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app did not shut down within 10s after Close")
	}
}
