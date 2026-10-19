package guards

// 守卫 C（审计 §10.2 / 元类 M-2：覆盖不对称）：契约面覆盖反扫，两部分：
//
//	a) Runtime 端口覆盖：反射枚举 capability.Runtime（含嵌入 Provider 面）
//	   与在册子面（RuntimeLogs/RuntimeAdmin/RuntimeInspector/RuntimeRestart）
//	   的方法名 + 关键旗标形态（LogQuery.Follow 的 true/false），断言每个
//	   名字至少被一个测试文件**以 AST 形态**引用——方法声明（假/真 Provider
//	   实现）或选择器调用点（`x.Method(...)`）。R12-M2：旧判据是裸子串，
//	   Ensure/Watch 等通用英文动词在注释、字符串与任意标识符里都算命中，
//	   对核心方法零判别力；AST 判据只认代码结构，注释/文案不再记账。
//	b) Delete RPC 覆盖：解析 proto 枚举全部 Delete* RPC，断言 apitest
//	   测试源码存在对应覆盖引用（活跃下级拒绝或显式删除/级联断言的
//	   载体文件）。
//
// 两部分共用豁免表模式：缺口必须登记豁免并带理由；豁免不再命中（覆盖
// 已补上或 RPC 已消失）即红——双向保鲜。
//
// 扫描口径：a 扫 engine/swarm/k3s/apitest 四棵测试树（Runtime 端口的全部
// 消费面——k3s 是第二运行时 Provider，与 swarm 同权入扫，P5-3 补）；
// b 只扫 apitest（RPC 契约面的验收层）。
//
// 防盲：a 的 AST 语料有文件数/方法声明数/调用点数三重下限，并把两个
// 核心方法（Ensure/Watch）的**实现声明**钉成正向锚——语料解析退化或
// 假底座整体消失时守卫自己先红，而不是静默全绿。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// runtimeTestTrees 是 Runtime 端口覆盖的扫描树（假底座、真 Provider、
// 服务面三处；P5-3 起第二运行时 Provider k3s 与 swarm 同权入扫）。
var runtimeTestTrees = []string{
	filepath.Join("internal", "application", "engine"),
	filepath.Join("internal", "infrastructure", "providers", "swarm"),
	filepath.Join("internal", "infrastructure", "providers", "k3s"),
	filepath.Join("internal", "testsupport", "apitest"),
}

// apitestTree 是 RPC 契约覆盖的扫描树。
var apitestTree = filepath.Join("internal", "testsupport", "apitest")

// testCorpus 拼接一棵树下全部 *_test.go 的源码文本（CRLF 归一）。保留给
// Delete RPC 覆盖的文本引用判据（RPC 名字是生成契约面唯一符号，子串误报
// 面小；Runtime 面已迁 AST）。
func testCorpus(t *testing.T, tree string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(filepath.Join(repoRoot(t), tree), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		sb.WriteString(readFileLF(t, relPath(t, path)))
		sb.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// runtimeASTCorpus 是 Runtime 端口覆盖的 AST 语料（R12-M2）：
// methods 数方法声明（任意接收者的 `func (x *T) Name(...)`——假/真 Provider
// 实现面），calls 数选择器调用点（`x.Name(...)`——消费面）。两者都不含
// 注释与字符串，裸子串命中面（"Ensure appears somewhere"）被彻底移除。
type runtimeASTCorpus struct {
	files   int
	methods map[string]int
	calls   map[string]int
}

// collectRuntimeAST 从单文件收集方法声明与选择器调用点（R12-M2 判据本体；
// 形状用例直接消费本函数）。
func collectRuntimeAST(file *ast.File) (methods, calls map[string]int) {
	methods = map[string]int{}
	calls = map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Recv != nil && x.Name != nil {
				methods[x.Name.Name]++
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				calls[sel.Sel.Name]++
			}
		}
		return true
	})
	return methods, calls
}

