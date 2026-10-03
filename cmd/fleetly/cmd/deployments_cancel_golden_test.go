package cmd

// deployments cancel golden（ADR-0016 CLI 补面）：独立于主流业务流——
// cancel 是状态迁移动词，同一部署在 --json 轮同参重跑会撞已终态的
// E_NOT_CANCELLABLE（错误信封含随机 error_id，不可 golden）。播种两个
// queued 部署（manual 夹具不驱动，deploy --image 停 queued）：human 轮
// 取消第一个、--json 轮取消第二个，双形态双 golden。

import (
	"regexp"
	"testing"
)

// deploymentQueuedRe 取 deploy 人类形态首行 "deployment X queued" 的部署
// ID（deploy 输出不带 "(id X)" 尾注，extractTailID 不适用）。
var deploymentQueuedRe = regexp.MustCompile(`deployment ([0-9A-HJKMNP-TV-Z]{26}) queued`)

// seedQueuedDeployment 在独立 app 上部署 --image 停在 queued，返回部署 ID。
// 同 App 的 queued 行会被 latest-wins 合并（admission 判定 5），两个待取消
// 部署必须分属两 App 才能同时存活。
func seedQueuedDeployment(t *testing.T, projectID, appName string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, appName)
	if code != 0 || stderr != "" {
		t.Fatalf("seed app %s: code=%d stderr=%q", appName, code, stderr)
	}
	appID := extractTailID(out)
	code, out, stderr = runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.27")
	if code != 0 || stderr != "" {
		t.Fatalf("seed deploy %s: code=%d stderr=%q", appName, code, stderr)
	}
	m := deploymentQueuedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract queued deployment id from output: %q", out)
	}
	return m[1]
}

func TestGoldenDeploymentsCancel(t *testing.T) {
	_ = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "cancels")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)

	dep1 := seedQueuedDeployment(t, projectID, "one")
	dep2 := seedQueuedDeployment(t, projectID, "two")

	// human 轮：取消第一个 queued 部署（exit 0）。
	code, out, stderr = runCLI(t, "deployments", "cancel", dep1)
	if code != 0 || stderr != "" {
		t.Fatalf("deployments cancel: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "deployments-cancel", normalizeGolden(out))

	// --json 轮：取消第二个 queued 部署（独立部署——不撞第一个已终态的
	// E_NOT_CANCELLABLE）。
	code, out, stderr = runCLI(t, "deployments", "cancel", dep2, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("deployments cancel --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "deployments-cancel-json", normalizeGolden(out))
}
