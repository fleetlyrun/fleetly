package finding

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 链 C：usage 反扫——每个在册诊断条目必须在生产代码出现为带引号字面量
// ≥1 处（条目只以字面量进入 diagnosis 规则的实例化面）。零引用 = 预留
// 浪费或拼写漂移，即红；确属预留的条目进豁免清单（必须带理由，且必须
// 仍在册——注销豁免条目同样红，双向保鲜）。照抄 errcode usage_test 形态。
var findingExemptions = map[string]string{
	// 示例形态（勿仿）："F_RESERVED": "reserved for X batch (ADR-NNNN); do not reuse",
}

func TestRegistryFindingsReferencedInProduction(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	// internal/domain/shared/model/finding/usage_test.go -> repo 根（上溯 6 层）。
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))))

	hits := map[string]int{}
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		// 测试与生成物不算 usage；注册表自身目录（findings.go 字面量）排除。
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".pb.go") ||
			strings.HasSuffix(rel, ".pb.gw.go") || strings.HasSuffix(rel, "wire_gen.go") ||
			strings.HasPrefix(rel, "internal/domain/shared/model/finding/") {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
		if readErr != nil {
			return readErr
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n") // CRLF 归一
		for _, id := range IDs() {
			if strings.Contains(content, `"`+id+`"`) {
				hits[id]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range IDs() {
		if reason, exempt := findingExemptions[id]; exempt {
			if reason == "" {
				t.Errorf("exemption %s must carry a reason", id)
			}
			continue
		}
		if hits[id] == 0 {
			t.Errorf("finding %s registered but never referenced in production code (add usage or an exemption with reason)", id)
		}
	}
	// 双向保鲜：豁免项必须仍在册。
	for id := range findingExemptions {
		if _, ok := Get(id); !ok {
			t.Errorf("exemption %s is no longer registered; remove the exemption entry", id)
		}
	}
}
