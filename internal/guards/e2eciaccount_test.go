package guards

// G20 e2e↔CI 对账守卫 + 本地 verify 类门禁对账（R12-M5/R12-L5/R09-L4）：
//
//   - e2e：mise.toml 的全部 e2e 任务与 .github/workflows/ci.yml 的全部
//     `mise run e2e:*` 调用一一对账——每个 e2e 任务要么被 CI 调用、要么登记
//     对账白名单（带中文理由）。双向保鲜：新增任务必登记或进 CI（漏挂即
//     红）；任务删除/改名后白名单条目悬空、或白名单任务被 CI 收编后条目
//     过期，即红；CI 调用指向不存在的任务（改名漂移）即红。
//   - 脚本存在性：每个 e2e 任务 run 体里的 `sh e2e/*.sh` 必须真实存在
//     （脚本改名忘改 mise 只在运行期红——R12-L5）。
//   - verify 类门禁：名字含 verify / generate: / console: 前缀的任务必须
//     从 CI 可达（直接调用或经任务引用闭包），否则登记 ciTaskExemptions
//     带理由（"没有 CI 承载"成为一条要写理由的登记，而不是沉默——R12-M5）。
//   - 防盲：mise e2e 任务实测 13、CI e2e 调用实测 13（2026-10-30 B1/B1s 批
//     e2e:confirm 进 CI；此前 2026-10-16 挂账清偿批 template/browse/
//     bluegreen 三腿进 CI）；阈值贴实测值，解析退化（文件挪走/改格式）
//     在此显形，不留 1/3 余量（R12-L5）。
//
// go.mod/go.sum 的零漂移范围由 generate:verify 任务体断言（R09-L4）；只
// 编译不测试的 Windows 门禁由 TestCIHasWindowsCrossCompile 断言（R09-L7）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// e2eciWhitelist 是 e2e↔CI 对账白名单（任务名 → 理由，中文一句话）。
// 理由依据各 e2e/dind-*.sh 头部"前置"段如实登记；任务进 CI 或删除即删条目。
// 现树零豁免——2026-10-16 挂账清偿批裁决三腿全量进 CI（用户裁决：
// 「时间预算」理由不再成立），白名单清空但机制保留。
var e2eciWhitelist = map[string]string{}

// ciTaskExemptions 是 verify 类任务的「无 CI 承载」登记面（任务名 → 理由）。
// 只有确实不是门禁/通道重复的任务才能进；条目不再命中（任务被 CI 收编/
// 删除）即红——双向保鲜，避免豁免腐烂。
var ciTaskExemptions = map[string]string{
	"generate:proto": "remote-plugin 生成通道（buf.gen.yaml 的 remote 插件）；门禁通道走钉版本地插件 generate:proto:local（generate:verify 引用），不单独进 CI",
	"console:dev":    "Vite 开发服（人机开发面），不是门禁任务",
}

// miseTaskHeaderRe 匹配 mise.toml 任务头（[tasks."name"]）。
var miseTaskHeaderRe = regexp.MustCompile(`^\[tasks\."([^"]+)"\]$`)

// miseTaskRefRe 匹配任务体内的任务引用（run = [{ task = "x" }, ...]）。
var miseTaskRefRe = regexp.MustCompile(`task\s*=\s*"([^"]+)"`)

// miseShScriptRe 匹配任务体里的 sh 脚本调用（run = "sh e2e/x.sh"）。
var miseShScriptRe = regexp.MustCompile(`\bsh\s+([A-Za-z0-9_./-]+\.sh)\b`)

// ciMiseCallRe 匹配 CI 步骤里的 mise 任务调用（run: mise run <task>）。
var ciMiseCallRe = regexp.MustCompile(`mise\s+run\s+([A-Za-z0-9._:-]+)`)

// parseMiseTasks 把 mise.toml 解析成 任务名 → 任务体文本。TOML-lite：只
// 认 `[tasks."name"]` 任务头（其余表头结束上一个任务），不做完整 TOML
// 解析——守卫只需要任务引用/脚本/命令面的结构断言。
func parseMiseTasks(content string) map[string]string {
	tasks := map[string]string{}
	name := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := miseTaskHeaderRe.FindStringSubmatch(trimmed); m != nil {
			name = m[1]
			tasks[name] = ""
			continue
		}
		if strings.HasPrefix(trimmed, "[") { // 其他 TOML 表：任务体结束
			name = ""
			continue
		}
		if name != "" {
			tasks[name] += line + "\n"
		}
	}
	return tasks
}

