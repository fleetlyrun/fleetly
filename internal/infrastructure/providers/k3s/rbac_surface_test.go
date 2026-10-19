package k3s

// RBAC 调用面对账守卫（R06-H1 根因）：fake clientset 不执法 RBAC、表测只
// 钉表自身、e2e can-i 只抽样——新增 p.cli 调用面漏扩 desiredClusterRole()
// 时三道门全绿而真机 403。本守卫用 AST 扫描 k3s 生产 .go 中 SA 客户端
// （p.cli）上的 typed-client 调用，提取 (API group, resource, verb) 与
// 权限表对账：
//
//   - 调用面缺规则 → 红（扩表或改调用面）；
//   - 未识别的 client / resource / verb → 红（防扫描器静默漏网）；
//   - RESTClient 裸调用按条豁免并带理由，豁免不再命中同样红（双向保鲜）；
//   - 生产文件数 / 调用点数下限断言（防目录漂移或正则失配导致"零扫描"）。
//
// boot.* 是自举（管理 kubeconfig）身份面，不属 SA 权限表——只扫 p.cli。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// rbacSurface 是一条调用面三元组（RBAC PolicyRule 的展开形态）。
type rbacSurface struct {
	group    string
	resource string
	verb     string
}

func (s rbacSurface) String() string {
	group := s.group
	if group == "" {
		group = "core"
	}
	return group + "/" + s.resource + " " + s.verb
}

// clientGroups 是扫描器认识的 typed client 构造器 → API group。
var clientGroups = map[string]string{
	"CoreV1":       "",
	"AppsV1":       "apps",
	"NetworkingV1": "networking.k8s.io",
	"PolicyV1":     "policy",
}

// resourceNames 是资源构造器 → RBAC resource 真名（子资源带斜杠）。
var resourceNames = map[string]string{
	"Namespaces":             "namespaces",
	"Nodes":                  "nodes",
	"Pods":                   "pods",
	"Services":               "services",
	"Secrets":                "secrets",
	"ConfigMaps":             "configmaps",
	"PersistentVolumeClaims": "persistentvolumeclaims",
	"Events":                 "events",
	"Deployments":            "deployments",
	"DaemonSets":             "daemonsets",
	"NetworkPolicies":        "networkpolicies",
	"Evictions":              "pods/eviction",
}

// clientVerbs 是 typed-client 动词 → RBAC 动词（DeleteCollection 归一为
// deletecollection；GetLogs 是 pods/log 的 get 客户端形态，单独处理）。
var clientVerbs = map[string]string{
	"Get":              "get",
	"List":             "list",
	"Create":           "create",
	"Update":           "update",
	"Patch":            "patch",
	"Delete":           "delete",
	"DeleteCollection": "deletecollection",
	"Watch":            "watch",
}

// rbacSurfaceExemptions 是带理由的裸调用豁免（key 由 classifyCallChain
// 产出）。豁免不再命中即红。
var rbacSurfaceExemptions = map[string]string{
	"raw:CoreV1":    "pods/exec create is issued as a raw subresource request (Post on the exec URL); the pods/exec create rule covers the verb",
	"raw:Discovery": "/readyz is a non-resource URL; the built-in system:discovery ClusterRole (bound to system:authenticated) covers the probe",
}

// surfaceClassification 是选择子链分类结果。recognized=false 即"未识别"
// （守卫红）；surface 零值 = 中间构造调用（不参与对账）；exemption 非空 =
// 显式豁免 key。
type surfaceClassification struct {
	surface    rbacSurface
	recognized bool
	exemption  string
}

// classifyCallChain 把 p 起的选择子链（steps[0] == "cli"）分类为调用面。
// 链形态：p.cli.<ClientCtor>().<ResourceCtor>(...).<Verb>(...)；
// 资源构造器之后的额外调用（GetLogs(...).Stream(ctx)）不改变动词归属。
func classifyCallChain(steps []string) surfaceClassification {
	if len(steps) < 3 {
		return surfaceClassification{recognized: true} // 中间构造调用
	}
	if steps[1] == "Discovery" {
		return surfaceClassification{recognized: true, exemption: "raw:Discovery"}
	}
	group, known := clientGroups[steps[1]]
	if !known {
		return surfaceClassification{}
	}
	if steps[2] == "RESTClient" {
		return surfaceClassification{recognized: true, exemption: "raw:" + steps[1]}
	}
	resource, known := resourceNames[steps[2]]
	if !known {
		return surfaceClassification{}
	}
	if len(steps) < 4 {
		return surfaceClassification{recognized: true} // 中间资源构造调用
	}
	verbName := steps[3]
	if resource == "pods/eviction" {
		if verbName != "Evict" {
			return surfaceClassification{}
		}
		return surfaceClassification{
			surface:    rbacSurface{group: group, resource: resource, verb: "create"},
			recognized: true,
		}
	}
	if resource == "pods" && verbName == "GetLogs" {
		return surfaceClassification{
			surface:    rbacSurface{group: group, resource: "pods/log", verb: "get"},
			recognized: true,
		}
	}
	verb, known := clientVerbs[verbName]
	if !known {
		return surfaceClassification{}
	}
	return surfaceClassification{
		surface:    rbacSurface{group: group, resource: resource, verb: verb},
		recognized: true,
	}
}

