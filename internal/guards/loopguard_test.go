package guards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEngineLoopSkeletonSingular：kick/tick 收敛循环骨架全仓唯一一份
// （架构 §0 修正 2："每段只许有一份"——六份循环骨架的历史不再发生）。
// engine 树内 time.NewTicker 只准出现在 loop.go（骨架本体）与测试。
func TestEngineLoopSkeletonSingular(t *testing.T) {
	root := repoRoot(t)
	engineDir := filepath.Join(root, "internal", "engine")
	err := filepath.WalkDir(engineDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if name == "loop.go" {
			return nil // 骨架本体
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "time.NewTicker") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s builds its own ticker loop — converge via engine.NewLoop (架构 §0 唯一骨架)", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
