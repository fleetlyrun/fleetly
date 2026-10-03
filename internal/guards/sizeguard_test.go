package guards

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sizeguard：非生成物 Go 文件体积红线（P4，裁决阈值 1200 行，docs/
// design/2026-10-03-optimization-proposals.md）。六竞品的 God module 全部
// 从功能正确的代码长出来（coolify 5806 行部署 job、zane 3700 行 models、
// porter 1750 行 manifest——docs/research/2026-10-03 §6.2）：现有守卫
// 全部执法"方向"（import/词汇/单源），无一执法"聚集"。红线 1200 = 现仓
// 最大正常文件（spec/normalize.go 628 行）的一倍余量、竞品事故水位的
// 一个量级之下。
//
// 豁免条目带理由注释登记在 sizeguardWhitelist；条目文件消失或降到线下
// 即红（白名单双向保鲜惯例）——豁免是债，不是资产。
const sizeguardMaxLines = 1200

var sizeguardWhitelist = map[string]string{
	// 当前零豁免。
}

func TestSizeGuardFileBudget(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd", "e2e"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			base := filepath.Base(rel)
			if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".pb.go") || base == "wire_gen.go" {
				return nil
			}
			if _, exempt := sizeguardWhitelist[rel]; exempt {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("read %s: %v", rel, rerr)
				return nil
			}
			if n := len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")); n > sizeguardMaxLines {
				t.Errorf("%s is %d lines (budget %d) — split the file or register a whitelist entry with rationale", rel, n, sizeguardMaxLines)
			}
			return nil
		})
	}
	// 白名单双向保鲜：条目失效（文件消失）即红；降回线下也红（还债即销账）。
	for path := range sizeguardWhitelist {
		full := filepath.Join(root, filepath.FromSlash(path))
		data, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("sizeguard whitelist entry %s no longer exists", path)
			continue
		}
		if n := len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")); n <= sizeguardMaxLines {
			t.Errorf("sizeguard whitelist entry %s is %d lines (<= budget) — remove the stale exemption", path, n)
		}
	}
}
