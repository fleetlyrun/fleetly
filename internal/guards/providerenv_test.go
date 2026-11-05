package guards

// 提案 2026-11-05 C1 终态：providers 生产代码 os.Getenv 站点级白名单执法。
// C1 主体批（FactoryParams 载体收拢，ADR-0036 载体收拢节）后，配置进
// provider 只剩显式参数一条通道——装配期 env 读点收敛为 assembly（proxy
// 四键旧拼写兜底 + DOCKER_HOST）与 relay（DOCKER_HOST）两处清单，provider
// 面零部署配置 env 读取。
//
// 白名单终态仅一条：FLEETLY_RAILPACK_BIN（钉版宿主二进制路径覆盖，
// dev/pin 面非部署配置）。新增读点（含新文件复用旧键——按 (文件, 键)
// 站点执法，按键白名单挡不住照抄）即红；条目退役（读点上移 assembly）
// 不再命中同样红（双向保鲜，wording 同款机制）。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// providerEnvSite 是一个获准的 env 读点：文件 + 键 + 理由。
type providerEnvSite struct {
	file   string
	key    string
	reason string
}

// providerEnvAllowlist 是 providers 生产代码的全部合法 os.Getenv 站点
// （C1 主体批后的终态清单——预计仅 railpack 钉版路径覆盖一条）。
var providerEnvAllowlist = []providerEnvSite{
	{"internal/infrastructure/providers/builders/railpack.go", "FLEETLY_RAILPACK_BIN",
		"终态保留：钉版宿主二进制路径覆盖（dev/pin 面，非部署配置）"},
}

// providerEnvPattern 匹配完整读表达式（installenv 同款锚：注释里提及
// 键名不算读点）。
var providerEnvPattern = regexp.MustCompile(`os\.Getenv\("([A-Za-z0-9_]+)"\)`)

func TestProvidersEnvReadSites(t *testing.T) {
	root := repoRoot(t)
	providersRoot := filepath.Join(root, "internal", "infrastructure", "providers")

	allowed := make(map[string]providerEnvSite, len(providerEnvAllowlist))
	for _, s := range providerEnvAllowlist {
		allowed[s.file+"\x00"+s.key] = s
	}

	hits := map[string]bool{}
	err := filepath.WalkDir(providersRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range providerEnvPattern.FindAllStringSubmatch(string(src), -1) {
			hits[rel+"\x00"+m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 && len(providerEnvAllowlist) > 0 {
		t.Fatal("scan found no os.Getenv sites but the allowlist is non-empty — guard is blind, fix the scan")
	}

	for site := range hits {
		if _, ok := allowed[site]; !ok {
			parts := strings.SplitN(site, "\x00", 2)
			t.Errorf("%s reads env %q via os.Getenv — provider env reads are closed (proposal 2026-11-05 C1): route the value through assembly (FactoryParams); deployment config keys need an ADR to re-open a site",
				parts[0], parts[1])
		}
	}
	for site, entry := range allowed {
		if !hits[site] {
			parts := strings.SplitN(site, "\x00", 2)
			t.Errorf("allowlist entry %s/%s no longer matches any os.Getenv read (reason: %s) — remove the stale entry (双向保鲜)",
				parts[0], parts[1], entry.reason)
		}
	}
}
