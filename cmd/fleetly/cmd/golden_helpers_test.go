package cmd

// golden 夹具共享助手：手动驱动 harness + 固定 seed 流（project → app →
// deploy 到 succeeded），供 telemetry 等后续 golden 组复用。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newGoldenHarness(t *testing.T) *apitest.Harness {
	t.Helper()
	h := apitest.NewManual(t)
	origDial := dialClient
	dialClient = func(_ string, opts ...fleetly.Option) (*fleetly.Client, error) {
		return fleetly.Dial("passthrough:///bufnet", append(opts, h.DialOpts()...)...)
	}
	t.Cleanup(func() { dialClient = origDial })
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
