package guards

// 守卫 C（审计 §10.2 / 元类 M-2：覆盖不对称）：契约面覆盖反扫，两部分：
//
//	a) Runtime 端口覆盖：反射枚举 capability.Runtime（含嵌入 Provider 面）
//	   与三个在册子面（RuntimeLogs/RuntimeAdmin/RuntimeInspector）的方法名
//	   + 关键旗标形态（LogQuery.Follow 的 true/false），断言每个名字至少被
//	   一个测试文件引用（文本级——fake 实现子面也是引用面，新方法落地时
//	   必然在此露出或登记豁免）。
//	b) Delete RPC 覆盖：解析 proto 枚举全部 Delete* RPC，断言 apitest
//	   测试源码存在对应覆盖引用（活跃下级拒绝或显式删除/级联断言的
//	   载体文件）。
//
// 两部分共用豁免表模式：缺口必须登记豁免并带理由；豁免不再命中（覆盖
// 已补上或 RPC 已消失）即红——双向保鲜。
//
// 扫描口径：a 扫 engine/swarm/apitest 三棵测试树（Runtime 端口的全部
// 消费面）；b 只扫 apitest（RPC 契约面的验收层）。

import (
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// runtimeTestTrees 是 Runtime 端口覆盖的扫描树（假底座、真 Provider、
// 服务面三处）。
var runtimeTestTrees = []string{
	filepath.Join("internal", "engine"),
	filepath.Join("internal", "providers", "swarm"),
	filepath.Join("internal", "apitest"),
}

// apitestTree 是 RPC 契约覆盖的扫描树。
var apitestTree = filepath.Join("internal", "apitest")

// testCorpus 拼接一棵树下全部 *_test.go 的源码文本（CRLF 归一）。
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

// portCoverageExemptions 是 Runtime 面/旗标缺口的豁免表（名字 → 理由）。
// 当前零条目：核心方法、三子面、Follow 双形态均有引用。新方法缺测时
// 在此登记（如 "Uncordon": "drain/cordon 同路径已测，对称面随 N1 补"），
// 覆盖补上后条目不再命中会红——记得清走。
var portCoverageExemptions = map[string]string{}

// followForms 是 LogQuery.Follow 的关键旗标形态（形态名 → 匹配器）。
// struct 字面量经 gofmt 对齐会插入空白，用正则容忍。
var followForms = map[string]*regexp.Regexp{
	"LogQuery.Follow=true":  regexp.MustCompile(`Follow:\s*true\b`),
	"LogQuery.Follow=false": regexp.MustCompile(`Follow:\s*false\b`),
}

// TestRuntimePortCoverage：Runtime 方法×子面×旗标形态 ↔ 测试引用反扫。
func TestRuntimePortCoverage(t *testing.T) {
	corpus := testCorpus(t, runtimeTestTrees[0])
	for _, tree := range runtimeTestTrees[1:] {
		corpus += testCorpus(t, tree)
	}
	if len(corpus) < 4096 {
		t.Fatal("test corpus suspiciously small — guard is blind, fix the scan")
	}

	// 反射枚举面：Runtime（含嵌入 Provider 的 Describe/Health）+ 三子面。
	faces := []struct {
		name string
		typ  reflect.Type
	}{
		{"capability.Runtime", reflect.TypeOf((*capability.Runtime)(nil)).Elem()},
		{"capability.RuntimeLogs", reflect.TypeOf((*capability.RuntimeLogs)(nil)).Elem()},
		{"capability.RuntimeAdmin", reflect.TypeOf((*capability.RuntimeAdmin)(nil)).Elem()},
		{"capability.RuntimeInspector", reflect.TypeOf((*capability.RuntimeInspector)(nil)).Elem()},
	}
	usedExemptions := map[string]bool{}
	var missing []string
	for _, face := range faces {
		for i := 0; i < face.typ.NumMethod(); i++ {
			name := face.name + "." + face.typ.Method(i).Name
			if strings.Contains(corpus, face.typ.Method(i).Name) {
				continue
			}
			if reason, ok := portCoverageExemptions[name]; ok && reason != "" {
				usedExemptions[name] = true
				continue
			}
			missing = append(missing, name)
		}
	}
	for form, re := range followForms {
		if re.MatchString(corpus) {
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
		t.Errorf("runtime port face %s has no reference in engine/swarm/apitest tests — add a test or register an exemption with a reason (audit §10.2 guard C)", name)
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
		t.Errorf("Delete RPC %s has no coverage reference in internal/apitest tests — add a case (active-subordinate rejection or explicit delete/cascade assertion) or register an exemption with a reason (audit §10.2 guard C)", rpc)
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
