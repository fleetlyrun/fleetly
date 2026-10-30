package cmd

// golden 夹具共享助手：手动驱动 harness + 固定 seed 流（project → app →
// deploy 到 succeeded），供 telemetry 等后续 golden 组复用。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newGoldenHarness(t *testing.T) *apitest.Harness {
	t.Helper()
	// Platform Backup 面可用（F2.3）：restic 缝指向本测试二进制（TestMain
	// 重 invoked 拦截，main_test.go）——platform backup/backups 的 golden
	// 双形态走真 restic 链路形态。
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary for the fake restic seam: %v", err)
	}
	h := apitest.NewManualOpts(t, func(o *engine.Options) {
		o.Backup.ResticPath = self
		o.Backup.PlatformBackup = &engine.PlatformBackupConfig{
			Interval: time.Hour, Retention: 24 * time.Hour,
		}
	})
	// 节拍锚预热：DriveOnce 会跑 backup 环（platformBackupDue 首拍即到点
	// → 假 restic 链落 platform.backup_* 事件，污染先行的 events follow
	// golden）。锚 = 夹具时钟纪元（interval 1h 内恒不到点）；手动触发
	//（platform backup 步）不走节拍锚，不受影响。路径段与 engine 的
	// platformBackupPath 常量同源（last-run 是节拍语义的稳定文件名）。
	anchorDir := filepath.Join(h.DataRoot, "platform-backups")
	if err := os.MkdirAll(anchorDir, 0o750); err != nil {
		t.Fatalf("prewarm platform backup anchor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(anchorDir, "last-run"),
		[]byte("2026-01-01T00:00:00Z"), 0o600); err != nil {
		t.Fatalf("prewarm platform backup anchor: %v", err)
	}
	origDial := dialClient
	dialClient = func(_ string, opts ...fleetly.Option) (*fleetly.Client, error) {
		return fleetly.Dial("passthrough:///bufnet", append(opts, h.DialOpts()...)...)
	}
	t.Cleanup(func() { dialClient = origDial })
	// 夹具统一注入 admin token（owner 全权 bootstrap）：golden 动词经
	// FLEETLY_TOKEN 环境解析（conn 解析序 flag > env > 凭据文件）。
	t.Setenv("FLEETLY_TOKEN", h.Token)
	return h
}

// goldenSeedDeploy 建 project/app 并部署到 succeeded，返回 appID。
func goldenSeedDeploy(t *testing.T, h *apitest.Harness) string {
	t.Helper()
	code, out, stderr := runCLI(t, "projects", "create", "shop")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "web")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	appID := extractTailID(out)
	code, _, stderr = runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.27", "--yes")
	if code != 0 || stderr != "" {
		t.Fatalf("seed deploy: code=%d stderr=%q", code, stderr)
	}
	promoteToSucceeded(t, h, appID)
	return appID
}

// latestDeploymentState 读某 App 最新部署行的状态（golden 轮询夹具的推进门）：
// 假时钟只在 observing 就位后推进（S18b——推进次数与轮询次数解耦，否则观测
// 经 Watch 消费 goroutine 异步进缓存，调度抖动会让同测试多推一拍，时间戳漂）。
func latestDeploymentState(t *testing.T, h *apitest.Harness, appID string) string {
	t.Helper()
	var state string
	err := h.DB.Runner().QueryRowContext(context.Background(),
		`SELECT state FROM deployments WHERE app_id = ? ORDER BY seq DESC LIMIT 1`, appID).Scan(&state)
	if err != nil {
		return ""
	}
	return state
}

// waitDatabaseRunning 在界预算内驱动收敛环直到库行 running（S18b browse
// flake 根因修）：running 观测经 Watch 消费 goroutine 异步进引擎缓存，固定
// 一拍 Drive 可能与消费时序竞争——缺观测即 pending，browse 受理如实拒绝
// （E_DATABASE_NOT_READY 假阴性）。条件等待只去掉时序假设，不弱化断言。
func waitDatabaseRunning(t *testing.T, h *apitest.Harness, dbID string) {
	t.Helper()
	ctx := context.Background()
	driveCtx := fleetly.WithToken(ctx, h.Token)
	deadline := time.Now().Add(10 * time.Second)
	status := ""
	for {
		h.Drive(driveCtx)
		if err := h.DB.Runner().QueryRowContext(ctx,
			`SELECT status FROM databases WHERE id = ?`, dbID).Scan(&status); err != nil {
			t.Fatalf("read database status for %s: %v", dbID, err)
		}
		if status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("database %s did not reach running within the drive budget (last status %q)", dbID, status)
		}
		time.Sleep(2 * time.Millisecond) // 让出 Watch 消费 goroutine（观测异步进缓存）
	}
}
