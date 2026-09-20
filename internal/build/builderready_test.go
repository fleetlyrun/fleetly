package build

// buildkitd 就绪收敛测试（M2-4/M2-8）：瞬时错误不毒化重试预算（lastErr
// 每轮清零）；容器 start 后的就绪探测带预算重试（probe 可注入，前 N 次
// 失败后成功不被误杀、持续未就绪在预算内等待而非立即失败）。重试节奏经
// 包级变量注入缩短（缺省 15s/2s——回归在毫秒级验证）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDaemon 是 buildkitd 编排端口的测试假件：EnsureVolumePresent 前
// failVolume 次失败后成功（瞬时错误注入），其余恒成功。
type fakeDaemon struct {
	failVolume atomic.Int64
	volCalls   atomic.Int64
}

func (d *fakeDaemon) EnsureImagePresent(context.Context, string) error { return nil }

func (d *fakeDaemon) EnsureVolumePresent(context.Context, string) error {
	if d.volCalls.Add(1) <= d.failVolume.Load() {
		return errors.New("transient volume error")
	}
	return nil
}

func (d *fakeDaemon) EnsureContainerRunning(context.Context, DaemonSpec) error { return nil }

// newReadyTestBuilder 构造带假 daemon 与即时成功 probe 的 Builder（M2-8
// 的 probe 语义另行注入覆盖）。
func newReadyTestBuilder(t *testing.T, daemon DaemonManager) *Builder {
	t.Helper()
	return NewBuilder(Config{ManageDaemon: true}, newQueueTestStore(t), errImages{}, daemon,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// injectEnsureTiming 注入缩短就绪收敛/探测节奏，测试结束还原。
func injectEnsureTiming(t *testing.T, ensure, budget, probe time.Duration) {
	t.Helper()
	origEnsure, origBudget, origProbe := daemonEnsureInterval, executeEnsureBudget, daemonProbeInterval
	daemonEnsureInterval, executeEnsureBudget, daemonProbeInterval = ensure, budget, probe
	t.Cleanup(func() {
		daemonEnsureInterval, executeEnsureBudget, daemonProbeInterval = origEnsure, origBudget, origProbe
	})
}

// TestEnsureDaemonReadyRetriesAfterTransientError M2-4：首轮瞬时错误
// （EnsureVolumePresent 失败）不得毒化重试预算——第二次成功即在预算内收敛。
// 回归形态：lastErr 不清零时 `lastErr == nil` 门条件永不成立，空转整个
// 预算后失败。
func TestEnsureDaemonReadyRetriesAfterTransientError(t *testing.T) {
	daemon := &fakeDaemon{}
	daemon.failVolume.Store(1)
	b := newReadyTestBuilder(t, daemon)
	b.probe = func(context.Context) error { return nil } // 探测面另行覆盖
	injectEnsureTiming(t, 5*time.Millisecond, 2*time.Second, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.EnsureDaemonReady(ctx); err != nil {
		t.Fatalf("should converge successfully within budget after a transient error: %v", err)
	}
	if n := daemon.volCalls.Load(); n < 2 {
		t.Fatalf("volume calls = %d, want >=2 (must retry after a transient error instead of spinning out the budget)", n)
	}
}

// TestEnsureDaemonReadyProbesUntilReady M2-8：容器 start 成功 ≠ 就绪——
// probe 前两次失败第三次成功，收敛不被 E_BUILD_FAILED 误杀。
func TestEnsureDaemonReadyProbesUntilReady(t *testing.T) {
	daemon := &fakeDaemon{}
	b := newReadyTestBuilder(t, daemon)
	var probeCalls atomic.Int64
	b.probe = func(context.Context) error {
		if probeCalls.Add(1) <= 2 {
			return errors.New("buildkitd starting")
		}
		return nil
	}
	injectEnsureTiming(t, time.Second, 5*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.EnsureDaemonReady(ctx); err != nil {
		t.Fatalf("should converge successfully within budget after the first two probe failures: %v", err)
	}
	if n := probeCalls.Load(); n != 3 {
		t.Fatalf("probe calls = %d, want 3", n)
	}
}

// TestEnsureDaemonReadyWaitsThroughProbeFailures M2-8「未就绪保持等待而非
// 失败」：probe 持续失败时在剩余预算内重试等待，预算耗尽才以
// E_BUILD_FAILED 就绪错误失败（而非首探失败即终态失败）。
func TestEnsureDaemonReadyWaitsThroughProbeFailures(t *testing.T) {
	daemon := &fakeDaemon{}
	b := newReadyTestBuilder(t, daemon)
	var probeCalls atomic.Int64
	b.probe = func(context.Context) error {
		probeCalls.Add(1)
		return errors.New("buildkitd never ready")
	}
	injectEnsureTiming(t, time.Second, 150*time.Millisecond, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := b.EnsureDaemonReady(ctx)
	if err == nil {
		t.Fatal("persistent not-ready must fail only after the budget is exhausted")
	}
	// 预算前不得失败（等待而非立即终态失败），预算后不久必须失败。
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("failed before waiting out the budget: elapsed %v", elapsed)
	}
	if n := probeCalls.Load(); n < 2 {
		t.Fatalf("probe calls = %d, want >=2 (must keep probing while waiting)", n)
	}
}
