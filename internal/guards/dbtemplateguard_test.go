package guards

// 引擎名反扫守卫（架构评审第二轮候选 1，2026-10-03）：数据库引擎值域的
// 唯一真源 = internal/engine/dbtemplate 注册表（dbtemplate.Engines()）。
// 引擎名字符串字面量出现在子包之外的 internal/ 代码（注释与测试夹具
// 除外）即红——per-engine 知识必须住在模板 adapter 里，别处长出硬拷贝
// 就是 locality 破口（先例：CLI 帮助文案硬编码曾无守卫漂移）。cmd/ 面由
// cmd/fleetly/cmd/databases_guard_test.go 单独对账（其帮助文案形态多样，
// 由包内守卫按形态解析）；本守卫只扫 internal/。例外条目带理由注释，
// 不再命中即红（白名单双向保鲜，wording/禁词扫描同款纪律）。

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
)

// engineNameLiteralAllowlist 是反扫例外（路径前缀 → 理由）。条目不再命中
// 即红：删除失效例外与修掉真泄漏同批。
var engineNameLiteralAllowlist = map[string]string{
	// 目前一例外：dokploy 迁移解析的引擎词映射（F3.3，ADR-0050 决策 5）——
	// 键侧是 dokploy 的 type 词、值侧恰好与 fleetly 引擎词同名。该表承载
	// 的是"竞品词汇 → fleetly 词汇"的翻译面，不携带任何引擎知识（无镜像/
	// 端口/命令）；值侧在册性由 CreateDatabase 受理位以 dbtemplate 注册表
	// 单源执法（未知词在解析期即 skip 进报告，见 dokploy.go）。
	"internal/spec/dokploy.go": "dokploy type-word to fleetly engine-word translation (ADR-0050 migration hook); carries no engine knowledge — registry membership is enforced at CreateDatabase acceptance",
	// dbbrowser 注册表（F3.6，ADR-0051 决策 3）：engine 名作为映射键出现——
	// 值侧是浏览器方言 adapter（与 dbtemplate 的引擎知识分立：无镜像/端口/
	// 凭证渲染）。映射的 totality（对 dbtemplate.Engines 全覆盖）由
	// dbbrowser_test 的 TestRegistryCoversDbtemplateEngines 双向钉死；值域
	// 外的引擎键走 E_BROWSER_UNSUPPORTED 诚实失败。
	"internal/engine/dbbrowser/dbbrowser.go": "engine-to-browser registry keys (ADR-0051 decision 3); totality over dbtemplate.Engines() is pinned by dbbrowser_test and unknown engines fail with E_BROWSER_UNSUPPORTED",
}

// scanEngineNameLiterals 是反扫纯核（红灯实验直测）：files 是路径→源文本，
// engines 是注册表值域；返回全量命中清单（例外不过滤——例外表的对账由
// 调用方与保鲜测试分别承担）。命中形态 = 引擎名的完整带引号字面量
// （"postgres"），前缀撞车不误伤（"postgresql://…"、"pgvector/pgvector:…"
// 都不含闭合引号形态）。
func scanEngineNameLiterals(files map[string]string, engines []string) []string {
	var errs []string
	for _, engine := range engines {
		needle := `"` + engine + `"`
		for path, src := range files {
			if strings.Contains(src, needle) {
				errs = append(errs, fmt.Sprintf(
					"%s: engine name %q appears as a string literal outside internal/engine/dbtemplate — per-engine knowledge must live in the template adapter (dbtemplate registry is the single source)", path, engine))
			}
		}
	}
	sort.Strings(errs)
	return errs
}

// TestEngineNamesStayInTemplatePackage：internal/ 非测试代码引擎名字面量
// 反扫（值域真源 = dbtemplate.Engines()，运行时导入保新鲜）。例外表路径
// 的命中在此放行（例外面由保鲜测试单独对账）。
func TestEngineNamesStayInTemplatePackage(t *testing.T) {
	files := map[string]string{}
	root := filepath.Join(repoRoot(t), "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot(t), path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = readFileLF(t, filepath.ToSlash(rel))
			return nil
		}
		// 目录裁剪：testdata 夹具与模板子包自身（注册表键是值域真源所在）。
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.Name() == "dbtemplate" && filepath.Dir(path) == filepath.Join(root, "engine") {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go sources found under internal/ — the scan surface moved; update this guard with it")
	}
	for _, e := range scanEngineNameLiterals(files, dbtemplate.Engines()) {
		if _, allowed := engineNameLiteralAllowlist[strings.SplitN(e, ":", 2)[0]]; allowed {
			continue
		}
		t.Error(e)
	}
}

// TestEngineNameLiteralAllowlistFresh：例外表双向保鲜——条目不再命中即红
// （失效例外与真泄漏同等是债）。
func TestEngineNameLiteralAllowlistFresh(t *testing.T) {
	if len(engineNameLiteralAllowlist) == 0 {
		return // 零例外态：新增条目随真实需求来
	}
	files := map[string]string{}
	for path := range engineNameLiteralAllowlist {
		files[path] = readFileLF(t, path)
	}
	hits := scanEngineNameLiterals(files, dbtemplate.Engines())
	if len(hits) == 0 {
		t.Fatalf("allowlist entries no longer match anything — retire them with the fix: %v", engineNameLiteralAllowlist)
	}
}

// TestEngineNameScanRedLight：反扫核的常驻红灯实验（守卫自身失明即红）。
func TestEngineNameScanRedLight(t *testing.T) {
	files := map[string]string{
		"internal/api/fleetlygrpc/databases.go": `x := "postgres"`,
	}
	hits := scanEngineNameLiterals(files, []string{"postgres", "redis"})
	if len(hits) != 1 || !strings.Contains(hits[0], `"postgres"`) {
		t.Fatalf("guard went blind: expected the postgres literal to be caught, got %v", hits)
	}

	// 前缀撞车不误伤：URL 与镜像引用不含闭合引号形态。
	clean := map[string]string{
		"internal/engine/projection.go": `url := "postgresql://fleetly:x@db-1:5432/fleetly"` + "\n" +
			`image := "pgvector/pgvector:0.8.6-pg17-bookworm"`,
	}
	if hits := scanEngineNameLiterals(clean, []string{"postgres", "pgvector"}); len(hits) != 0 {
		t.Fatalf("prefix collisions must not be flagged, got %v", hits)
	}
}
