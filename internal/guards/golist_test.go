package guards

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// listedPackage 是 go list 的最小投影：仓库相对包路径 + import 集。
// 依赖方向契约测试消费它而不是自研 AST 扫描（AGENTS.md：不再新增 AST
// 守卫；依赖图以 go list 为准）。
type listedPackage struct {
	rel        string   // 仓库相对包路径（斜杠分隔）
	imports    []string // 生产 import（不含 _test.go 的 import）
	allImports []string // 生产 + 测试 + 外部测试 import（测试也在执法面时用）
}

// listModulePackages 返回 dir（仓库相对路径；"" = 根）的生产包投影。
func listModulePackages(t *testing.T, dir string) []listedPackage {
	return listPackages(t, dir, false)
}

// listModulePackagesAll 返回 dir 的全部包投影（含纯测试包；allImports 覆盖
// _test.go 与 _test 外部测试包的 import——测试同样受依赖方向执法）。
func listModulePackagesAll(t *testing.T, dir string) []listedPackage {
	return listPackages(t, dir, true)
}

// listWorkspacePackages 列举 go.work 三个 module（主 module + sdk/go +
// genproto）的包投影；rel 统一为仓库相对路径。契约测试自身不在执法面。
func listWorkspacePackages(t *testing.T, includeTestOnly bool) []listedPackage {
	t.Helper()
	var out []listedPackage
	for _, dir := range []string{"", "sdk/go", "genproto"} {
		for _, p := range listPackages(t, dir, includeTestOnly) {
			if p.rel == "internal/guards" || strings.HasPrefix(p.rel, "internal/guards/") {
				continue
			}
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// listPackages 在 dir 运行 go list ./...。go.work 下 sdk/go、genproto 是
// 独立 module，调用方按需逐 module 列举。
func listPackages(t *testing.T, dir string, includeTestOnly bool) []listedPackage {
	t.Helper()
	workDir := filepath.Join(repoRoot(t), filepath.FromSlash(dir))
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = workDir
	// 固定环境：不继承调用方的 GOFLAGS（CI 与本机同口径）。
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./... in %s: %v\n%s", dir, err, stderr.String())
	}
	modulePath := modulePathOf(t, workDir)
	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []listedPackage
	for {
		var raw struct {
			ImportPath   string
			Imports      []string
			TestImports  []string
			XTestImports []string
			GoFiles      []string
			TestGoFiles  []string
			XTestGoFiles []string
		}
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("go list decode in %s: %v", dir, err)
		}
		hasProd := len(raw.GoFiles) > 0
		hasTests := len(raw.TestGoFiles) > 0 || len(raw.XTestGoFiles) > 0
		if !hasProd && !(includeTestOnly && hasTests) {
			continue
		}
		rel := strings.TrimPrefix(raw.ImportPath, modulePath+"/")
		if rel == raw.ImportPath {
			rel = "" // module 根包
		}
		if dir != "" {
			rel = filepath.ToSlash(filepath.Join(dir, rel))
		}
		all := make([]string, 0, len(raw.Imports)+len(raw.TestImports)+len(raw.XTestImports))
		all = append(all, raw.Imports...)
		all = append(all, raw.TestImports...)
		all = append(all, raw.XTestImports...)
		pkgs = append(pkgs, listedPackage{rel: rel, imports: raw.Imports, allImports: all})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].rel < pkgs[j].rel })
	if len(pkgs) == 0 {
		t.Fatalf("go list in %s returned no packages — guard is blind", dir)
	}
	return pkgs
}

// modulePathOf 读 dir 所属 module 的 path。go.work 下 `go list -m` 会列出
// 全部 main module，故按 go.mod 目录精确匹配。
func modulePathOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m in %s: %v", dir, err)
	}
	wantGoMod, err := filepath.Abs(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("resolve go.mod for %s: %v", dir, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var mod struct {
			Path  string
			GoMod string
		}
		if err := dec.Decode(&mod); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("go list -m decode in %s: %v", dir, err)
		}
		if filepath.Clean(mod.GoMod) == filepath.Clean(wantGoMod) {
			return mod.Path
		}
	}
	t.Fatalf("go list -m in %s found no module for %s", dir, wantGoMod)
	return ""
}
