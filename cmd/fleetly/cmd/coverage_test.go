package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAllVerbsHaveDualGoldens 是 CLI 机器契约守卫（F0.22/F0.24）：每个注册
// 动词必须有 ≥1 个人类形态 golden 与 ≥1 个 --json 形态 golden——归档项目
// 23 命令缺 --json 的缺口不重演（架构文档 §11）。新增动词时同步补
// golden（go test ./cmd/fleetly/cmd -update）。文件名把动词内空格归一为
// 连字符（与 goldenFile 的归一一致）。
//
// N0 修复批（A5）：枚举递归进动词组——组本身有 golden 不代表组员有
// （revisions diff / builds list 曾双双漏网：顶层名词检查全绿、组员零
// golden）。
func TestAllVerbsHaveDualGoldens(t *testing.T) {
	if *goldenUpdate {
		t.Skip("golden regeneration in progress; coverage re-checked on the next plain run")
	}
	app := NewApp(testBuildInfo)
	names := app.Names()
	assert.NotEmpty(t, names)

	for _, name := range names {
		verbNames := []string{name}
		cmd, ok := app.Lookup(name)
		if ok {
			if fv, ok := cmd.(*flaggedVerb); ok {
				for _, sub := range fv.subcommands {
					verbNames = append(verbNames, name+" "+sub)
				}
			}
		}
		for _, vn := range verbNames {
			file := strings.ReplaceAll(vn, " ", "-")
			human, err := filepath.Glob(filepath.Join("testdata", "golden", file+".golden"))
			assert.NoError(t, err)
			assert.NotEmpty(t, human, "verb %q has no human-form golden (%s.golden)", vn, file)

			jsonForm, err := filepath.Glob(filepath.Join("testdata", "golden", file+"-json.golden"))
			assert.NoError(t, err)
			assert.NotEmpty(t, jsonForm, "verb %q has no --json golden (%s-json.golden)", vn, file)
		}
	}
}

// TestExitCodesAreStable 钉死退出码机器契约：0 成功、2 有变化（diff 类）、
// 64 用法错误（未知动词/旗标解析失败/缺必填参数）——脚本能分支的承诺
// （N0 修复批 A5 补断言，此前退出码四态只有注释没有测试）。
func TestExitCodesAreStable(t *testing.T) {
	t.Run("unknown verb is 64", func(t *testing.T) {
		code, _, _ := runCLI(t, "definitely-not-a-verb")
		assert.Equal(t, 64, code)
	})

	t.Run("unknown subcommand is 64", func(t *testing.T) {
		code, _, _ := runCLI(t, "revisions", "definitely-not-a-sub")
		assert.Equal(t, 64, code)
	})

	t.Run("bad flag is 64", func(t *testing.T) {
		code, _, _ := runCLI(t, "logs", "--app", "x", "--no-such-flag")
		assert.Equal(t, 64, code)
	})

	t.Run("missing required flag is 64", func(t *testing.T) {
		code, _, _ := runCLI(t, "logs")
		assert.Equal(t, 64, code)
		code, _, _ = runCLI(t, "revisions", "diff", "--app", "x")
		assert.Equal(t, 64, code)
	})

	t.Run("diff with changes is 2", func(t *testing.T) {
		h := newGoldenHarness(t)
		appID := goldenSeedDeploy(t, h)
		code, _, stderr := runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.26")
		if code != 0 {
			t.Fatalf("second deploy: code=%d stderr=%q", code, stderr)
		}
		code, _, stderr = runCLI(t, "revisions", "diff", "--app", appID, "--from", "1", "--to", "2")
		assert.Equal(t, 2, code, "revisions diff with entries must exit 2 (stderr %q)", stderr)
		// errChanges 渲染为空（render 单点）；框架仍打一行换行。
		assert.True(t, stderr == "" || stderr == "\n", "changes exit must not render an error envelope, got %q", stderr)
	})
}

// TestFlagVerbsRejectPositionalArgs 是取参形态裁决的执法面（ADR-0006 附录，
// D25）：旗标形态动词（usage 不含位置参数）对多余位置参数一律 64，不再裸
// 放行——位置参数会被 Agent 误当合法取参面，静默吞掉是第三种取参形态的
// 滋生口。守卫在拨号前（noArgs 先于连接），无需夹具。逐一枚举全部旗标形
// 态动词——新增旗标形态动词必须入表（漏表的动词多余位置参数会裸放行）。
func TestFlagVerbsRejectPositionalArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"deploy", []string{"deploy", "STRAY"}},
		{"rollback", []string{"rollback", "STRAY"}},
		{"deployments list", []string{"deployments", "list", "STRAY"}},
		{"deployments wait", []string{"deployments", "wait", "STRAY"}},
		{"revisions list", []string{"revisions", "list", "STRAY"}},
		{"revisions diff", []string{"revisions", "diff", "STRAY"}},
		{"builds list", []string{"builds", "list", "STRAY"}},
		{"builds wait", []string{"builds", "wait", "STRAY"}},
		{"builds logs", []string{"builds", "logs", "STRAY"}},
		{"routes list", []string{"routes", "list", "STRAY"}},
		{"nodes list", []string{"nodes", "list", "STRAY"}},
		{"nodes enroll", []string{"nodes", "enroll", "STRAY"}},
		{"nodes drain", []string{"nodes", "drain", "STRAY"}},
		{"tasks create", []string{"tasks", "create", "STRAY"}},
		{"tasks list", []string{"tasks", "list", "STRAY"}},
		{"tasks get", []string{"tasks", "get", "STRAY"}},
		{"tasks scale", []string{"tasks", "scale", "STRAY"}},
		{"tasks stop", []string{"tasks", "stop", "STRAY"}},
		{"tasks delete", []string{"tasks", "delete", "STRAY"}},
		{"tasks renew", []string{"tasks", "renew", "STRAY"}},
		{"runs list", []string{"runs", "list", "STRAY"}},
		{"runs get", []string{"runs", "get", "STRAY"}},
		{"runs stop", []string{"runs", "stop", "STRAY"}},
		{"runs wait", []string{"runs", "wait", "STRAY"}},
		{"schedules create", []string{"schedules", "create", "STRAY"}},
		{"schedules list", []string{"schedules", "list", "STRAY"}},
		{"schedules get", []string{"schedules", "get", "STRAY"}},
		{"schedules trigger", []string{"schedules", "trigger", "STRAY"}},
		{"schedules delete", []string{"schedules", "delete", "STRAY"}},
		{"events list", []string{"events", "list", "STRAY"}},
		{"events follow", []string{"events", "follow", "STRAY"}},
		{"logs", []string{"logs", "STRAY"}},
		{"projects list", []string{"projects", "list", "STRAY"}},
		{"projects delete", []string{"projects", "delete", "STRAY"}},
		{"apps list", []string{"apps", "list", "STRAY"}},
		{"apps delete", []string{"apps", "delete", "STRAY"}},
		{"secrets list", []string{"secrets", "list", "STRAY"}},
		{"configs list", []string{"configs", "list", "STRAY"}},
		{"databases list", []string{"databases", "list", "STRAY"}},
		{"uploads list", []string{"uploads", "list", "STRAY"}},
		{"tokens list", []string{"tokens", "list", "STRAY"}},
		{"users list", []string{"users", "list", "STRAY"}},
		{"roles list", []string{"roles", "list", "STRAY"}},
		{"teams list", []string{"teams", "list", "STRAY"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := runCLI(t, tc.args...)
			assert.Equal(t, exitUsage, code, "flag-form verb must reject stray positional args: %v", tc.args)
		})
	}
}
