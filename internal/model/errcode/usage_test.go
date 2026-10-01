package errcode

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 链 C：usage 反扫——每个在册错误码必须在生产代码出现为带引号字面量
// ≥1 处（码只以字面量进入 apperr.New 调用面）。零引用 = 预留浪费或拼写
// 漂移，即红；确属预留的码进豁免清单（必须带理由，且必须仍在册——注销
// 豁免码同样红，双向保鲜）。
var codeExemptions = map[string]string{
	// 示例形态（勿仿）："E_RESERVED": "reserved for X batch (ADR-NNNN); do not reuse",
	//
	// E_ALREADY_EXISTS 曾随批 0 Q-12 退休（producer 退役进豁免）；2026-10-01
	// 批 0 复核恢复唯一约束违例的专门映射（state.ErrAlreadyExists → 本码，
	// REST 409 回归修复），生产者回归即摘除豁免——豁免清单只对"确属预留"
	// 的码保鲜。
}

func TestRegistryCodesReferencedInProduction(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	// internal/model/errcode/usage_test.go -> repo 根（上溯 4 层）。
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))

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
		// 测试与生成物不算 usage；注册表自身目录（codes.go 字面量）排除。
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".pb.go") ||
			strings.HasSuffix(rel, ".pb.gw.go") || strings.HasSuffix(rel, "wire_gen.go") ||
			strings.HasPrefix(rel, "internal/model/errcode/") {
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
		if reason, exempt := codeExemptions[id]; exempt {
			if reason == "" {
				t.Errorf("exemption %s must carry a reason", id)
			}
			continue
		}
		if hits[id] == 0 {
			t.Errorf("errcode %s registered but never referenced in production code (add usage or an exemption with reason)", id)
		}
	}
	// 双向保鲜：豁免项必须仍在册。
	for id := range codeExemptions {
		if _, ok := Get(id); !ok {
			t.Errorf("exemption %s is no longer registered; remove the exemption entry", id)
		}
	}
}