// scanRuntimeAST 解析四棵测试树的全部 *_test.go，收集方法声明与调用点。
func scanRuntimeAST(t *testing.T) runtimeASTCorpus {
	t.Helper()
	c := runtimeASTCorpus{methods: map[string]int{}, calls: map[string]int{}}
	for _, tree := range runtimeTestTrees {
		// apitest 树的非测试文件也入扫：harness.go 是假 Runtime 的**实现
		// 面**（Cordon/Drain/Uncordon 等方法声明只住在这里）——旧子串判据
		// 把 "drain"/"cordon" 文案当引用，AST 判据要么看见实现声明，要么
		// 误报缺口。engine/swarm/k3s 仍只扫 *_test.go（生产实现不算覆盖）。
		apitestNonTest := tree == apitestTree
		err := filepath.WalkDir(filepath.Join(repoRoot(t), tree), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			isTest := strings.HasSuffix(path, "_test.go")
			if !isTest && !(apitestNonTest && strings.HasSuffix(path, ".go")) {
				return nil
			}
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			c.files++
			methods, calls := collectRuntimeAST(file)
			for name, n := range methods {
				c.methods[name] += n
			}
			for name, n := range calls {
				c.calls[name] += n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("AST scan %s: %v", tree, err)
		}
	}
	return c
}

// portCoverageExemptions 是 Runtime 面/旗标缺口的豁免表（名字 → 理由）。
// 当前零条目：核心方法、在册子面、Follow 双形态均有 AST 引用。新方法缺测
// 时在此登记（如 "Uncordon": "drain/cordon 同路径已测，对称面随 N1 补"），
// 覆盖补上后条目不再命中会红——记得清走。
var portCoverageExemptions = map[string]string{}

// followForms 是 LogQuery.Follow 的关键旗标形态（形态名 → 匹配器）。
// struct 字面量经 gofmt 对齐会插入空白，用正则容忍。旗标不是方法名，
// 保留文本判据（true/false 出现在测试源码即覆盖面）。
var followForms = map[string]*regexp.Regexp{
	"LogQuery.Follow=true":  regexp.MustCompile(`Follow:\s*true\b`),
	"LogQuery.Follow=false": regexp.MustCompile(`Follow:\s*false\b`),
}

// TestRuntimePortCoverage：Runtime 方法×子面×旗标形态 ↔ 测试 AST 引用反扫。
func TestRuntimePortCoverage(t *testing.T) {
	corpus := scanRuntimeAST(t)
	// 防盲三重下限（实测在注释里写死的数字随语料增长只升不降；解析退化
	// 或扫描树搬走时这里先红）。
	if corpus.files < 80 {
		t.Fatalf("AST corpus parsed only %d test files — guard is blind, fix the scan", corpus.files)
	}
	if len(corpus.methods) < 60 {
		t.Fatalf("AST corpus found only %d distinct method declarations — guard is blind, fix the scan", len(corpus.methods))
	}
	totalCalls := 0
	for _, n := range corpus.calls {
		totalCalls += n
	}
	if totalCalls < 3000 {
		t.Fatalf("AST corpus found only %d selector calls — guard is blind, fix the scan", totalCalls)
	}
	// 正向锚：两个核心方法必须有至少两处实现声明（engine 假底座 + apitest
	// 假底座）。语料若只剩一棵树，这里红——比"名字出现过"更难失明。
	for _, name := range []string{"Ensure", "Watch"} {
		if corpus.methods[name] < 2 {
			t.Fatalf("positive anchor: %s has only %d method declaration(s) in the test trees (want >= 2 fake implementations) — guard is blind, fix the scan", name, corpus.methods[name])
		}
	}

	// 反射枚举面：Runtime（含嵌入 Provider 的 Describe/Health）+ 在册子面。
	// RuntimeRestart 自 R06-L1 起是可发现子面（ProviderFaces.Restart），
	// 一并纳入覆盖反扫。
	faces := []struct {
		name string
		typ  reflect.Type
	}{
		{"capability.Runtime", reflect.TypeOf((*capability.Runtime)(nil)).Elem()},
		{"capability.RuntimeLogs", reflect.TypeOf((*capability.RuntimeLogs)(nil)).Elem()},
		{"capability.RuntimeAdmin", reflect.TypeOf((*capability.RuntimeAdmin)(nil)).Elem()},
		{"capability.RuntimeInspector", reflect.TypeOf((*capability.RuntimeInspector)(nil)).Elem()},
		{"capability.RuntimeRestart", reflect.TypeOf((*capability.RuntimeRestart)(nil)).Elem()},
	}
	usedExemptions := map[string]bool{}
	var missing []string
	for _, face := range faces {
		for i := 0; i < face.typ.NumMethod(); i++ {
			name := face.typ.Method(i).Name
			if corpus.methods[name]+corpus.calls[name] > 0 {
				continue
			}
			key := face.name + "." + name
			if reason, ok := portCoverageExemptions[key]; ok && reason != "" {
				usedExemptions[key] = true
				continue
			}
			missing = append(missing, key)
		}
	}
	text := testCorpus(t, runtimeTestTrees[0])
	for _, tree := range runtimeTestTrees[1:] {
		text += testCorpus(t, tree)
	}
	for form, re := range followForms {
		if re.MatchString(text) {
			continue
		}
		if reason, ok := portCoverageExemptions[form]; ok && reason != "" {
			usedExemptions[form] = true
			continue
		}
		missing = append(missing, form)
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("runtime port face %s has no AST reference (method declaration or selector call) in engine/swarm/k3s/apitest tests — add a test or register an exemption with a reason (audit §10.2 guard C)", name)
	}
	for name, reason := range portCoverageExemptions {
		if reason == "" {
			t.Errorf("port coverage exemption %s must carry a reason", name)
		}
		if !usedExemptions[name] {
			t.Errorf("port coverage exemption %s no longer matches a real gap; remove the entry", name)
		}
	}
}

// TestRuntimePortCoverageASTDiscriminates 是 R12-M2 的形状用例：注释与
// 字符串里的方法名不算引用，方法声明/选择器调用才算——旧裸子串判据在这
// 两种形态上完全等价；本用例是"退回子串即红"的永久反验锚。
func TestRuntimePortCoverageASTDiscriminates(t *testing.T) {
	const src = `package fixture

// Ensure Watch Remove Drain Cordon Uncordon appear only in this comment.
var _ = "Ensure Watch Remove Drain Cordon Uncordon in a string literal"

type fake struct{}

func (f *fake) Ensure() {}
func call(f *fake)      { f.Watch() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	methods, calls := collectRuntimeAST(file)
	for _, name := range []string{"Remove", "Drain", "Cordon", "Uncordon"} {
		if methods[name] != 0 || calls[name] != 0 {
			t.Errorf("%s appears only in a comment/string but the AST corpus counted it (methods=%d calls=%d) — the guard regressed to a bare substring criterion", name, methods[name], calls[name])
		}
	}
	if methods["Ensure"] != 1 {
		t.Errorf("method declaration Ensure must be counted once, got %d", methods["Ensure"])
	}
	if calls["Watch"] != 1 {
		t.Errorf("selector call f.Watch() must be counted once, got %d", calls["Watch"])
	}
}

// deleteRPCRe 匹配 proto 服务方法声明中的 Delete* RPC。
var deleteRPCRe = regexp.MustCompile(`rpc\s+(Delete\w+)\s*\(`)

// deleteRPCs 解析 proto/ 枚举全部 Delete 开头 RPC 名。
func deleteRPCs(t *testing.T) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(filepath.Join(repoRoot(t), "proto"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		for _, m := range deleteRPCRe.FindAllStringSubmatch(readFileLF(t, relPath(t, path)), -1) {
			names = append(names, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("proto scan found no Delete* RPCs — guard is blind, fix the scan")
	}
	sort.Strings(names)
	return unique(names)
}

// deleteCoverageExemptions 是 Delete RPC 缺口的豁免表（RPC → 理由）。
// 当前零条目：全部 Delete RPC 在 apitest 有覆盖（结构面 DeleteApp/
// DeleteProject 在 delete_app_test.go；DeleteUser/DeleteTeam/DeleteRole/
// DeleteSecret/DeleteRoute 在 delete_coverage_test.go）。新增 Delete RPC
// 无对应用例时在此登记豁免并带理由；用例补上后条目不再命中会红。
var deleteCoverageExemptions = map[string]string{}

// TestDeleteRPCCoverage：proto 全部 Delete* RPC ↔ apitest 覆盖反扫。
func TestDeleteRPCCoverage(t *testing.T) {
	corpus := testCorpus(t, apitestTree)
	if len(corpus) < 1024 {
		t.Fatal("apitest corpus suspiciously small — guard is blind, fix the scan")
	}
	usedExemptions := map[string]bool{}
	var missing []string
	for _, rpc := range deleteRPCs(t) {
		if strings.Contains(corpus, rpc) {
			continue
		}
		if reason, ok := deleteCoverageExemptions[rpc]; ok && reason != "" {
			usedExemptions[rpc] = true
			continue
		}
		missing = append(missing, rpc)
	}
	for _, rpc := range missing {
		t.Errorf("Delete RPC %s has no coverage reference in internal/testsupport/apitest tests — add a case (active-subordinate rejection or explicit delete/cascade assertion) or register an exemption with a reason (audit §10.2 guard C)", rpc)
	}
	for rpc, reason := range deleteCoverageExemptions {
		if reason == "" {
			t.Errorf("delete coverage exemption %s must carry a reason", rpc)
		}
		if !usedExemptions[rpc] {
			t.Errorf("delete coverage exemption %s no longer matches a real gap; remove the entry", rpc)
		}
	}
}
