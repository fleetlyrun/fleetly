package cmd

// quickstart golden 双形态（F0.3）：一条龙 project→app→deploy→route→wait。
// 人类形态与手动驱动夹具并发（CLI goroutine 轮询、测试侧推进状态机——真
// 等待路径被覆盖而非绕过）；--json 轮用独立夹具走 --no-wait。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGoldenQuickstartWaitsToSucceeded(t *testing.T) {
	h := newGoldenHarness(t)
	ctx := sdk.WithToken(context.Background(), h.Token)

	type result struct {
		code   int
		out    string
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, out, stderr := runCLI(t, "quickstart")
		done <- result{code, out, stderr}
	}()

	// 先只读轮询等 CLI 递交部署（拿到 app 锚，才开始驱动——首拍必须先注入
	// running 观测再推进时钟，否则 L1 死线先到即 failed）。
	var appID string
	for appID == "" {
		select {
		case r := <-done:
			t.Fatalf("quickstart settled before any deployment appeared: code=%d out=%q stderr=%q", r.code, r.out, r.stderr)
		default:
		}
		row := h.DB.Runner().QueryRowContext(ctx, `SELECT app_id FROM deployments LIMIT 1`)
		_ = row.Scan(&appID)
		time.Sleep(20 * time.Millisecond)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case r := <-done:
			require.Zero(t, r.code, "quickstart: %s", r.stderr)
			compareGolden(t, "quickstart", normalizeGolden(r.out))
			return
		default:
			if time.Now().After(deadline) {
				t.Fatal("quickstart did not settle within the drive budget")
			}
		}
		h.Runtime.ReportRunning(appID+"-web", 1)
		h.Drive(ctx)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		time.Sleep(50 * time.Millisecond)
	}
}

func TestGoldenQuickstartJSONNoWait(t *testing.T) {
	newGoldenHarness(t)
	code, out, stderr := runCLI(t, "quickstart", "--json", "--no-wait")
	if code != 0 || stderr != "" {
		t.Fatalf("quickstart --json --no-wait: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "quickstart-json", normalizeGolden(out))
}
