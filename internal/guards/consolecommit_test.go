package guards

// R15-L4 守卫：proto/swagger 契约变更必须与 console/src/api 类型再生成同
// commit（AGENTS.md「dist 是提交进仓的生成物，改 proto/前端后必须
// console:gen && console:build 同 commit」）。评审实证 8 处「晚一个
// commit」的窗口——CI 的 console job 按 head 跑看不到 commit 级红窗。
//
// 执法面两层：
//  1. 工作树变更集（含未跟踪）：git diff <base> + ls-files --others；
//  2. 未合入提交逐条：git rev-list <base>..HEAD，git diff-tree 每 commit。
//
// base 解析序：FLEETLY_CONSOLE_SYNC_BASE > origin/main > HEAD（离线/无
// origin 时退回工作树面，不静默跳过整条律文）。CI PR 面 fetch-depth 0
// （backend job）下 origin/main 可解析 → 逐 commit 生效；push 到 main 后
// range 为空，历史窗口由 PR 门禁拦截。
//
// 判据：变更集含 proto/** 或 genproto/**/*.swagger.json，却不含
// console/src/api/** 即红。swagger 的描述文本会进入生成类型（见
// console/src/api 的 @description），故注释级 proto 变更同样要求再生成。

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// gitLines 在仓库根执行 git 并返回非空行。
func gitLines(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// gitRefExists 报告 ref 是否可解析为 commit。
func gitRefExists(t *testing.T, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = repoRoot(t)
	return cmd.Run() == nil
}

// consoleSyncBase 返回对账基线：env 覆盖 > origin/main > HEAD。
func consoleSyncBase(t *testing.T) string {
	t.Helper()
	if ref := strings.TrimSpace(os.Getenv("FLEETLY_CONSOLE_SYNC_BASE")); ref != "" {
		if !gitRefExists(t, ref) {
			t.Fatalf("FLEETLY_CONSOLE_SYNC_BASE=%q does not resolve to a commit", ref)
		}
		return ref
	}
	if gitRefExists(t, "origin/main") {
		return "origin/main"
	}
	return "HEAD"
}

// isProtoContractPath 报告路径是否属于 proto 契约面（proto 源或 swagger）。
func isProtoContractPath(path string) bool {
	if strings.HasPrefix(path, "proto/") {
		return true
	}
	return strings.HasPrefix(path, "genproto/") && strings.HasSuffix(path, ".swagger.json")
}

// isConsoleAPIPath 报告路径是否属于 console 生成类型面。
func isConsoleAPIPath(path string) bool {
	return strings.HasPrefix(path, "console/src/api/")
}

// consoleSyncViolations 返回触发「proto 变更但 console/src/api 未同批」
// 判据的变更路径（空 = 合规）。
func consoleSyncViolations(paths []string) []string {
	proto, api := false, false
	for _, path := range paths {
		if isProtoContractPath(path) {
			proto = true
		}
		if isConsoleAPIPath(path) {
			api = true
		}
	}
	if proto && !api {
		var offenders []string
		for _, path := range paths {
			if isProtoContractPath(path) {
				offenders = append(offenders, path)
			}
		}
		return offenders
	}
	return nil
}

// TestConsoleTypesFollowProto：契约变更与 console 生成类型的同 commit 对账。
func TestConsoleTypesFollowProto(t *testing.T) {
	base := consoleSyncBase(t)
	if repo := gitLines(t, "rev-parse", "--show-toplevel"); len(repo) == 0 || repo[0] == "" {
		t.Fatal("not inside a git work tree — the console-sync guard is blind")
	}

	// 面①：工作树变更集（含未跟踪文件——新 proto 文件同样要先见后改）。
	worktree := append(gitLines(t, "diff", "--name-only", base), gitLines(t, "ls-files", "--others", "--exclude-standard")...)
	if offenders := consoleSyncViolations(worktree); len(offenders) > 0 {
		t.Errorf("worktree changes touch the proto/swagger contract without regenerating console/src/api (base %s): %s — run `mise run console:gen` (and console:build) in the same change set (AGENTS.md generation discipline, R15-L4)",
			base, strings.Join(offenders, ", "))
	}

	// 面②：未合入提交逐条（merge commit 除外——合并本身不改契约）。
	commits := gitLines(t, "rev-list", "--no-merges", base+"..HEAD")
	for _, sha := range commits {
		files := gitLines(t, "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", sha)
		offenders := consoleSyncViolations(files)
		if len(offenders) == 0 {
			continue
		}
		subject := ""
		if lines := gitLines(t, "log", "-1", "--format=%s", sha); len(lines) > 0 {
			subject = lines[0]
		}
		short := sha
		if len(short) > 12 {
			short = short[:12]
		}
		t.Errorf("commit %s (%s) touches the proto/swagger contract without console/src/api in the same commit: %s — protocol batches must carry `mise run console:gen && console:build` (R15-L4)",
			short, subject, strings.Join(offenders, ", "))
	}
}
