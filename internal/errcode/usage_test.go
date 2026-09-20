package errcode

// 注册表使用点扫描测试（MG-2，B4）：遍历注册表全部错误/警告码，断言每码
// 在仓内生产代码（internal + cmd，排除 _test 与注册表自身）有 ≥1 引用点
//（带引号的字符串形态——错误码只以字面量进入 apperr.New/errorf 等信封
// 构造面）。注册但零引用的码 = 注册表与实现脱节（漏发或死注册）：真实
// 漏发补最小实现；确属预留的入下方豁免清单（每条必须有注释理由）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codeExemptions 是零引用码的豁免清单（MG-2：豁免必须有注释理由；注册表
// 行同步加 `// 预留：…` 注释）。
var codeExemptions = map[string]string{
	// 预留：恢复器校验失败码（备份集/主密钥指纹不匹配拒绝半恢复）——
	// 恢复器未实现（横切评审确认的预留码，state-model §2.7）。
	"E_BACKUP_KEY_MISSING": "预留：恢复器未实现（横切评审确认）",
	// 预留：跨点移动确认门控码——v0.1 单机无第二候选，MoveBinding 直接
	// E_CAPABILITY_REQUIRES_MULTI_NODE 守卫拒绝；带确认的换点（rebind）
	// 随 v0.2 多节点 + 备份恢复迁移路径接线。
	"E_PLACEMENT_MOVE_REQUIRES_ACK": "预留：rebind/换点确认流 v0.2（单机守卫拒绝）",
}

// productionSources 收集 internal 与 cmd 下的生产 .go 文件文本（排除
// _test 与注册表包自身）。
func productionSources(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	out := map[string]string{}
	for _, sub := range []string{"internal", "cmd"} {
		base := filepath.Join(root, sub)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// 排除注册表包自身（定义文件不算引用点）。
				if filepath.Base(path) == "errcode" && path == filepath.Join(root, "internal", "errcode") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(raw)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no production sources scanned（扫描基座失效）")
	}
	return out
}

// TestRegistryCodesReferencedInProduction 注册表全量扫描：每码在生产代码
// 有 ≥1 个带引号引用点（或进入带理由的豁免清单）。
func TestRegistryCodesReferencedInProduction(t *testing.T) {
	srcs := productionSources(t)
	for _, c := range Default().All() {
		if _, exempt := codeExemptions[c.ID]; exempt {
			continue
		}
		quoted := `"` + c.ID + `"`
		found := ""
		for path, src := range srcs {
			if strings.Contains(src, quoted) {
				found = path
				break
			}
		}
		if found == "" {
			t.Errorf("错误码 %s 注册后零生产引用（真实漏发则补实现；确属预留则在 codeExemptions 与注册表行注明理由）", c.ID)
		}
	}
}

// TestCodeExemptionsStillRegistered 豁免清单健康度：豁免项必须仍在注册表
// （豁免的是「零引用」而不是「可注销」——码永不复用）。
func TestCodeExemptionsStillRegistered(t *testing.T) {
	for id := range codeExemptions {
		if _, ok := Default().Get(id); !ok {
			t.Errorf("豁免清单项 %s 不在注册表（清单与注册表脱节）", id)
		}
	}
}
