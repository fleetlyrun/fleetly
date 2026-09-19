package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// noopDocker 是状态层服务装配测试用的底座替身（全部返回「不可用」类
// 结果——服务必须容忍并在后台退避，不得影响生命周期）。
type noopDocker struct{}

func (noopDocker) Ping(context.Context) error { return state.ErrNotSwarmManager }
func (noopDocker) ListNodeObservations(context.Context) ([]state.SubstrateNode, error) {
	return nil, state.ErrNotSwarmManager
}
func (noopDocker) SelfNodeID(context.Context) (string, error) {
	return "", state.ErrNotSwarmManager
}
func (noopDocker) UpdateNodeLabel(context.Context, string, string, string, state.ObjectVersion) error {
	return state.ErrVersionConflict
}
func (noopDocker) ResolveObjectVersion(context.Context, state.ObjectKind, string) (state.ObjectVersion, error) {
	return state.ObjectVersion{}, state.ErrObjectNotFound
}
func (noopDocker) SubscribeEvents(context.Context) (<-chan state.SubstrateEvent, error) {
	ch := make(chan state.SubstrateEvent)
	return ch, nil
}
func (noopDocker) Close() error { return nil }

// TestStateServicesBlockUntilShutdown 是 lynx run.Group actor 契约的回归
// 测试：服务 Start 必须阻塞到关停——立即返回会被视为首个完成的 actor，
// 触发整个应用秒退（T2.1 实测踩坑）。本测试把四个状态层服务壳注册进
// 真实 lynx App，验证 RunE 在观察窗内不返回（不秒退），Close 后有序退出。
func TestStateServicesBlockUntilShutdown(t *testing.T) {
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity := state.NewNodeIdentity(st, noopDocker{}, logger)
	observer := state.NewObserver(st, noopDocker{}, logger)
	janitor := state.NewJanitor(st, state.JanitorConfig{}, logger)

	// lynx Runner 在进程内构造时会解析 os.Args 并做 CWD 配置发现
	// （lynx.go initConfigure：Parse(os.Args[1:]) + AddSearchPath(".")）。
	// go test 的 -test.* 参数与包目录环境会让每次 RunE 的行为随测试
	// 参数漂移（实测 -count 高倍压测下偶发到全灭：config 值被测试参数
	// 污染）。测试内隔离进程参数与工作目录，使 Runner 行为确定。
	savedArgs, savedWd := os.Args, ""
	if wd, err := os.Getwd(); err == nil {
		savedWd = wd
	}
	os.Args = []string{"fleetlyd-state-lifecycle-test"}
	_ = os.Chdir(t.TempDir())
	defer func() {
		os.Args = savedArgs
		if savedWd != "" {
			_ = os.Chdir(savedWd)
		}
	}()

	// app 经 channel 交接（直接裸写变量在 Runner goroutine 与测试 goroutine
	// 之间无 happens-before 边——-race 高倍压测下报 DATA RACE，T2-7 修复：
	// 测试侧同步，产品代码未涉）。
	appCh := make(chan lynx.App, 1)
	runner := lynx.NewRunner(func(a lynx.App) error {
		appCh <- a
		a.Register(
			newStoreService(st),
			newIdentityService(identity),
			newObserverService(observer),
			newJanitorService(janitor),
		)
		return nil
	}, lynx.WithName("fleetlyd-state-lifecycle-test"))

	runErr := make(chan error, 1)
	started := time.Now()
	go func() { runErr <- runner.RunE() }()

	// 观察窗：应用必须保持运行（历史缺陷形态 = 毫秒级秒退）。
	select {
	case err := <-runErr:
		t.Fatalf("app exited during observation window after %s (Start must block): %v",
			time.Since(started), err)
	case <-time.After(1500 * time.Millisecond):
	}

	// 关停：RunE 应在预算内有序返回。
	var app lynx.App
	select {
	case app = <-appCh:
	case <-time.After(time.Second):
		t.Fatal("runner did not hand over app within 1s")
	}
	app.Close()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("RunE after Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app did not shut down within 10s after Close")
	}
}
