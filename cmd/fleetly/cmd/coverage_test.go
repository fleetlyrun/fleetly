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
//（revisions diff / builds list 曾双双漏网：顶层名词检查全绿、组员零
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
//（N0 修复批 A5 补断言，此前退出码四态只有注释没有测试）。
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
