package guards

// 模块工厂直转律（装配面契约测试，2026-11-23）：组合根与测试夹具里的应用
// 模块工厂只准结构体直转 `x.New(x.Deps(p))`，不准手抄字段清单。
//
// 为什么需要：端口袋（contract.XPorts）与模块 Deps 逐字段同形，各模块包内
// 已有编译期互转断言——同形是编译器保证的事实，手抄只是把它再抄一遍，而
// 抄错编译器不管：函数类型字段 nil 能编译，漏转发即静默哑端口。实测至少
// 五次（P2-1b ManagedFactory 漏 ActivatorAliasSource/ActivatorPortSource 两
// 处、DiagnosisFactory 漏 ComponentsHealthSnapshot〔assembly 一处 + engine
// 白盒与 apitest 夹具各一处〕）。直转后「漏转发」在编译期不存在。
//
// 扫描面=装配面三处：internal/assembly（生产组合根）、
// internal/testsupport/apitest（进程内夹具）、engine 包白盒测试（测试组合
// 根）。模块包自己的测试不在射程——它们故意构造部分 Deps 验证降级行为。
// engine.New(engine.Deps{...}) 不在射程：内核自身的 Deps 不是模块端口袋。
//
// 防盲：直转工厂计数下限贴实测值——扫描面挪走/改写法而静默零命中即红。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// moduleFactoryLiteralRe 匹配 `x.New(y.Deps{`（RE2 无反向引用——两侧包名
// 是否同一个由调用方比较）。
var moduleFactoryLiteralRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.New\(([A-Za-z_][A-Za-z0-9_]*)\.Deps\{`)

// moduleFactoryDirectRe 匹配 `x.New(y.Deps(expr))`（同上，包名等值另判）。
var moduleFactoryDirectRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.New\(([A-Za-z_][A-Za-z0-9_]*)\.Deps\([^)]*\)\)`)

// countDirectModuleFactories 返回 src 里的直转工厂数（x.New(x.Deps(...))）。
func countDirectModuleFactories(src string) int {
	n := 0
	for _, m := range moduleFactoryDirectRe.FindAllStringSubmatch(src, -1) {
		if m[1] == m[2] {
			n++
		}
	}
	return n
}

// moduleFactoryScanDirs 是装配面扫描目录（仓库相对路径 → 是否只扫测试文件）。
var moduleFactoryScanDirs = []struct {
	rel      string
	testOnly bool
}{
	{rel: "internal/assembly"},
	{rel: "internal/testsupport/apitest"},
	{rel: "internal/application/engine", testOnly: true},
}

// moduleFactoryDirectBaseline 是装配面直转工厂的实测计数（双向保鲜：模块
// 增删即同批改值）。
const moduleFactoryDirectBaseline = 31

// handCopiedModuleFactories 返回 src 里的手抄工厂命中（包名:行号）。
func handCopiedModuleFactories(src string) []string {
	var hits []string
	for i, line := range strings.Split(src, "\n") {
		// engine.New(engine.Deps{...}) 是内核自身的 Deps 装配，不是模块工厂。
		if m := moduleFactoryLiteralRe.FindStringSubmatch(line); m != nil && m[1] == m[2] && m[1] != "engine" {
			hits = append(hits, m[1]+":"+strconv.Itoa(i+1))
		}
	}
	return hits
}

func TestModuleFactoriesAreDirectTransfers(t *testing.T) {
	root := repoRoot(t)
	direct := 0
	for _, dir := range moduleFactoryScanDirs {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir.rel)), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if dir.testOnly != strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, path)
			for _, hit := range handCopiedModuleFactories(string(data)) {
				t.Errorf("%s: %s hand-copies a module Deps literal — compose module factories by direct struct transfer x.New(x.Deps(p)) (the port bag and Deps are field-identical by compile-time assert; a hand-copied field list silently drops ports)", filepath.ToSlash(rel), hit)
			}
			direct += countDirectModuleFactories(string(data))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 防盲计数贴实测（assembly 11 + apitest 10 + engine 白盒 10）。
	if direct != moduleFactoryDirectBaseline {
		t.Errorf("direct-transfer module factories on the composition surfaces: got %d, baseline %d — update moduleFactoryDirectBaseline in the same commit (a module was added/removed) or the scan stopped seeing the factories", direct, moduleFactoryDirectBaseline)
	}
}

// TestModuleFactoryDetectorCatchesHandCopiedLiteral：检测器红灯自证——
// 手抄形态命中，直转与内核自身 Deps 不命中。
func TestModuleFactoryDetectorCatchesHandCopiedLiteral(t *testing.T) {
	for name, src := range map[string]string{
		"multi-line literal": "return manageds.New(manageds.Deps{\n\tProxy: p.Proxy,\n})",
		"one-line literal":   "return execs.New(execs.Deps{Log: p.Log})",
	} {
		if hits := handCopiedModuleFactories(src); len(hits) != 1 {
			t.Errorf("%s: a hand-copied literal must be caught exactly once, got %v", name, hits)
		}
	}
	for name, src := range map[string]string{
		"direct transfer": "return manageds.New(manageds.Deps(p))",
		"kernel own Deps": "return engine.New(engine.Deps{DB: db})",
		"other bag type":  "return fleetlygrpc.Deps{Core: core}",
	} {
		if hits := handCopiedModuleFactories(src); len(hits) != 0 {
			t.Errorf("%s: unexpected hits %v", name, hits)
		}
	}
}
