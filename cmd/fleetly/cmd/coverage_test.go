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
func TestAllVerbsHaveDualGoldens(t *testing.T) {
	if *goldenUpdate {
		t.Skip("golden regeneration in progress; coverage re-checked on the next plain run")
	}
	app := NewApp(testBuildInfo)
	names := app.Names()
	assert.NotEmpty(t, names)

	for _, name := range names {
		file := strings.ReplaceAll(name, " ", "-")
		human, err := filepath.Glob(filepath.Join("testdata", "golden", file+".golden"))
		assert.NoError(t, err)
		assert.NotEmpty(t, human, "verb %q has no human-form golden (%s.golden)", name, file)

		jsonForm, err := filepath.Glob(filepath.Join("testdata", "golden", file+"-json.golden"))
		assert.NoError(t, err)
		assert.NotEmpty(t, jsonForm, "verb %q has no --json golden (%s-json.golden)", name, file)
	}
}