// callChain 把调用表达式拆成"根标识符 + 选择子方法名序列"（CallExpr 展开
// 到 Fun；p.cli.X() 的根是 p，steps 从 cli 起）。
func callChain(expr ast.Expr) (string, []string, bool) {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name, nil, true
	case *ast.SelectorExpr:
		root, steps, ok := callChain(x.X)
		if !ok {
			return "", nil, false
		}
		return root, append(steps, x.Sel.Name), true
	case *ast.CallExpr:
		return callChain(x.Fun)
	case *ast.ParenExpr:
		return callChain(x.X)
	default:
		return "", nil, false
	}
}

// desiredSurfaceRules 把 desiredClusterRole() 展开为三元组集合（无通配
// 规则——新增通配即走"缺规则"红，提示显式裁决）。
func desiredSurfaceRules() map[rbacSurface]bool {
	rules := map[rbacSurface]bool{}
	for _, rule := range desiredClusterRole().Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					rules[rbacSurface{group: group, resource: resource, verb: verb}] = true
				}
			}
		}
	}
	return rules
}

// missingSurfaces 返回调用面中未被权限表覆盖的三元组（排序稳定）。
func missingSurfaces(observed map[rbacSurface]bool) []string {
	rules := desiredSurfaceRules()
	var missing []string
	for surface := range observed {
		if !rules[surface] {
			missing = append(missing, surface.String())
		}
	}
	sort.Strings(missing)
	return missing
}

// TestRBACSurfaceCoversCallSurface：生产调用面 ⊆ 最小权限表。
func TestRBACSurfaceCoversCallSurface(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("scan k3s package: %v", err)
	}
	fset := token.NewFileSet()
	observed := map[rbacSurface]bool{}
	exemptionHits := map[string]int{}
	var unrecognized []string
	files, callSites := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			root, steps, ok := callChain(call.Fun)
			if !ok || root != "p" || len(steps) == 0 {
				return true
			}
			pos := fset.Position(call.Pos())
			if steps[0] != "cli" {
				// SA 客户端字段只许 cli；新字段挂 typed client 即红（防
				// 第二个身份面绕过对账）。
				if len(steps) >= 2 {
					if _, typed := clientGroups[steps[1]]; typed {
						unrecognized = append(unrecognized, pos.String()+": call on p."+steps[0]+" typed client — the RBAC guard only reconciles p.cli (SA identity); add it explicitly or move the call")
					}
				}
				return true
			}
			classified := classifyCallChain(steps)
			if !classified.recognized {
				unrecognized = append(unrecognized, pos.String()+": unrecognized client chain p."+strings.Join(steps, ".")+" — extend the guard classifier (never let a call surface escape reconciliation)")
				return true
			}
			if classified.exemption != "" {
				exemptionHits[classified.exemption]++
				return true
			}
			if classified.surface != (rbacSurface{}) {
				observed[classified.surface] = true
				callSites++
			}
			return true
		})
	}

	sort.Strings(unrecognized)
	for _, issue := range unrecognized {
		t.Error(issue)
	}
	for _, missing := range missingSurfaces(observed) {
		t.Errorf("call surface %s is not covered by desiredClusterRole(); extend the RBAC table (and rbac_test.go / e2e can-i) in the same change", missing)
	}
	// 扫描面锚（R06-H1 三条真机 403 动线）：不仅表里要有规则，生产调用点
	// 必须真的被扫到——否则分类器失配会让"缺则红"退化成语义空转。
	for _, want := range []rbacSurface{
		{group: "", resource: "configmaps", verb: "create"},
		{group: "networking.k8s.io", resource: "networkpolicies", verb: "update"},
		{group: "", resource: "pods", verb: "deletecollection"},
	} {
		if !observed[want] {
			t.Errorf("scan missed production call surface %s — the classifier no longer matches the real call shape (R06-H1 anchor)", want)
		}
	}
	for key, reason := range rbacSurfaceExemptions {
		if reason == "" {
			t.Errorf("RBAC surface exemption %q must carry a reason", key)
		}
		if exemptionHits[key] == 0 {
			t.Errorf("RBAC surface exemption %q no longer matches a real call; remove it", key)
		}
	}
	if files < 15 {
		t.Fatalf("scanned only %d production files — guard is blind, fix the scan", files)
	}
	if callSites < 50 {
		t.Fatalf("scanned only %d p.cli call sites — guard is blind, fix the scan", callSites)
	}
	t.Logf("RBAC surface scan: %d production files, %d call sites, %d distinct (group,resource,verb) pairs, %d exemptions",
		files, callSites, len(observed), len(exemptionHits))
}

