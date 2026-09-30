package guards

import (
	"strings"
	"testing"
)

// orchestratorSDKPrefixes 是编排器/基础设施 SDK 的 import 前缀清单
// （ADR-0001：领域与引擎零编排器概念；架构 §2：internal/providers 是全仓
// 唯一允许 import 编排器/基础设施 SDK 的地方）。新增第二运行时的 SDK
// （k8s.io/*、nomad 等）时在此登记。
//
// 注意：compose-spec/compose-go 不在此列——它是 Compose 文件格式解析器
// （Source 归一化输入面），不是编排器 SDK；traefik 经 HTTP provider API
// 下发配置，其 SDK（如有）在此圈禁。
var orchestratorSDKPrefixes = []string{
	"github.com/moby/",
	"github.com/docker/",
	"k8s.io/",
	"github.com/hashicorp/nomad",
	"github.com/traefik/",
	"github.com/containous/",
}

const providersDir = "internal/providers/"

// TestOrchestratorSDKConfinedToProviders：编排器 SDK 只准出现在
// internal/providers/**（含测试文件——测试同样不得泄漏 SDK 到领域层）。
func TestOrchestratorSDKConfinedToProviders(t *testing.T) {
	for _, f := range scanGoFiles(t) {
		for _, imp := range f.imports {
			if !matchesAnyPrefix(imp, orchestratorSDKPrefixes) {
				continue
			}
			if !strings.HasPrefix(f.rel, providersDir) {
				t.Errorf("%s imports orchestrator SDK %q — only internal/providers/** may (ADR-0001)", f.rel, imp)
			}
		}
	}
}

// leafSubtrees 是叶子包子树（架构 §2：model/spec 不 import 任何其他
// internal 包）。
var leafSubtrees = []string{"internal/model/", "internal/spec/"}

// fleetlyInternal 报告 import 是否 fleetly 主 module 的 internal 包。
func fleetlyInternal(imp string) bool {
	return strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/internal/")
}

// TestLeafPackagesPurity：internal/model/** 与 internal/spec/** 不得 import
// 子树之外的任何 fleetly internal 包（叶子纯度；子树内部互引合法，
// 如 model/errcode 平级协作）。
func TestLeafPackagesPurity(t *testing.T) {
	for _, f := range scanGoFiles(t) {
		var own string
		isLeaf := false
		for _, sub := range leafSubtrees {
			if strings.HasPrefix(f.rel, sub) {
				own, isLeaf = sub, true
				break
			}
		}
		if !isLeaf {
			continue
		}
		for _, imp := range f.imports {
			if fleetlyInternal(imp) && !strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/"+own) {
				t.Errorf("leaf package %s imports %q — model/spec must not depend on other internal packages (架构 §2)", f.rel, imp)
			}
		}
	}
}

// TestProvidersImportBoundary：providers 只准消费 capability/spec/model
// （架构 §2 依赖方向：providers → {capability, spec, model}）。
func TestProvidersImportBoundary(t *testing.T) {
	allowed := []string{
		"github.com/fleetlyrun/fleetly/internal/capability",
		"github.com/fleetlyrun/fleetly/internal/model",
		"github.com/fleetlyrun/fleetly/internal/spec",
	}
	for _, f := range scanGoFiles(t) {
		if !strings.HasPrefix(f.rel, providersDir) {
			continue
		}
		for _, imp := range f.imports {
			if !fleetlyInternal(imp) {
				continue
			}
			if !matchesAnyPrefix(imp, allowed) {
				t.Errorf("%s imports %q — providers may only consume capability/spec/model (架构 §2)", f.rel, imp)
			}
		}
	}
}

// TestProvidersOnlyImportedByCmd：除 cmd/** 外无人 import providers
// （经注册表间接装配；装配点在 cmd/fleetlyd 的 blank import）。
func TestProvidersOnlyImportedByCmd(t *testing.T) {
	for _, f := range scanGoFiles(t) {
		for _, imp := range f.imports {
			if !strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/internal/providers/") {
				continue
			}
			if !strings.HasPrefix(f.rel, "cmd/") && !strings.HasPrefix(f.rel, providersDir) {
				t.Errorf("%s imports %q — providers are wired only from cmd (架构 §2)", f.rel, imp)
			}
		}
	}
}

func matchesAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
