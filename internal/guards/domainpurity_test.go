package guards

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// P1a-3b 守卫换装：domain 三红线（架构收敛 P1——domain 是纯领域层，不碰
// 系统副作用、API 契约生成物与 RPC/网络框架）。执法面：internal/domain/**
// 生产 .go（测试文件豁免——测试天然要碰文件与网络夹具；material 纯密码学
// 面如有正当 os 需求走 purityExemptions 豁免表带理由）。
//
// 三红线与豁免机制对齐 importguard 词汇：豁免条目带理由，不再命中即红
// （双向保鲜——spec 的 genproto 豁免若被移除依赖，条目本身过期也红）。
//
// 第四执法面（ADR-0087 决策 2 纯度判据的时间项，P3a 批级守卫批
// 2026-11-01）：internal/domain 非测试代码禁 time.Now() 直呼——时间一律
// 作参数传入（「零 clock 依赖」）。AST 调用级判定，只认 X 为标识符
// "time"、方法名 "Now" 的调用：time.Since/time.Parse/Duration 运算天然
// 不在禁列；测试文件不在执法面。已知局限：本地变量遮蔽 time 包名的形态
// 不做类型解析（当前 domain 全域零命中，遮蔽不存在）。

const domainPuritySubtree = "internal/domain/"

// 红线①（系统副作用面）的禁列是跨守卫共享表（R12-L1）：os/os.*、
// io/ioutil、net/net.*、syscall；net/url 走显式白名单。单源住
// purityredline_test.go 的 puritySystemImportMatchers——本守卫与
// capabilitypurity 引用同一表，禁止各自维护一份。

// genprotoImportPrefix 红线②（契约生成物面）：fleetly genproto module。
const genprotoImportPrefix = "github.com/fleetlyrun/fleetly/genproto/"

// frameworkImportMatchers 红线③（RPC/网络框架面）：grpc-go 全家与 x/net。
// protobuf runtime（google.golang.org/protobuf）不在禁列——spec 的 IR 类型
// 系统合法依赖 proto 消息操纵（proto/protojson 是纯序列化库非传输框架）。
var frameworkImportMatchers = []string{
	"google.golang.org/grpc",
	"golang.org/x/net",
}

// purityExemptions 是三红线豁免表：子树 → 放行的 import 前缀 + 理由。
// 条目不再被任何实际 import 命中即红（理由消化后删行）。
type purityExemption struct {
	subtree string // 豁免生效的子树前缀（internal/domain/ 下）
	allow   string // 放行的 import 前缀
	reason  string // 豁免理由
}

var domainPurityExemptions = []purityExemption{
	{
		subtree: "internal/domain/shared/spec/",
		allow:   genprotoImportPrefix + "fleetly/spec/v1",
		reason:  "spec 是 IR 校验/归一化包装层：原始生成类型住 genproto module（tech-stack §2，叶子纯度由 importguard 钉死只 import genproto 与标准库）——仅放行 spec/v1 自身类型，其他 genproto 面仍禁",
	},
}

// domainProductionFiles 收集 internal/domain/** 生产 .go（复用 scanGoFiles
// 的 AST 解析与生成物排除；防盲：扫零文件即红）。
func domainProductionFiles(t *testing.T) []goFile {
	t.Helper()
	var out []goFile
	for _, f := range scanGoFiles(t) {
		if !strings.HasPrefix(f.rel, domainPuritySubtree) || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatalf("domain purity scan found no production files under %s — guard is blind, fix the scan", domainPuritySubtree)
	}
	return out
}

// TestDomainPurity 三红线执法：每个生产 import 依次过①②③匹配器，命中且
// 无豁免即红；豁免条目命中计数为零即红（双向保鲜）。
func TestDomainPurity(t *testing.T) {
	exemptionHits := make(map[int]int, len(domainPurityExemptions))
	for _, f := range domainProductionFiles(t) {
		for _, imp := range f.imports {
			line := domainPurityLine(imp)
			if line == "" {
				continue
			}
			if ex, ok := purityExemptionFor(f.rel, imp); ok {
				for i, e := range domainPurityExemptions {
					if e.subtree == ex.subtree && e.allow == ex.allow {
						exemptionHits[i]++
					}
				}
				continue
			}
			t.Errorf("%s imports %q — domain red line %s (domain 是纯领域层；正当需求走 domainPurityExemptions 带理由登记)", f.rel, imp, line)
		}
	}
	for i, e := range domainPurityExemptions {
		if exemptionHits[i] == 0 {
			t.Errorf("domain purity exemption %s -> %s matches no import — reason consumed, delete the row (双向保鲜)", e.subtree, e.allow)
		}
	}
}

// domainPurityLine 报告 import 命中的红线（""=不命中）。①②③分别对应
// 系统/生成物/框架面，报错信息带红线名便于定位。
func domainPurityLine(imp string) string {
	if matchesPuritySystemImport(imp) {
		return "①系统副作用（os/os.*、io/ioutil、net/net.*、syscall；net/url 白名单）"
	}
	if strings.HasPrefix(imp, genprotoImportPrefix) {
		return "②契约生成物（genproto/**）"
	}
	if matchesImport(imp, frameworkImportMatchers) {
		return "③RPC/网络框架（grpc、x/net）"
	}
	return ""
}

