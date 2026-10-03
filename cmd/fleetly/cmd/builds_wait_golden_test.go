package cmd

// builds wait golden（D22）：独立于主流业务流——主流夹具（manual 不驱动）
// 没有终态 Build 行，而 wait 附着非终态行会挂流。本测试自播种全链：上传
// → deploy --from-dir（queued）→ 手动驱动至 Build 终态（FakeBuilder 即刻
// 成功；部署停在 releasing 不影响 Build 终态）→ 双形态 wait 终态行（单帧
// 即收，--json 轮幂等重放同响应）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGoldenBuildsWait(t *testing.T) {
	h := newGoldenHarness(t)

	code, out, stderr := runCLI(t, "projects", "create", "buildwaits")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "web")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	appID := extractTailID(out)

	// 固定内容源目录（确定性 tar → digest 确定；路径不进输出）。
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o600))
	code, _, stderr = runCLI(t, "uploads", "put", "--project", projectID, srcDir)
	if code != 0 || stderr != "" {
		t.Fatalf("uploads put: code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCLI(t, "deploy", "--app", appID, "--from-dir", srcDir)
	if code != 0 || stderr != "" {
		t.Fatalf("deploy from dir: code=%d stderr=%q", code, stderr)
	}

	// 驱动至 Build 终态（queued→building 的执行 goroutine 异步收口——轮询
	// Drive 让出调度；有界预算内不到即失败）。
	ctx := sdk.WithToken(context.Background(), h.Token)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	buildID := ""
	for i := 0; i < 100; i++ {
		h.Drive(ctx)
		list, err := builds.ListBuilds(ctx, &deliveryv1.ListBuildsRequest{AppId: appID})
		if err == nil && len(list.GetBuilds()) > 0 && list.GetBuilds()[0].GetState() == "succeeded" {
			buildID = list.GetBuilds()[0].GetId()
			break
		}
	}
	if buildID == "" {
		t.Fatal("build did not reach succeeded within the manual drive budget")
	}

	// human 轮：附着终态 Build——单帧即收（exit 0）。
	code, out, stderr = runCLI(t, "builds", "wait", "--build", buildID)
	if code != 0 || stderr != "" {
		t.Fatalf("builds wait: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "builds-wait", normalizeGolden(out))

	// --json 轮：终态行幂等重读（同响应）。
	code, out, stderr = runCLI(t, "builds", "wait", "--build", buildID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("builds wait --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "builds-wait-json", normalizeGolden(out))
}
