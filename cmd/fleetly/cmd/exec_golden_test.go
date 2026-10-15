package cmd

// Exec 动词 golden（F3.2，ADR-0049）：gRPC 全链经进程内假代理（帧流/
// 退出码透传）；stdout 确定性（假执行 argv 回显），会话头行落 stderr
//（ULID 与会话 ID 归一）。shell 的非 TTY 形态同钉（stdin EOF → StdinEOF
// 语义）。

import (
	"context"
	"regexp"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// goldenExecULID 归一全部 26 位 ULID（App ID 时间派生不稳；node/instance
// 假常量保留可读）。
var goldenExecULID = regexp.MustCompile(`[0-9A-Z]{26}`)

// goldenExecSessionID 归一会话 ID（2026-10-15 P3-1 随机化后每次受理为
// 不同的 crypto 随机值：exec-<32hex> → exec-ID）。
var goldenExecSessionID = regexp.MustCompile(`exec-[0-9a-f]{32}`)

func normalizeExecGolden(s string) string {
	// 会话 ID 先归一：32 位 hex 若纯数字会被 ULID 正则截断，顺序不可换。
	return goldenExecULID.ReplaceAllString(goldenExecSessionID.ReplaceAllString(s, "exec-ID"), "ID")
}

// execGoldenSeed 建夹具 App（exec 受理只要求 App 行在场——实例解析是
// Provider 假面，无需部署链）。
func execGoldenSeed(t *testing.T) (h *apitest.Harness, appID string) {
	t.Helper()
	h = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "shop")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "web")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	return h, extractTailID(out)
}

func TestGoldenExecVerbs(t *testing.T) {
	h, appID := execGoldenSeed(t)
	detach := apitest.AttachFakeRelay(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(0, "", false)

	steps := []struct {
		verb string
		args []string
	}{
		{"exec", []string{"exec", appID + "/web", "--", "echo", "hi"}},
		// 实例锚定（ADR-0067）：旗标前置铁律——--instance 在位置参数之前；
		// 会话头行的 instance 假常量钉死锚定语义。
		{"exec-anchored", []string{"exec", "--instance", apitest.FakeExecInstance, appID + "/web", "--", "echo", "anchored"}},
		{"shell", []string{"shell", appID + "/web"}},
		{"exec-instances", []string{"exec-instances", appID + "/web"}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			// 会话头行（instance/node 确定性假值 + 会话 ID 随机化后归一）。
			code, out, stderr := runCLI(t, st.args...)
			if code != 0 {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb), normalizeExecGolden(out))
			compareGolden(t, goldenFile(st.verb)+"-header", normalizeExecGolden(stderr))

			// --json 必须前置：`--` 分隔符之后 root flag 不剥离（argv 面）。
			jsonArgs := append([]string{"--json"}, st.args...)
			code, out, stderr = runCLI(t, jsonArgs...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeExecGolden(out))
		})
	}
}

// TestGoldenExecExitCodePassthrough 钉退出码透传（机器编排锚：载体进程
// 退出码即 CLI 退出码）。
func TestGoldenExecExitCodePassthrough(t *testing.T) {
	h, appID := execGoldenSeed(t)
	detach := apitest.AttachFakeRelay(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(42, "", false)
	h.Runtime.SetExecSkipStdin(true)

	code, _, _ := runCLI(t, "exec", appID+"/web", "--", "false")
	if code != 42 {
		t.Fatalf("exit code passthrough: got %d want 42", code)
	}
}
