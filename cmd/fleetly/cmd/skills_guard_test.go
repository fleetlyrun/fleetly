package cmd

// Skills-CLI 一致性守卫（F1.13，ADR-0031 决策 4）：skills/**/SKILL.md 里的
// fleetly 调用是字面可执行命令（机器契约的文档投影）。执法面三路：围栏代码
// 块内 fleetly 行 + 围栏外散文/表格的单反引号内联 span（后者曾失明——
// 分诊表引用死动词存活至今的根因）+ 三章节在场性（批 D 扩面，ADR-0031
// 决策 2 的正文章节约定由测试承载）。逐条经进程内真实 CLI 逐字执行
// （dialClient 接缝毒化——拨号即拒，零网络零副作用）：退出码 0/1 = 命令
// 路径与旗标解析成立（动词+旗标名+值类型都在册）；64 = 命令本身不再合法
// （动词消亡/旗标改名或删除/值类型错）即时红。围栏纪律（无 shell 元字符）
// 与 frontmatter（name=目录名、description 非空）同批执法。反向覆盖不执法
// （skills 是工作流不是命令目录——目录真源是 --help/schema）。

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lynx-go/commands"

	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// skillsRoot 是仓内 skills 目录（测试 cwd = 本包目录）。
var skillsRoot = filepath.Join("..", "..", "..", "skills")

// skillsFrontmatterRe 剥取 YAML frontmatter 块（首个 --- 围栏）。
var skillsFrontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)

// skillRequiredSections 是正文章节约定的在场性清单（ADR-0031 决策 2，
// 批 D 进守卫）：三章节各司一职——触发场景（何时用它）、命令序列（围栏
// 调用）、失败分诊（症状→探针→动作）。缺章即红：章节约定是形态冻结面，
// 偏离形态须修订 ADR（ADR-0031 后果节），不接受局部漂移。
var skillRequiredSections = []string{"## When to use", "## Command sequences", "## Failure triage"}

// hasSectionHeading 报告 body 是否含该章节标题行（行级精确匹配：标题
// 文字与层级都是约定面——`###` 降级同属漂移）。
func hasSectionHeading(body, heading string) bool {
	for _, raw := range strings.Split(body, "\n") {
		if strings.TrimSpace(raw) == heading {
			return true
		}
	}
	return false
}

func TestSkillsFencedCommandsResolve(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(skillsRoot, "*", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no skills found under %s (the first batch ships three; a missing directory is a packaging bug)", skillsRoot)
	}
	checked := 0
	inlined := 0
	for _, path := range files {
		dir := filepath.Base(filepath.Dir(path)) // skill 名 = 目录名
		body, err := os.ReadFile(path)           //nolint:gosec // 仓内文档固定路径
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, e := range skillLint(dir, string(body)) {
			t.Error(e)
		}
		checked += len(fencedFleetlyLines(string(body)))
		inlined += len(inlineFleetlySpans(string(body)))
	}
	if checked == 0 {
		t.Fatalf("no fenced fleetly invocations found under %s — the guard is the point of the literal-fence convention", skillsRoot)
	}
	t.Logf("skills guard: %d fenced + %d inline fleetly invocations verified against the live CLI", checked, inlined)
}

