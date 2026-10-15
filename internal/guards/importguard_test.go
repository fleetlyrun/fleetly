package guards

import (
	"strings"
	"testing"
)

// orchestratorSDKPrefixes 是编排器/基础设施 SDK 的 import 前缀清单
// （ADR-0001：领域与引擎零编排器概念；架构 §2：internal/infrastructure/providers 是全仓
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

const providersDir = "internal/infrastructure/providers/"

// TestOrchestratorSDKConfinedToProviders：编排器 SDK 只准出现在
// internal/infrastructure/providers/**（含测试文件——测试同样不得泄漏 SDK 到领域层）。
func TestOrchestratorSDKConfinedToProviders(t *testing.T) {
	for _, f := range scanGoFiles(t) {
		for _, imp := range f.imports {
			if !matchesAnyPrefix(imp, orchestratorSDKPrefixes) {
				continue
			}
			if !strings.HasPrefix(f.rel, providersDir) {
				t.Errorf("%s imports orchestrator SDK %q — only internal/infrastructure/providers/** may (ADR-0001)", f.rel, imp)
			}
		}
	}
}

// leafSubtree 是叶子包子树登记（架构 §2：叶子不 import 任何其他 internal
// 包）。model/spec 自架构 §2 起即叶子；capability/identity 是 C-12 扩面
// （capability 只 import 标准库，identity 只加 lynx 外部库——现况干净，
// 违例属裁决项：报修不走守卫豁免）；schema 是 F1.4 自描述真源（只 import
// genproto 与标准库，engine/api 层反向注入注册——保持叶子防层析下沉）。
// identity 是上下文种子根包（架构 §2：P1b 起各限界上下文在此扩容聚合）：
// 叶子面只执法根包文件（rootOnly），聚合子包（audit，P1b 批二）按 domain
// 聚合规则执法（dep_matrix domain 行 + 三红线），种子叶纯度不外溢子包。
type leafSubtree struct {
	prefix   string
	rootOnly bool // true = 只执法目录直接子文件（上下文种子根包），聚合子包不在叶面
}

var leafSubtrees = []leafSubtree{
	{prefix: "internal/domain/shared/model/"},
	{prefix: "internal/domain/shared/spec/"},
	{prefix: "internal/domain/shared/capability/"},
	{prefix: "internal/domain/identity/", rootOnly: true},
	{prefix: "internal/application/schema/"},
}

// fleetlyInternal 报告 import 是否 fleetly 主 module 的 internal 包。
func fleetlyInternal(imp string) bool {
	return strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/internal/")
}

// TestLeafPackagesPurity：叶子子树（model/spec/capability/identity 根包）
// 不得 import 子树之外的任何 fleetly internal 包（叶子纯度；子树内部互引
// 合法，如 model/errcode 平级协作）。rootOnly 子树（identity）的聚合子包
// 不在叶面——它们按 domain 聚合规则另行执法。
func TestLeafPackagesPurity(t *testing.T) {
	for _, f := range scanGoFiles(t) {
		var own string
		isLeaf := false
		for _, sub := range leafSubtrees {
			if !strings.HasPrefix(f.rel, sub.prefix) {
				continue
			}
			if sub.rootOnly && strings.Contains(strings.TrimPrefix(f.rel, sub.prefix), "/") {
				continue // 聚合子包（如 identity/audit）不在种子叶面
			}
			own, isLeaf = sub.prefix, true
			break
		}
		if !isLeaf {
			continue
		}
		for _, imp := range f.imports {
			if fleetlyInternal(imp) && !strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/"+own) {
				t.Errorf("leaf package %s imports %q — leaf subtrees (model/spec/capability/identity) must not depend on other internal packages (架构 §2)", f.rel, imp)
			}
		}
	}
}

// TestProvidersImportBoundary：providers 只准消费 capability/spec/model
// （架构 §2 依赖方向：providers → {capability, spec, model}）。
func TestProvidersImportBoundary(t *testing.T) {
	allowed := []string{
		"github.com/fleetlyrun/fleetly/internal/domain/shared/capability",
		"github.com/fleetlyrun/fleetly/internal/domain/shared/model",
		"github.com/fleetlyrun/fleetly/internal/domain/shared/spec",
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
			if !strings.HasPrefix(imp, "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/") {
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
