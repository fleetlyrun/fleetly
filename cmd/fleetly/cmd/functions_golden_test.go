package cmd

// functions 命令组 golden（ADR-0102 任务⑤；设计 v3 §11）：组清单 + 全部
// 子动词双形态（覆盖守卫 TestAllVerbsHaveDualGoldens 的精确命名契约）；
// 名寻址（project/name 复合）与 id 寻址双路。

import (
	"regexp"
	"testing"
)

func createTestFunctionCLI(t *testing.T, projectID, name string, jsonOut bool) (string, string) {
	t.Helper()
	args := []string{"functions", "create", "--project", projectID, "--image", "ghcr.io/fleetly/http-echo:1",
		"--max-replicas", "4", name}
	if jsonOut {
		args = append(args, "--json")
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("functions create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}

// dual 对一个子动词跑双形态并各落一枚 golden。revDigestColumnRe 把
// revisions 表的 digest 列（rN<TAB>hex12）占位——与 normalizeGolden 的
// revDigestRe 同因（protojson detrand 跨构建漂移）。
var revDigestColumnRe = regexp.MustCompile(`(r[0-9]+\t)[0-9a-f]{12}`)

func dual(t *testing.T, base string, args ...string) {
	t.Helper()
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
	}
	compareGolden(t, base, revDigestColumnRe.ReplaceAllString(normalizeGolden(out), "${1}<REVDIGEST>"))
	code, out, stderr = runCLI(t, append(append([]string{}, args...), "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v --json: code=%d stderr=%q", args, code, stderr)
	}
	compareGolden(t, base+"-json", revDigestColumnRe.ReplaceAllString(normalizeGolden(out), "${1}<REVDIGEST>"))
}

func TestGoldenFunctionsLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "fns")

	// 组清单（裸命令）。
	dual(t, "functions", "functions")

	fnID, _ := createTestFunctionCLI(t, projectID, "resize-img", false)
	_, _ = createTestFunctionCLI(t, projectID, "webhook-relay", true)

	// create 双形态经幂等键重放同响应（第二跑不撞名——idem 面顺带钉测）。
	dual(t, "functions-create", "functions", "create", "--project", projectID,
		"--image", "ghcr.io/fleetly/http-echo:1", "--max-replicas", "4",
		"--idempotency-key", "golden-create", "third-fn")
	dual(t, "functions-list", "functions", "list", "--project", projectID)
	dual(t, "functions-get", "functions", "get", "fns/resize-img")
	dual(t, "functions-url", "functions", "url", fnID)
	dual(t, "functions-deploy", "functions", "deploy", "--project", projectID,
		"--image", "ghcr.io/fleetly/http-echo:2", "resize-img")
	dual(t, "functions-revisions", "functions", "revisions", fnID)
	// stats：observed=false 诚实断口（测试夹具无观测面——结构性缺席呈现）。
	dual(t, "functions-stats", "functions", "stats", fnID)
	// git 源受理面（P2-4 同期件）：三源互斥错误形 + git create 受理形状
	//（冻结面——不触真克隆；构建输入走 pending 门由假 builder 兜住）。
	code, _, stderr := runCLI(t, "functions", "deploy", "--project", projectID,
		"--image", "ghcr.io/fleetly/http-echo:2", "--git-repo", "https://github.com/fleetly/demo", "resize-img")
	if code == 0 {
		t.Fatalf("image+git-repo: expected refusal")
	}
	compareGolden(t, "functions-deploy-git-mutex", normalizeGolden(stderr))
	dual(t, "functions-deploy-git", "functions", "deploy", "--project", projectID,
		"--git-repo", "https://github.com/fleetly/demo", "--git-ref", "9c8b7a6",
		"--railpack-version", "0.39.0", "resize-img")
	dual(t, "functions-rollback", "functions", "rollback", fnID)
	dual(t, "functions-scale", "functions", "scale", fnID, "3")
	// 出界 scale（受理位钳制 suggestion 面）：错误形态双锚。
	code, _, stderr = runCLI(t, "functions", "scale", fnID, "9")
	if code == 0 {
		t.Fatalf("out-of-window scale: expected refusal")
	}
	compareGolden(t, "functions-scale-out-of-window", normalizeGolden(stderr))
	_, _, stderr = runCLI(t, "functions", "scale", fnID, "9", "--json")
	compareGolden(t, "functions-scale-out-of-window-json", normalizeGolden(stderr))
	dual(t, "functions-exposure", "functions", "exposure", "--host", "img.fns.test", fnID, "public")
	dual(t, "functions-exposure-back", "functions", "exposure", fnID, "private")
	dual(t, "functions-logs", "functions", "logs", fnID)
	// delete 是破坏性动词——dual 双跑不可用：预埋两枚分别打两形态。
	doomedA, _ := createTestFunctionCLI(t, projectID, "doomed-a", false)
	doomedB, _ := createTestFunctionCLI(t, projectID, "doomed-b", false)
	code, out, stderr := runCLI(t, "functions", "delete", doomedA)
	if code != 0 || stderr != "" {
		t.Fatalf("functions delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "functions-delete", normalizeGolden(out))
	code, out, stderr = runCLI(t, "functions", "delete", doomedB, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("functions delete json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "functions-delete-json", normalizeGolden(out))
	// 幂等 tombstone：再删同形 404（错误形态双锚）。
	code, _, stderr = runCLI(t, "functions", "delete", doomedA)
	if code == 0 {
		t.Fatalf("delete again: expected not-found")
	}
	compareGolden(t, "functions-delete-again", normalizeGolden(stderr))
	_, _, stderr = runCLI(t, "functions", "delete", doomedA, "--json")
	compareGolden(t, "functions-delete-again-json", normalizeGolden(stderr))

	// invoke：private/已删函数诚实拒（控制面不代理）——双形态文案同锚；
	// public 直发的真网断言归 e2e 腿（golden 面不引外部依赖）。
	code, out, stderr = runCLI(t, "functions", "invoke", fnID)
	if code == 0 {
		t.Fatalf("invoke on deleted function: expected refusal, out=%q", out)
	}
	compareGolden(t, "functions-invoke", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "functions", "invoke", fnID, "--json")
	if code == 0 {
		t.Fatalf("invoke json on deleted function: expected refusal")
	}
	compareGolden(t, "functions-invoke-json", normalizeGolden(stderr))
}
