package cmd

// Exec 动词 golden（F3.2，ADR-0049）：gRPC 全链经进程内假代理（帧流/
// 退出码透传）；stdout 确定性（假执行 argv 回显），会话头行落 stderr
//（ULID 归一）。shell 的非 TTY 形态同钉（stdin EOF → StdinEOF 语义）。

import (
	"context"
	"regexp"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
)

// goldenExecULID 归一全部 26 位 ULID（会话 ID 与 App ID 皆时间派生不稳；
// node/instance 假常量保留可读）。
var goldenExecULID = regexp.MustCompile(`[0-9A-Z]{26}`)

func normalizeExecGolden(s string) string {
	return goldenExecULID.ReplaceAllString(s, "ID")
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
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(0, "", false)

	steps := []struct {
		verb string
		args []string
	}{
		{"exec", []string{"exec", appID + "/web", "--", "echo", "hi"}},
		{"shell", []string{"shell", appID + "/web"}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			// 会话头行（instance/node 确定性假值 + 会话 ULID 归一）。
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
	detach := apitest.AttachFakeExecAgent(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(42, "", false)
	h.Runtime.SetExecSkipStdin(true)

	code, _, _ := runCLI(t, "exec", appID+"/web", "--", "false")
	if code != 42 {
		t.Fatalf("exit code passthrough: got %d want 42", code)
	}
}
