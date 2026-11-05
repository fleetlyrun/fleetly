package guards

// 提案 2026-11-05 C8：blank import ↔ providers 子目录双向对账。
// 漏挂 = 可选能力静默停用——四个可选 kind 的装配函数在零在册时返 nil
// 无告警（NewRegistryProvider 等），忘加 blank import 时 Registry/Logging
// 面在 daemon 上静默消失，用户侧到 prepare/查询才精确失败；多挂 = 死
// 进口。importguard 只执法方向（providers 只准 cmd 消费），不钉清单——
// 本守卫补清单面（双向保鲜）。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// blankImportPattern 匹配 main.go 里 providers 子包的 blank import。
var blankImportPattern = regexp.MustCompile(`_ "github\.com/fleetlyrun/fleetly/internal/infrastructure/providers/([a-z0-9]+)"`)

func TestProviderBlankImportsReconcile(t *testing.T) {
	mainSrc := readFileLF(t, "cmd/fleetlyd/main.go")

	imported := map[string]bool{}
	for _, m := range blankImportPattern.FindAllStringSubmatch(mainSrc, -1) {
		imported[m[1]] = true
	}
	if len(imported) == 0 {
		t.Fatal("no provider blank imports found in cmd/fleetlyd/main.go — guard is blind, fix the scan")
	}

	// providers 子目录（含 .go 文件才算 provider 包；testdata 排除）。
	root := repoRoot(t)
	providersRoot := filepath.Join(root, "internal", "infrastructure", "providers")
	dirs := map[string]bool{}
	err := filepath.WalkDir(providersRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			if strings.HasSuffix(d.Name(), ".go") {
				rel, relErr := filepath.Rel(providersRoot, path)
				if relErr == nil {
					parts := strings.Split(filepath.ToSlash(rel), "/")
					if len(parts) > 1 && parts[len(parts)-2] != "testdata" {
						dirs[parts[0]] = true
					}
				}
			}
			return nil
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no provider packages found under internal/infrastructure/providers — guard is blind, fix the scan")
	}

	for name := range dirs {
		if !imported[name] {
			t.Errorf("provider package %q is not blank-imported in cmd/fleetlyd/main.go — its factories never register and the capability stays silently disabled (add the blank import)", name)
		}
	}
	for name := range imported {
		if !dirs[name] {
			t.Errorf("cmd/fleetlyd/main.go blank-imports provider %q but internal/infrastructure/providers/%s has no Go package — dead import, remove it", name, name)
		}
	}
}
