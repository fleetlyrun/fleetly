// Package guards 承载结构守卫测试（架构由测试承载，架构文档 §11）：
// 编排器 SDK 圈禁、model/spec 叶子纯度、providers 隔离、词汇禁词扫描。
// 扫描 idiom：runtime.Caller 定位仓库根 → WalkDir 枚举 → CRLF 归一 →
// 命中对照白名单（条目带理由；不再命中即红——双向保鲜）。
package guards

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot 返回仓库根（internal/guards/scan_test.go 上溯两层）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// generatedFile 报告 rel 是否生成物（buf/protoc/wire）。import 守卫不扫
// 生成物：import 结构由 buf/wire 生成模板决定，改它必须走再生成而不是改
// 依赖（手改生成物本身就是错，AGENTS.md 生成物纪律）。词汇扫描（
// wording_test.go）裁决相反——生成文本随仓发布、面向 SDK/网关消费方，是
// 全仓文本面的一部分，禁词契约不豁免生成物（批 D 裁决，2026-10-03）。
func generatedFile(rel string) bool {
	return strings.HasSuffix(rel, ".pb.go") ||
		strings.HasSuffix(rel, ".pb.gw.go") ||
		strings.HasSuffix(rel, "wire_gen.go") ||
		strings.HasSuffix(rel, ".swagger.json")
}

// goFile 是一个被扫描的 Go 文件。
type goFile struct {
	rel     string // 仓库相对路径（斜杠分隔）
	imports []string
}

// scanModulePrefixes 是守卫执法面的 module/目录前缀集（批 D 扩面：主
// module 之外纳入 sdk/go 与 genproto 两 module——SDK 与契约生成 module
// 同在守卫契约面上）。
var scanModulePrefixes = []string{
	"internal/",
	"cmd/",
	"sdk/go/",
	"genproto/",
}

// scanGoModulePath 报告 rel（斜杠分隔）是否落 Go 执法面（scanGoFiles 与
// 词汇扫描共用的前缀裁决；词汇扫描另加 proto/skills/install.sh 面）。
func scanGoModulePath(rel string) bool {
	for _, p := range scanModulePrefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// scanGoFiles 枚举主 module（internal/、cmd/）与 sdk/go、genproto 两
// module 的全部 .go（含测试；排除生成物与守卫自身），用 go/parser 提取
// import 集（比文本扫描精确：无视注释与 build tag 干扰）。批 D 扩面：
// sdk/go（手写客户端）与 genproto 此前在执法面外——SDK 模块的依赖方向
// 同受架构 §2 约束（编排器 SDK 圈禁、providers 只准 cmd 消费）。genproto
// 现状全是生成物（generatedFile 排除），前缀在册是让该 module 将来出现
// 的任何手写文件即落执法面。
func scanGoFiles(t *testing.T) []goFile {
	t.Helper()
	root := repoRoot(t)
	var files []goFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		if !scanGoModulePath(rel) {
			return nil
		}
		if generatedFile(rel) || strings.HasPrefix(rel, "internal/guards/") {
			return nil
		}
		fset := token.NewFileSet()
		astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("parse %s: %v", rel, parseErr)
			return nil
		}
		f := goFile{rel: rel}
		for _, imp := range astFile.Imports {
			f.imports = append(f.imports, strings.Trim(imp.Path.Value, `"`))
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("scan found no Go files — guard is blind, fix the scan")
	}
	return files
}

// readFileLF 读文件并归一 CRLF（Windows 检出漂移教训）。
func readFileLF(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}