// TestSkillsGuardRedLight 是守卫的常驻红灯实验（ADR-0031 验收锚）：死动词、
// 死旗标、旗标落在位置参数之后、围栏元字符、frontmatter 名不符、内联死
// 动词、缺章节七种漂移必须被咬住——守卫自身失明即红。断言按"错误集中
// 含目标错误"判（红灯夹具体不携带齐备章节时，章节错误先于目标错误出现，
// 目标错误在不在才是本实验的判据）。
func TestSkillsGuardRedLight(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "dead-verb",
			body: "---\nname: dead-verb\ndescription: d\n---\n```bash\nfleetly databases creat NAME\n```\n",
			want: `no longer resolves`,
		},
		{
			name: "dead-flag",
			body: "---\nname: dead-flag\ndescription: d\n---\n```bash\nfleetly databases list --projectx P\n```\n",
			want: `no longer resolves`,
		},
		{
			name: "flag-after-positional",
			body: "---\nname: flag-after-positional\ndescription: d\n---\n```bash\nfleetly networks create --project P default --egress-none\n```\n",
			want: `no longer resolves`,
		},
		{
			name: "shell-metacharacter-in-fence",
			body: "---\nname: shell-metacharacter-in-fence\ndescription: d\n---\n```bash\nfleetly events list --limit 50 | more\n```\n",
			want: `must stay literal`,
		},
		{
			// & 同受元字符禁令（D25）：后台化/命令拼接进围栏行同样劈链。
			name: "shell-metacharacter-ampersand-in-fence",
			body: "---\nname: shell-metacharacter-ampersand-in-fence\ndescription: d\n---\n```bash\nfleetly events list --limit 50 & more\n```\n",
			want: `must stay literal`,
		},
		{
			name: "frontmatter-name-mismatch",
			body: "---\nname: other\ndescription: d\n---\n```bash\nfleetly events list --limit 50\n```\n",
			want: `must equal the directory name`,
		},
		{
			// 内联执法（守卫扩面）：散文/表格里的死动词同受执法——这是
			// database-provision 分诊表死动词存活至今的失明面。
			name: "inline-dead-verb",
			body: "---\nname: inline-dead-verb\ndescription: d\n---\nTriage row: probe with `fleetly bogus-verb list --json` next.\n",
			want: `inline command no longer resolves`,
		},
		{
			// 三章节在场性（批 D 扩面）：缺章即红——前两章在场、缺分诊章
			// 的形态，唯一错误须点名缺失章节（ADR-0031 决策 2）。
			name: "missing-section",
			body: "---\nname: missing-section\ndescription: d\n---\n## When to use\n\n- x\n\n## Command sequences\n\n```bash\nfleetly events list --limit 50\n```\n",
			want: `missing required section "## Failure triage"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := skillLint(tc.name, tc.body)
			for _, e := range errs {
				if strings.Contains(e.Error(), tc.want) {
					return
				}
			}
			t.Fatalf("guard went blind: expected an error containing %q, got %v", tc.want, errs)
		})
	}
}

// skillLint 是守卫的纯检查核（真实 skills 与红灯实验共用）：frontmatter
// （ADR-0031 决策 1/3）+ 三章节在场性（决策 2，批 D）+ 围栏 fleetly 行与
// 内联 fleetly span 的 CLI 解析级契约。
func skillLint(dir, body string) []error {
	var errs []error
	m := skillsFrontmatterRe.FindStringSubmatch(body)
	if m == nil {
		return []error{fmt.Errorf("skills/%s/SKILL.md: missing YAML frontmatter (--- name/description ---)", dir)}
	}
	var name, description string
	for _, ln := range strings.Split(m[1], "\n") {
		key, val, found := strings.Cut(ln, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = strings.TrimSpace(val)
		case "description":
			description = strings.TrimSpace(val)
		}
	}
	if name != dir {
		errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: frontmatter name %q must equal the directory name", dir, name))
	}
	if description == "" {
		errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: frontmatter description must be non-empty (hosts load skills by it)", dir))
	}
	// 三章节在场性（ADR-0031 决策 2）：触发/命令/分诊缺一即红。
	for _, section := range skillRequiredSections {
		if !hasSectionHeading(body, section) {
			errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: missing required section %q (ADR-0031 decision 2 fixes the body outline: trigger scenes / command sequences / failure triage)", dir, section))
		}
	}
	// 拨号接缝毒化：检查核永不触网（含 localhost 开发实例），动词在解析与
	// 本地校验后于拨号处确定性失败——退出码 0/1 = 命令路径与旗标解析成立，
	// 64 = 围栏行本身不再合法（动词消亡/旗标漂移/值类型错/旗标序错）。
	origDial := dialClient
	dialClient = func(string, ...sdk.Option) (*sdk.Client, error) {
		return nil, fmt.Errorf("skills guard: dial disabled")
	}
	defer func() { dialClient = origDial }()

	for _, line := range fencedFleetlyLines(body) {
		for _, meta := range []string{"|", "&", "$", "`", ";", "\\", ">", "<"} {
			if strings.Contains(line, meta) {
				errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: fenced fleetly line must stay literal (move pipelines to prose; found %q):\n  %s",
					dir, meta, line))
			}
		}
		if err := checkInvocation("fenced", dir, line); err != nil {
			errs = append(errs, err)
		}
	}
	// 内联面同受执法：散文/表格的单反引号 fleetly span（围栏纪律的元字符
	// 禁令是围栏专属——span 天然装不下反引号，表格单元格容不下裸 |，其余
	// 形态按同一解析级契约裁决）。
	for _, span := range inlineFleetlySpans(body) {
		if err := checkInvocation("inline", dir, span); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// checkInvocation 把一条字面调用逐字喂给毒化拨号的真实 CLI：退出码 0/1 =
// 命令路径与旗标解析成立（拨号在本地校验后确定性失败），64 = 命令本身
// 不再合法（动词消亡/旗标漂移/值类型错/旗标序错）即时红。kind 标注执法面
// （fenced/inline），错误可辨来源。
func checkInvocation(kind, dir, line string) error {
	// 剥程序名 token（args 形态同 os.Args[1:]）。
	args := strings.Fields(strings.TrimPrefix(line, "fleetly "))
	code, _, stderr := runCLISilent(args...)
	if code == exitUsage {
		return fmt.Errorf("skills/%s/SKILL.md: %s command no longer resolves against the CLI (exit 64):\n  %s\n  stderr: %s",
			dir, kind, line, strings.TrimSpace(strings.SplitN(stderr, "\n", 2)[0]))
	}
	return nil
}

// runCLISilent 同 runCLI 但失败不 t.Fatal（守卫收集全部错误一次性报告）。
func runCLISilent(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
	code := NewApp(testBuildInfo).Run(context.Background(), env, args)
	return code, stdout.String(), stderr.String()
}

// fencedFleetlyLines 抽取围栏代码块内以 fleetly 开头的行；围栏外（散文/
// 缩进块/表格）不在此采集（内联面归 inlineFleetlySpans）。
func fencedFleetlyLines(body string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		line := stripInvocationDecorations(trimmed)
		if !strings.HasPrefix(line, "fleetly ") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// inlineFleetlySpans 抽取围栏外文本中单反引号内联代码 span 里以 fleetly
// 开头的命令（散文与分诊表的行内调用同受执法——死动词曾在表内存活至今
// 的根因即守卫只查围栏）。span 内容须为 "fleetly " 后随至少一个 token，
// 裸 `fleetly`（无空格后继）跳过；$ 提示符前缀与 " #" 尾注释同款剥除。
// 单反引号成对切分：偶数段是普通文本、奇数段是 span（成对反引号包裹的
// markdown 形态；仓内 skills 无 “ 双反引号 span，按单层处理）。
func inlineFleetlySpans(body string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		parts := strings.Split(raw, "`")
		for i := 1; i+1 < len(parts); i += 2 {
			line := stripInvocationDecorations(parts[i])
			if !strings.HasPrefix(line, "fleetly ") {
				continue
			}
			if strings.TrimSpace(strings.TrimPrefix(line, "fleetly")) == "" {
				continue // 裸 fleetly：指程序本身不是调用
			}
			out = append(out, line)
		}
	}
	return out
}

// stripInvocationDecorations 剥 $ 提示符前缀与 " #" 尾注释（围栏行与内联
// span 共用的字面化归一）。
func stripInvocationDecorations(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "$ ")
	if i := strings.Index(line, " # "); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}
