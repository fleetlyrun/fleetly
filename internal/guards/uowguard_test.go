package guards

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0080 验收锚守卫：执行器词汇的圈禁（显式 bundle UoW 的两条静态承诺）。
//
// ①domain 生产代码零 state.Runner 引用——执行接缝只允许出现在契约定义处
//（shared/state）；②database/sql import 全仓仅存 shared/state（契约面的
// sql.Result/Row/Rows 类型）与 persistence（机械面）。违反即红——逐方法
// runner 传参或裸 SQL 上抬至此被静态挡回。

const stateContractPkg = "internal/domain/shared/state/"

// walkRepoGoFiles 遍历仓库生产 .go 文件（跳过测试文件与生成物目录），逐
// 文件回调相对路径与内容。
func walkRepoGoFiles(t *testing.T, fn func(rel, source string)) {
	t.Helper()
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == ".tmp-bin" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fn(rel, string(source))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
}

// TestDomainRepoInterfacesRunnerFree：domain 层生产文件不含 state.Runner
// 词汇（shared/state 契约定义豁免）。参数式回归（repo 方法重新携带执行器）
// 在此红（ADR-0080 验收锚）。
func TestDomainRepoInterfacesRunnerFree(t *testing.T) {
	walkRepoGoFiles(t, func(rel, source string) {
		if !strings.HasPrefix(rel, "internal/domain/") {
			return
		}
		if strings.HasPrefix(rel, stateContractPkg) {
			return
		}
		if strings.Contains(source, "state.Runner") {
			t.Errorf("%s references state.Runner — execution seam vocabulary is confined to shared/state (ADR-0080)", rel)
		}
	})
}

// TestDatabaseSQLConfined：database/sql import 只准出现在 shared/state 契约
// 面与 persistence 机械面（ADR-0080 验收锚；application/interfaces 持执行
// 器类型即红）。
func TestDatabaseSQLConfined(t *testing.T) {
	walkRepoGoFiles(t, func(rel, source string) {
		if !strings.Contains(source, `"database/sql"`) {
			return
		}
		if strings.HasPrefix(rel, stateContractPkg) ||
			strings.HasPrefix(rel, "internal/infrastructure/persistence/") {
			return
		}
		t.Errorf("%s imports database/sql — driver types are confined to shared/state and persistence (ADR-0080)", rel)
	})
}