// taskScripts 返回任务体里引用的 sh 脚本路径（去重升序）。
func taskScripts(body string) []string {
	var out []string
	for _, m := range miseShScriptRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return unique(out)
}

// taskRefs 返回任务体里引用的其它任务名（去重升序）。
func taskRefs(body string) []string {
	var out []string
	for _, m := range miseTaskRefRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return unique(out)
}

// parseMiseE2ETasks 从 mise.toml 文本提取全部 e2e 任务名（去重升序）。
func parseMiseE2ETasks(content string) []string {
	var names []string
	for name := range parseMiseTasks(content) {
		if strings.HasPrefix(name, "e2e:") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// parseCIMiseCalls 从 CI workflow 文本提取全部 `mise run <task>` 调用
// （去重升序）。
func parseCIMiseCalls(content string) []string {
	var names []string
	for _, m := range ciMiseCallRe.FindAllStringSubmatch(content, -1) {
		names = append(names, m[1])
	}
	return unique(names)
}

// parseCIE2ECalls 从 CI workflow 文本提取全部 `mise run e2e:*` 调用。
func parseCIE2ECalls(content string) []string {
	var names []string
	for _, name := range parseCIMiseCalls(content) {
		if strings.HasPrefix(name, "e2e:") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ciReachableTasks 计算从 CI 直接调用出发、经任务引用闭包可达的任务集。
func ciReachableTasks(tasks map[string]string, direct []string) map[string]bool {
	reached := map[string]bool{}
	queue := append([]string{}, direct...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reached[name] {
			continue
		}
		reached[name] = true
		for _, ref := range taskRefs(tasks[name]) {
			queue = append(queue, ref)
		}
	}
	return reached
}

// isVerifyClassTask 报告任务是否属于「verify 类门禁」（名字含 verify，
// 或 generate:/console: 生成/验证族）。新增此类任务必须进 CI 或登记。
func isVerifyClassTask(name string) bool {
	return strings.Contains(name, "verify") ||
		strings.HasPrefix(name, "generate:") ||
		strings.HasPrefix(name, "console:")
}

// TestE2ECIAccount：mise e2e 任务集 ⊆ CI 调用集 ∪ 白名单，三面互证保鲜。
func TestE2ECIAccount(t *testing.T) {
	miseContent := readFileLF(t, "mise.toml")
	ciContent := readFileLF(t, ".github/workflows/ci.yml")
	mise := parseMiseE2ETasks(miseContent)
	ci := parseCIE2ECalls(ciContent)

	// 防盲：实测值之下即解析退化（2026-10-30 实测 13/13，阈值贴边）。
	if len(mise) < 13 {
		t.Fatalf("mise.toml scan found only %d e2e tasks (expected >= 13) — guard is blind, fix the scan", len(mise))
	}
	if len(ci) < 13 {
		t.Fatalf("ci.yml scan found only %d e2e invocations (expected >= 13) — guard is blind, fix the scan", len(ci))
	}

	miseSet := map[string]bool{}
	for _, name := range mise {
		miseSet[name] = true
	}
	ciSet := map[string]bool{}
	for _, name := range ci {
		ciSet[name] = true
	}

	// CI 调用必须指向在册任务（任务改名后 workflow 未跟 → 漂移）。
	for _, name := range ci {
		if !miseSet[name] {
			t.Errorf("ci.yml invokes %s but mise.toml has no such e2e task — renamed or removed, fix the workflow", name)
		}
	}

	// 律文：每任务要么在 CI、要么登记白名单（带理由）。
	for _, name := range mise {
		if ciSet[name] {
			continue
		}
		reason, ok := e2eciWhitelist[name]
		if !ok {
			t.Errorf("e2e task %s is neither invoked in ci.yml nor registered in e2eciWhitelist — add a CI job or register an exemption with a reason (G20 e2e↔CI 对账)", name)
			continue
		}
		if reason == "" {
			t.Errorf("e2e↔CI whitelist entry %s must carry a reason", name)
		}
	}

	// 条目保鲜：白名单必须仍指向在册任务，且尚未被 CI 收编。
	for name := range e2eciWhitelist {
		if !miseSet[name] {
			t.Errorf("e2e↔CI whitelist entry %s points at a task that no longer exists in mise.toml — delete or update the entry", name)
		}
		if ciSet[name] {
			t.Errorf("e2e↔CI whitelist entry %s is now invoked in ci.yml; delete the stale entry", name)
		}
	}
}

// TestE2ETaskScriptsExist：每个 e2e 任务必须调用 ≥1 个 sh 脚本，且脚本
// 在现树存在（脚本改名忘改 mise 不得只在运行期红——R12-L5）。
func TestE2ETaskScriptsExist(t *testing.T) {
	tasks := parseMiseTasks(readFileLF(t, "mise.toml"))
	root := repoRoot(t)
	var checked int
	for name, body := range tasks {
		if !strings.HasPrefix(name, "e2e:") {
			continue
		}
		scripts := taskScripts(body)
		if len(scripts) == 0 {
			t.Errorf("e2e task %s runs no sh script — the task body shape changed; fix the guard or the task", name)
			continue
		}
		for _, rel := range scripts {
			checked++
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Errorf("e2e task %s points at missing script %s: %v — rename the script and the task in the same commit", name, rel, err)
			}
		}
	}
	// 防盲：13 个 e2e 任务各至少一条脚本。
	if checked < 13 {
		t.Fatalf("only %d e2e script references checked (expected >= 13) — guard is blind, fix the scan", checked)
	}
}

// TestVerifyClassTasksReconciled：本地 verify 类门禁任务必须从 CI 可达或
// 显式登记（R12-M5）。可达性含任务引用闭包（generate:verify → generate:all
// → generate:proto:local 等），因此 CI 只需引用顶层门禁任务。
func TestVerifyClassTasksReconciled(t *testing.T) {
	tasks := parseMiseTasks(readFileLF(t, "mise.toml"))
	ciCalls := parseCIMiseCalls(readFileLF(t, ".github/workflows/ci.yml"))
	reached := ciReachableTasks(tasks, ciCalls)

	verifyClass := 0
	usedExemptions := map[string]bool{}
	var names []string
	for name := range tasks {
		if isVerifyClassTask(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		verifyClass++
		if reached[name] {
			continue
		}
		reason, ok := ciTaskExemptions[name]
		if !ok {
			t.Errorf("verify-class mise task %s is neither reachable from ci.yml nor registered in ciTaskExemptions — add a CI job or register the reason (R12-M5)", name)
			continue
		}
		if reason == "" {
			t.Errorf("ciTaskExemptions entry %s must carry a reason", name)
		}
		usedExemptions[name] = true
	}
	// 防盲：verify/generate/console 族实测 >= 10（少于即扫描退化）。
	if verifyClass < 10 {
		t.Fatalf("only %d verify-class tasks found (expected >= 10) — guard is blind, fix the scan", verifyClass)
	}

	// 双向保鲜：豁免不再命中即红。
	var stale []string
	for name, reason := range ciTaskExemptions {
		if reason == "" {
			t.Errorf("ciTaskExemptions entry %s must carry a reason", name)
		}
		if !usedExemptions[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("ciTaskExemptions entry %s no longer covers a real gap (task removed or now reachable from CI); remove the entry", name)
	}
}

// TestVerifyHermeticInCI：换装 hermeticity 回归锚必须在 CI 直接跑（它是
// 2026-10-11 换装事故的回归锚，不能只活在本地门禁/runbook 的记忆里——
// R12-M5）。豁免通道不适用于本任务：verify:hermetic 必须被 CI 直接调用。
func TestVerifyHermeticInCI(t *testing.T) {
	calls := parseCIMiseCalls(readFileLF(t, ".github/workflows/ci.yml"))
	found := false
	for _, name := range calls {
		if name == "verify:hermetic" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ci.yml no longer runs `mise run verify:hermetic` — the hermetic build channel regression anchor is left to human memory (R12-M5)")
	}
}

// TestGenerateVerifyCoversModuleFiles：零漂移断言必须覆盖 go.mod/go.sum/
// go.work（R09-L4）——wire 任务会跑 `go mod tidy`，旧断言只 diff 生成物，
// 依赖漂移在门禁里被静默放行。
func TestGenerateVerifyCoversModuleFiles(t *testing.T) {
	tasks := parseMiseTasks(readFileLF(t, "mise.toml"))
	body, ok := tasks["generate:verify"]
	if !ok {
		t.Fatal("mise.toml has no generate:verify task — the codegen drift gate is gone")
	}
	if !strings.Contains(body, "git diff --exit-code") {
		t.Fatalf("generate:verify no longer asserts a zero git diff:\n%s", body)
	}
	for _, path := range []string{"genproto", "internal/config", "internal/assembly", "go.mod", "go.sum", "go.work", "go.work.sum"} {
		if !strings.Contains(body, path) {
			t.Errorf("generate:verify diff scope does not cover %s — generated artifacts or the module files can drift silently (R09-L4)", path)
		}
	}
	if !strings.Contains(body, `task = "generate:all"`) {
		t.Errorf("generate:verify no longer runs generate:all — the drift gate is not regenerating anything")
	}
}

// TestCIHasWindowsCrossCompile：CI 必须有 Windows 交叉编译门禁（R09-L7）。
// 只编译不测试（生产部署 Linux-only；Windows 是仓内声明的开发平台之二，
// Unix-only 符号漂移此前只在开发者本机显形）。
func TestCIHasWindowsCrossCompile(t *testing.T) {
	ci := readFileLF(t, ".github/workflows/ci.yml")
	const want = "GOOS=windows GOARCH=amd64 go build ./..."
	if !strings.Contains(ci, want) {
		t.Errorf("ci.yml has no %q step — cross-platform compile drift stays invisible in CI (R09-L7)", want)
	}
}

// TestE2ECIAccountShapes：解析器形状验证（内联文本，注入式）——非 e2e
// 任务不收、调用去重、跨行提取、任务引用/脚本提取。
func TestE2ECIAccountShapes(t *testing.T) {
	miseToml := `
[tools]
go = "1.26.6"

[tasks."build"]
run = "sh build.sh"

[tasks."e2e:dind"]
run = "sh e2e/dind-smoke.sh"

[tasks."e2e:k3s-ha"]
run = "sh e2e/dind-k3s-ha.sh"

[tasks."generate:all"]
run = [
  { task = "tools:protoc-plugins" },
  { task = "generate:proto:local" },
]
`
	tasks := parseMiseTasks(miseToml)
	if len(tasks) != 4 {
		t.Fatalf("task parse: want 4 tasks, got %d: %v", len(tasks), tasks)
	}
	got := parseMiseE2ETasks(miseToml)
	if len(got) != 2 || got[0] != "e2e:dind" || got[1] != "e2e:k3s-ha" {
		t.Fatalf("mise parse: want [e2e:dind e2e:k3s-ha], got %v", got)
	}
	refs := taskRefs(tasks["generate:all"])
	if len(refs) != 2 || refs[0] != "tools:protoc-plugins" || refs[1] != "generate:proto:local" {
		t.Fatalf("task refs: want [tools:protoc-plugins generate:proto:local], got %v", refs)
	}
	scripts := taskScripts(tasks["e2e:dind"])
	if len(scripts) != 1 || scripts[0] != "e2e/dind-smoke.sh" {
		t.Fatalf("task scripts: want [e2e/dind-smoke.sh], got %v", scripts)
	}

	ciYml := `
      - name: smoke
        run: mise run e2e:dind
      - name: smoke again
        run: mise run e2e:dind
      - name: ha
        run: |
          mise run e2e:k3s-ha
      - name: codegen
        run: mise run generate:verify
      - name: unrelated
        run: mise run test
`
	got = parseCIE2ECalls(ciYml)
	if len(got) != 2 || got[0] != "e2e:dind" || got[1] != "e2e:k3s-ha" {
		t.Fatalf("ci parse: want [e2e:dind e2e:k3s-ha], got %v", got)
	}
	calls := parseCIMiseCalls(ciYml)
	if len(calls) != 4 {
		t.Fatalf("ci calls: want 4 unique tasks, got %v", calls)
	}
	if !sort.StringsAreSorted(got) || strings.Contains(strings.Join(got, ","), "test") {
		t.Fatalf("ci parse ordering/prefix drift: %v", got)
	}
	// 可达性闭包：CI 直接调 generate:verify → 经引用到达 generate:all 的子任务。
	tasks["generate:verify"] = "run = [\n  { task = \"generate:all\" },\n]\n"
	tasks["generate:proto:local"] = "run = \"buf generate\"\n"
	tasks["tools:protoc-plugins"] = "run = \"go install\"\n"
	reached := ciReachableTasks(tasks, []string{"generate:verify"})
	for _, want := range []string{"generate:verify", "generate:all", "generate:proto:local", "tools:protoc-plugins"} {
		if !reached[want] {
			t.Errorf("reachability closure must include %s; got %v", want, reached)
		}
	}
	if reached["e2e:dind"] {
		t.Errorf("reachability closure leaked into an unrelated task: %v", reached)
	}
}