// TestClassifyCallChain：分类器对调用形态的识别/未识别矩阵（未识别必须
// 显式红——分类器自身不能静默吞链）。
func TestClassifyCallChain(t *testing.T) {
	cases := []struct {
		name       string
		steps      []string
		want       rbacSurface
		recognized bool
		exemption  string
	}{
		{"pods list", []string{"cli", "CoreV1", "Pods", "List"}, rbacSurface{"", "pods", "list"}, true, ""},
		{"configmap create", []string{"cli", "CoreV1", "ConfigMaps", "Create"}, rbacSurface{"", "configmaps", "create"}, true, ""},
		{"networkpolicy update", []string{"cli", "NetworkingV1", "NetworkPolicies", "Update"}, rbacSurface{"networking.k8s.io", "networkpolicies", "update"}, true, ""},
		{"pods deletecollection", []string{"cli", "CoreV1", "Pods", "DeleteCollection"}, rbacSurface{"", "pods", "deletecollection"}, true, ""},
		{"pod logs get", []string{"cli", "CoreV1", "Pods", "GetLogs", "Stream"}, rbacSurface{"", "pods/log", "get"}, true, ""},
		{"pod eviction create", []string{"cli", "PolicyV1", "Evictions", "Evict"}, rbacSurface{"policy", "pods/eviction", "create"}, true, ""},
		{"raw exec request", []string{"cli", "CoreV1", "RESTClient", "Post"}, rbacSurface{}, true, "raw:CoreV1"},
		{"discovery probe", []string{"cli", "Discovery", "RESTClient", "Get"}, rbacSurface{}, true, "raw:Discovery"},
		{"intermediate client ctor", []string{"cli", "CoreV1"}, rbacSurface{}, true, ""},
		{"intermediate resource ctor", []string{"cli", "CoreV1", "Pods"}, rbacSurface{}, true, ""},
		{"unknown client", []string{"cli", "BatchV1", "Jobs", "Create"}, rbacSurface{}, false, ""},
		{"unknown resource", []string{"cli", "CoreV1", "Widgets", "Get"}, rbacSurface{}, false, ""},
		{"unknown verb", []string{"cli", "CoreV1", "Pods", "Explode"}, rbacSurface{}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCallChain(tc.steps)
			if got.recognized != tc.recognized || got.surface != tc.want || got.exemption != tc.exemption {
				t.Fatalf("classify(%v) = %+v, want surface=%v recognized=%v exemption=%q",
					tc.steps, got, tc.want, tc.recognized, tc.exemption)
			}
		})
	}
}

// TestMissingSurfacesDetectsGap：对账函数本身不能空转——未覆盖三元组必须
// 被点名（对拍表驱动的"缺则红"面）。
func TestMissingSurfacesDetectsGap(t *testing.T) {
	rules := desiredSurfaceRules()
	if !rules[rbacSurface{"", "configmaps", "create"}] {
		t.Fatal("desired table must cover configmaps create (R06-H1 regression anchor)")
	}
	if !rules[rbacSurface{"networking.k8s.io", "networkpolicies", "update"}] {
		t.Fatal("desired table must cover networkpolicy update (R06-H1 regression anchor)")
	}
	if !rules[rbacSurface{"", "pods", "deletecollection"}] {
		t.Fatal("desired table must cover pods deletecollection (R06-H1 regression anchor)")
	}
	missing := missingSurfaces(map[rbacSurface]bool{{group: "", resource: "pods", verb: "explode"}: true})
	if len(missing) != 1 || !strings.Contains(missing[0], "pods explode") {
		t.Fatalf("missingSurfaces must flag an uncovered call surface, got %v", missing)
	}
}
