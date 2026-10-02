package cmd

// Skills-CLI 一致性守卫（F1.13，ADR-0031 决策 4）：skills/**/SKILL.md 围栏内
// 的 fleetly 行是字面可执行调用（机器契约的文档投影）。逐行经进程内真实
// CLI 逐字执行（dialClient 接缝毒化——拨号即拒，零网络零副作用）：退出码
// 0/1 = 命令路径与旗标解析成立（动词+旗标名+值类型都在册）；64 = 围栏行
// 本身不再合法（动词消亡/旗标改名或删除/值类型错）即时红。围栏纪律（无
// shell 元字符）与 frontmatter（name=目录名、description 非空）同批执法。
// 反向覆盖不执法（skills 是工作流不是命令目录——目录真源是 --help/schema）。

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

func TestSkillsFencedCommandsResolve(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(skillsRoot, "*", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no skills found under %s (the first batch ships three; a missing directory is a packaging bug)", skillsRoot)
	}
	checked := 0
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
	}
	if checked == 0 {
		t.Fatalf("no fenced fleetly invocations found under %s — the guard is the point of the literal-fence convention", skillsRoot)
	}
	t.Logf("skills guard: %d fenced fleetly invocations verified against the live CLI", checked)
}

// TestSkillsGuardRedLight 是守卫的常驻红灯实验（ADR-0031 验收锚）：死动词、
// 死旗标、旗标落在位置参数之后三种漂移必须被咬住——守卫自身失明即红。
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
			name: "frontmatter-name-mismatch",
			body: "---\nname: other\ndescription: d\n---\n```bash\nfleetly events list --limit 50\n```\n",
			want: `must equal the directory name`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := skillLint(tc.name, tc.body)
			if len(errs) == 0 {
				t.Fatalf("guard went blind: expected an error containing %q", tc.want)
			}
			if !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("wrong error: want %q, got %q", tc.want, errs[0])
			}
		})
	}
}

// skillLint 是守卫的纯检查核（真实 skills 与红灯实验共用）：frontmatter
// （ADR-0031 决策 1/3）+ 逐条围栏 fleetly 行的 CLI 解析级契约。
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
	// 拨号接缝毒化：检查核永不触网（含 localhost 开发实例），动词在解析与
	// 本地校验后于拨号处确定性失败——退出码 0/1 = 命令路径与旗标解析成立，
	// 64 = 围栏行本身不再合法（动词消亡/旗标漂移/值类型错/旗标序错）。
	origDial := dialClient
	dialClient = func(string, ...sdk.Option) (*sdk.Client, error) {
		return nil, fmt.Errorf("skills guard: dial disabled")
	}
	defer func() { dialClient = origDial }()

	for _, line := range fencedFleetlyLines(body) {
		for _, meta := range []string{"|", "$", "`", ";", "\\", ">", "<"} {
			if strings.Contains(line, meta) {
				errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: fenced fleetly line must stay literal (move pipelines to prose; found %q):\n  %s",
					dir, meta, line))
			}
		}
		// 剥围栏行内的程序名 token（args 形态同 os.Args[1:]）。
		args := strings.Fields(strings.TrimPrefix(line, "fleetly "))
		code, _, stderr := runCLISilent(args...)
		if code == exitUsage {
			errs = append(errs, fmt.Errorf("skills/%s/SKILL.md: fenced command no longer resolves against the CLI (exit 64):\n  %s\n  stderr: %s",
				dir, line, strings.TrimSpace(strings.SplitN(stderr, "\n", 2)[0])))
		}
	}
	return errs
}

// runCLISilent 同 runCLI 但失败不 t.Fatal（守卫收集全部错误一次性报告）。
func runCLISilent(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
	code := NewApp(testBuildInfo).Run(context.Background(), env, args)
	return code, stdout.String(), stderr.String()
}

// fencedFleetlyLines 抽取围栏代码块内以 fleetly 开头的行（剥 $ 提示符前缀
// 与 " #" 尾注释）；围栏外（散文/缩进块）不采集。
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
		line := strings.TrimPrefix(trimmed, "$ ")
		if !strings.HasPrefix(line, "fleetly ") {
			continue
		}
		if i := strings.Index(line, " # "); i >= 0 {
			line = line[:i]
		}
		out = append(out, strings.TrimSpace(line))
	}
	return out
}
