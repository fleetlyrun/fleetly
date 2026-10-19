package guards

import (
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
