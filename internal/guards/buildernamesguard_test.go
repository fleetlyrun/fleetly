package guards

// builder 名对账守卫（ADR-0007 Builder 词条 + ADR-0032 Builder 家族）：
// builder 名有两处真源——internal/spec 叶子常量集（BuilderNames 值域，受理
// 校验的唯一依据）与 internal/providers/builders 的 RegisterFactory 注册
// 字面量集（运行时装配的唯一依据）。两集漂移的形态都是静默坏：spec 有而
// 注册无 = 受理放行、运行时永不解析（executeBuild 终态 failed）；注册有而
// spec 无 = 每个 carrying spec 都被受理拒绝。本守卫对源文本对账两集，
// 任一方向漂移即红（railpackpin_test.go 同款静态对账形态）。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// specBuilderNameRe 抽取 spec 叶子常量块的 builder 名字面量
// （BuilderXxx = "name" 形态；使用点都是标识符引用不带字面量，只有常量
// 块命中）。
var specBuilderNameRe = regexp.MustCompile(`(?m)^\s*Builder[A-Za-z]+\s*=\s*"([a-z0-9-]+)"`)

// registeredBuilderNameRe 抽取 builders 包的 KindBuilder 注册名（注册名
// 是调用方 spec 携带的字面量，必须与叶子常量逐字一致）。
var registeredBuilderNameRe = regexp.MustCompile(`capability\.RegisterFactory\(\s*capability\.KindBuilder,\s*"([a-z0-9-]+)"`)

// reconcileBuilderNames 对账两真源，返回漂移错误（空集=一致）。纯核：
// 红灯实验注入漂移集直接咬。
func reconcileBuilderNames(specNames, registered []string) []string {
	toSet := func(names []string) map[string]bool {
		m := make(map[string]bool, len(names))
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	specSet, regSet := toSet(specNames), toSet(registered)
	var errs []string
	missing := func(have, want map[string]bool) []string {
		var out []string
		for n := range want {
			if !have[n] {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	for _, n := range missing(regSet, specSet) {
		errs = append(errs, fmt.Sprintf("builder %q is declared in internal/spec (BuilderNames value domain) but no providers/builders factory registers it — admission would accept specs that never resolve at runtime; register a factory or retire the constant via ADR (ADR-0007 wording freeze / ADR-0032)", n))
	}
	for _, n := range missing(specSet, regSet) {
		errs = append(errs, fmt.Sprintf("builder %q is registered by internal/providers/builders but the spec leaf declares no such constant — admission (ValidateBuild) rejects every spec carrying it; align the registration name with the frozen value domain (CONTEXT.md Builder entry, ADR-0007)", n))
	}
	return errs
}

// TestBuilderNamesSpecAndRegistrationAgree：spec 叶子常量集 × builders
// 注册字面量集逐字对账。
func TestBuilderNamesSpecAndRegistrationAgree(t *testing.T) {
	specSrc := readFileLF(t, "internal/spec/spec.go")
	var specNames []string
	for _, m := range specBuilderNameRe.FindAllStringSubmatch(specSrc, -1) {
		specNames = append(specNames, m[1])
	}
	if len(specNames) == 0 {
		t.Fatal("no Builder* string constants found in internal/spec/spec.go — the value domain moved; update this guard with it")
	}

	entries, err := os.ReadDir(filepath.Join(repoRoot(t), filepath.FromSlash("internal/providers/builders")))
	if err != nil {
		t.Fatal(err)
	}
	var registered []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src := readFileLF(t, "internal/providers/builders/"+e.Name())
		for _, m := range registeredBuilderNameRe.FindAllStringSubmatch(src, -1) {
			registered = append(registered, m[1])
		}
	}
	if len(registered) == 0 {
		t.Fatal("no capability.KindBuilder registration literal found under internal/providers/builders — the registration surface moved; update this guard with it")
	}

	for _, e := range reconcileBuilderNames(specNames, registered) {
		t.Error(e)
	}
}

// TestBuilderNamesGuardRedLight 是对账核的常驻红灯实验：spec 独有名
// （注册面缺）与注册独有名（spec 值域缺）两个漂移方向必须被咬住，一致集
// 零错误——守卫自身失明即红。
func TestBuilderNamesGuardRedLight(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       []string
		registered []string
		want       string
	}{
		{
			name:       "spec-only-drift",
			spec:       []string{"dockerfile", "railpack", "static", "ko"},
			registered: []string{"dockerfile", "railpack", "static"},
			want:       `builder "ko" is declared in internal/spec`,
		},
		{
			name:       "registration-only-drift",
			spec:       []string{"dockerfile", "railpack", "static"},
			registered: []string{"dockerfile", "railpack", "static", "kaniko"},
			want:       `builder "kaniko" is registered by internal/providers/builders`,
		},
		{
			name:       "agree",
			spec:       []string{"dockerfile", "railpack", "static"},
			registered: []string{"static", "dockerfile", "railpack"},
			want:       "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := reconcileBuilderNames(tc.spec, tc.registered)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("agreement must be green, got: %v", errs)
				}
				return
			}
			for _, e := range errs {
				if strings.Contains(e, tc.want) {
					return
				}
			}
			t.Fatalf("guard went blind: expected an error containing %q, got %v", tc.want, errs)
		})
	}
}
