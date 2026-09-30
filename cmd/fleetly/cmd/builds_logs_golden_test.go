package cmd

// builds logs golden（B4：构建日志读面；独立夹具直插 Build 行——golden
// 业务流不产构建）。

import (
	"context"
	"testing"

	buildrepo "github.com/fleetlyrun/fleetly/internal/state/build"
)

func TestGoldenBuildsLogs(t *testing.T) {
	h := newGoldenHarness(t)

	code, out, stderr := runCLI(t, "projects", "create", "builds")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "builder")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	appID := extractTailID(out)

	// 直插一条成功 Build 行（golden 夹具无真实构建链；读面只消费行与
	// 最近缓冲——缓冲为空，golden 钉住空流形态。builds-list 的 golden 归
	// TestGoldenBusinessVerbs 的同名步骤，此处不重复写同名文件）。
	b := &buildrepo.Build{
		ID: "01M3BUILD00000000000000000", AppID: appID,
		State: buildrepo.StateSucceeded, Digest: "sha256:" + "a1b2c3d4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff00",
	}
	if err := buildrepo.New(h.Clock).Create(context.Background(), h.DB.Runner(), b); err != nil {
		t.Fatalf("seed build row: %v", err)
	}

	// builds logs（最近缓冲为空 = 无帧；--json 空流同样空输出）。
	code, out, stderr = runCLI(t, "builds", "logs", "--build", b.ID)
	if code != 0 || stderr != "" {
		t.Fatalf("builds logs: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "builds-logs", normalizeGolden(out))

	code, out, stderr = runCLI(t, "builds", "logs", "--build", b.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("builds logs --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "builds-logs-json", normalizeGolden(out))
}
