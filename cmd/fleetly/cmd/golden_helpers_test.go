package cmd

// golden 夹具共享助手：手动驱动 harness + 固定 seed 流（project → app →
// deploy 到 succeeded），供 telemetry 等后续 golden 组复用。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/engine"
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
		o.ResticPath = self
		o.PlatformBackup = &engine.PlatformBackupConfig{
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
	code, _, stderr = runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.27")
	if code != 0 || stderr != "" {
		t.Fatalf("seed deploy: code=%d stderr=%q", code, stderr)
	}
	promoteToSucceeded(t, h, appID)
	return appID
}
