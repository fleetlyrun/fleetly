package guards

import (
	"strings"
	"testing"
)

// P1a-3b 守卫换装：capability 纯度（internal/domain/shared/capability 是
// provider 契约面——纯类型 + 纯函数纪律，架构 §2 叶子）。执法面：该子树的
// 生产 .go（测试文件豁免）。禁四类 import：
//
//	os / os.* —— 进程与文件系统副作用（密钥落盘等属 infrastructure/keyring）；
//	net / net.*（net/url 除外，纯字符串解析）—— socket/HTTP 面；
//	google.golang.org/grpc* —— RPC 框架（进程外插件协议是 ADR-0003 明确
//	  不做的未来项，届时再议）；
//	genproto/** —— API 契约生成物（契约类型走 domain/shared/spec 或 proto
//	  之外的叶子自有类型）。
//
// 豁免表机制备用（capabilityPurityExemptions）：正当例外带理由登记，不再
// 命中即红（双向保鲜）。

const capabilityPuritySubtree = "internal/domain/shared/capability/"

// capabilityDenyMatchers 是 capability 子树生产码的 import 禁列。
var capabilityDenyMatchers = []string{
	"os",
	"os/",
	"net",
	"net/",
	"google.golang.org/grpc",
	genprotoImportPrefix,
}

// capabilityPurityExemptions 豁免表（当前为空——P1a-3a 拆 keyring 后
// capability 已纯；新例外须带理由，理由消化后删行即红）。
var capabilityPurityExemptions = []purityExemption{}

// TestCapabilityPurity：capability 生产码禁 os/net/grpc/genproto import。
func TestCapabilityPurity(t *testing.T) {
	var files int
	for _, f := range scanGoFiles(t) {
		if !strings.HasPrefix(f.rel, capabilityPuritySubtree) || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		files++
		for _, imp := range f.imports {
			if imp == "net/url" {
				continue // 纯解析（URL 拆解/拼装），零副作用
			}
			if !matchesImport(imp, capabilityDenyMatchers) {
				continue
			}
			if _, ok := capabilityExemptionFor(f.rel, imp); ok {
				continue
			}
			t.Errorf("%s imports %q — capability is pure types + pure functions (纯类型+纯函数纪律；正当例外走 capabilityPurityExemptions 带理由)", f.rel, imp)
		}
	}
	if files == 0 {
		t.Fatalf("capability purity scan found no production files under %s — guard is blind, fix the scan", capabilityPuritySubtree)
	}
	// 豁免保鲜：条目零命中即红。
	for _, e := range capabilityPurityExemptions {
		hit := false
		for _, f := range scanGoFiles(t) {
			if !strings.HasPrefix(f.rel, e.subtree) || strings.HasSuffix(f.rel, "_test.go") {
				continue
			}
			for _, imp := range f.imports {
				if strings.HasPrefix(imp, e.allow) {
					hit = true
				}
			}
		}
		if !hit {
			t.Errorf("capability purity exemption %s -> %s matches no import — reason consumed, delete the row (双向保鲜)", e.subtree, e.allow)
		}
	}
}

// capabilityExemptionFor 查 capability 子树内对 imp 的豁免条目。
func capabilityExemptionFor(fileRel, imp string) (purityExemption, bool) {
	for _, e := range capabilityPurityExemptions {
		if strings.HasPrefix(fileRel, e.subtree) && strings.HasPrefix(imp, e.allow) {
			return e, true
		}
	}
	return purityExemption{}, false
}
