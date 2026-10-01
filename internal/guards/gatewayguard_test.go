package guards

// 守卫 A（审计 §10.2 / 元类 M-1：REST 契约漂移）：proto 注解面 ↔ assembly
// 挂载清单一一对应。注解面以 genproto 生成物为单源（*.pb.gw.go 每个带
// HTTP 面的服务各有一个 Register*HandlerClient 函数），无需 proto parser；
// 挂载面扫 internal/assembly 的 Register*HandlerClient 引用（gateway.go
// 的 gatewayRegistrations 清单）。两侧都用 go/parser 提取真实代码符号
// （AST 级，注释免疫——把注册行注释掉必须在守卫红）。"注解加了、忘挂载"
// （P1-13/A-9：identity 六服务曾整面 404）在此红并列缺口服务名；反向死
// 条目（注解面已不存在的挂载）同样红。豁免表带理由、双向保鲜（豁免不再
// 命中即红）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// annotationSurface 枚举 genproto 下全部带 HTTP 注解面的服务名
// （*.pb.gw.go 的 Register*HandlerClient 函数声明，生成物单源）。
func annotationSurface(t *testing.T) map[string]bool {
	t.Helper()
	services := map[string]bool{}
	err := walkGoDecls(t, filepath.Join("genproto"), ".pb.gw.go", false, func(name string) {
		if svc, ok := strings.CutPrefix(name, "Register"); ok {
			if svc, ok := strings.CutSuffix(svc, "HandlerClient"); ok {
				services[svc] = true
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatal("annotation surface scan found no *.pb.gw.go handlers — guard is blind, fix the scan")
	}
	return services
}

// mountedSurface 枚举 internal/assembly 非生成物、非测试 .go 里引用的
// Register*HandlerClient 服务名（gatewayRegistrations 挂载清单的成员）。
func mountedSurface(t *testing.T) map[string]bool {
	t.Helper()
	services := map[string]bool{}
	err := walkGoDecls(t, filepath.Join("internal", "assembly"), ".go", true, func(name string) {
		if svc, ok := strings.CutPrefix(name, "Register"); ok {
			if svc, ok := strings.CutSuffix(svc, "HandlerClient"); ok {
				services[svc] = true
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatal("mount surface scan found no Register*HandlerClient references — guard is blind, fix the scan")
	}
	return services
}

// walkGoDecls 解析 root 子树（仓库相对）下后缀匹配的 .go 文件，回调每个
// 函数声明名与选择子表达式名（挂载清单成员是函数值引用，不是调用）。
// skipGenerated 排除生成物：assembly 侧排除 wire_gen.go；genproto 侧本就
// 扫生成物（*.pb.gw.go 是注解面的单源），不排。
func walkGoDecls(t *testing.T, root, suffix string, skipGenerated bool, fn func(name string)) error {
	t.Helper()
	absRoot := filepath.Join(repoRoot(t), root)
	return filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		rel := relPath(t, path)
		if !strings.HasSuffix(rel, ".go") || (skipGenerated && generatedFile(rel)) || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		astFile, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, decl := range astFile.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				fn(fd.Name.Name)
			}
		}
		ast.Inspect(astFile, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				fn(sel.Sel.Name)
			}
			return true
		})
		return nil
	})
}

// mountExemptions 是注解面 → 挂载面差异的豁免表（服务名 → 理由）。
// 只豁免"注解面存在但有意不经 Register 清单挂载"的方向；当前唯一例外
// 形态是 ReceiveWebhook（无 HTTP 注解、gateway_hooks.go 原生挂法）——
// 它不产生 HandlerClient 生成物，无需豁免。豁免条目不再命中（差异消失）
// 即红。
var mountExemptions = map[string]string{}

// relPath 返回仓库相对斜杠路径。
func relPath(t *testing.T, path string) string {
	t.Helper()
	rel, err := filepath.Rel(repoRoot(t), path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// TestGatewayMountsAnnotatedSurface：注解面与挂载面双向对账。
func TestGatewayMountsAnnotatedSurface(t *testing.T) {
	annotated := annotationSurface(t)
	mounted := mountedSurface(t)
	usedExemptions := map[string]bool{}

	var missing []string
	for svc := range annotated {
		if mounted[svc] {
			continue
		}
		if reason, ok := mountExemptions[svc]; ok && reason != "" {
			usedExemptions[svc] = true
			continue
		}
		missing = append(missing, svc)
	}
	sort.Strings(missing)
	for _, svc := range missing {
		t.Errorf("service %s has an HTTP annotation surface (Register%sHandlerClient in genproto) but is not mounted by internal/assembly — add it to gatewayRegistrations or exempt with a reason (audit §10.2 guard A / P1-13)", svc, svc)
	}

	// 反向：挂载面出现注解面不存在的服务 = 死条目（生成物已消失）。
	var dead []string
	for svc := range mounted {
		if annotated[svc] {
			continue
		}
		if reason, ok := mountExemptions[svc]; ok && reason != "" {
			usedExemptions[svc] = true
			continue
		}
		dead = append(dead, svc)
	}
	sort.Strings(dead)
	for _, svc := range dead {
		t.Errorf("internal/assembly registers %s but no genproto annotation surface exists for it — remove the stale mount or exempt with a reason", svc)
	}

	// 双向保鲜：豁免不再命中（差异消失）即红，清走死条目。
	for svc, reason := range mountExemptions {
		if reason == "" {
			t.Errorf("mount exemption %s must carry a reason", svc)
		}
		if !usedExemptions[svc] {
			t.Errorf("mount exemption %s no longer matches a real gap; remove the entry (annotated=%v mounted=%v)", svc, annotated[svc], mounted[svc])
		}
	}

	// 防呆：双向非空，防 WalkDir 根路径失配后空集静默通过。
	if len(annotated) < 10 || len(mounted) < 10 {
		t.Fatalf("surfaces suspiciously small: annotated=%d mounted=%d — guard is blind, fix the scan", len(annotated), len(mounted))
	}
}