// purityExemptionFor 查 f.rel 子树对 imp 的豁免（子树前缀 + import 前缀
// 双匹配）。
func purityExemptionFor(fileRel, imp string) (purityExemption, bool) {
	for _, e := range domainPurityExemptions {
		if strings.HasPrefix(fileRel, e.subtree) && strings.HasPrefix(imp, e.allow) {
			return e, true
		}
	}
	return purityExemption{}, false
}

// matchesImport 报告 import 路径是否命中匹配器（精确匹配或目录前缀：
// "os" 匹配 "os" 与 "os/exec"，不匹配 "ossgn"）。
func matchesImport(imp string, matchers []string) bool {
	for _, m := range matchers {
		if imp == m {
			return true
		}
		if strings.HasSuffix(m, "/") && strings.HasPrefix(imp, m) {
			return true
		}
	}
	return false
}

// domainTimeNowExemptions 是 time.Now 调用禁列的豁免面（键 = 仓库相对
// "路径:行" → 理由）。条目不再命中（调用点改参数化/挪行）即红——行号
// 键故意脆：挪动即重新裁决。预检 2026-11-01：internal/domain 非测试代码
// 零命中，**零豁免直接立法**（ADR-0087 决策 2）。
var domainTimeNowExemptions = map[string]string{}

// scanGoTimeNowCalls 解析 root 子树全部非测试 .go（跳过 testdata/），收集
// time.Now() 调用位（仓库相对 "路径:行"）。只认 CallExpr 的 Fun 为
// SelectorExpr{X: Ident("time"), Sel: "Now"}——time.Since/time.Parse/
// Duration 运算不命中（按构造排除，非前缀豁免）。
func scanGoTimeNowCalls(t *testing.T, root string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var hits []string
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		files++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "time" || sel.Sel.Name != "Now" {
				return true
			}
			rel, rerr := filepath.Rel(repoRoot(t), path)
			if rerr != nil {
				return true
			}
			hits = append(hits, filepath.ToSlash(rel)+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防盲：0 文件即 fatal（根路径错了不得静默零命中通过）。
	if files == 0 {
		t.Fatalf("time.Now scan found no production .go under %s — guard is blind, fix the scan", root)
	}
	sort.Strings(hits)
	return hits
}

// TestDomainPurityNoTimeNowCalls：第四执法面（ADR-0087 决策 2 纯度判据的
// 时间项）——internal/domain 非测试代码禁 time.Now() 直呼；时间一律作
// 参数传入。豁免面双向保鲜；防盲锚 = testdata/domaintimenow 合成样本
// （违规模板必须命中、time.Since 合法面必须不命中——扫描器失明或过宽
// 都红）。
func TestDomainPurityNoTimeNowCalls(t *testing.T) {
	hits := scanGoTimeNowCalls(t, filepath.Join(repoRoot(t), "internal", "domain"))
	used := map[string]bool{}
	for _, h := range hits {
		reason, ok := domainTimeNowExemptions[h]
		if !ok || reason == "" {
			t.Errorf("%s calls time.Now() — domain purity requires time to arrive as a parameter (ADR-0087 decision 2); take a `now time.Time` argument and let the application layer own the clock, or register domainTimeNowExemptions with a reason", h)
			continue
		}
		used[h] = true
	}
	for k, reason := range domainTimeNowExemptions {
		if reason == "" {
			t.Errorf("time.Now exemption %s must carry a reason", k)
		}
		if !used[k] {
			t.Errorf("time.Now exemption %s no longer matches a real call site — remove the entry (双向保鲜)", k)
		}
	}

	// 防盲锚：合成样本过同一扫描器——恰好一处命中，且行内容真是
	// time.Now（报告位真实）；time.Since 样本不命中（判据有分辨力）。
	anchor := scanGoTimeNowCalls(t, filepath.Join(repoRoot(t), "internal", "guards", "testdata", "domaintimenow"))
	if len(anchor) != 1 {
		t.Fatalf("time.Now anchor fixture must yield exactly 1 hit, found %d (%v) — fix the fixture or the scanner", len(anchor), anchor)
	}
	_, line, ok := strings.Cut(anchor[0], ":")
	if !ok {
		t.Fatalf("malformed anchor hit %q", anchor[0])
	}
	idx, aerr := strconv.Atoi(line)
	lines := strings.Split(readFileLF(t, "internal/guards/testdata/domaintimenow/anchor.go"), "\n")
	if aerr != nil || idx < 1 || idx > len(lines) || !strings.Contains(lines[idx-1], "time.Now()") {
		t.Errorf("time.Now anchor hit %q does not point at a real time.Now() call — fix the scanner", anchor[0])
	}
}
