package cmd

// 按名 create-or-reuse 的分页查重回归（P0-7.5）：List 首页（服务端缺省
// 50）线性找名在实体超过 50 时假阴性——误判"不存在"后重复 Create 撞
// E_ALREADY_EXISTS。真实可达面是 projects（平台级无配额，项目数随使用
// 单调累积；apps 有 50/项目配额兜着，但配额与页大小同值 = 边界耦合，
// 收敛单源对配额上调不敏感）。此处经 create-from-dokploy 端到端钉死：
// 第 55 个项目必须被 reused 而不是撞唯一约束。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureProjectByNameBeyondFirstPage(t *testing.T) {
	_ = newGoldenHarness(t)

	// 播 55 个项目（ListProjects 缺省页 50——第 51+ 落首页外）。
	for i := 1; i <= 55; i++ {
		name := fmt.Sprintf("pg-%02d", i)
		code, _, stderr := runCLI(t, "projects", "create", name)
		if code != 0 || stderr != "" {
			t.Fatalf("seed project %s: code=%d stderr=%q", name, code, stderr)
		}
	}

	// 单 App 迁移计划，目标项目取第 55 个（首页 50 之外）：修复前
	// ensureProjectByName 查重假阴性 → 重复 CreateProject →
	// E_ALREADY_EXISTS 非零退出；修复后游标收敛命中 → project reused。
	export := `{"applications":[{"applicationId":"app-1","name":"landing",` +
		`"buildType":"dockerimage","dockerImage":"nginx:1.27","env":""}]}`
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, []byte(export), 0o600); err != nil { //nolint:gosec // 测试夹具文件
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "create-from-dokploy", "--file", path, "--project", "pg-55")
	if code != 0 {
		t.Fatalf("create-from-dokploy beyond first page: code=%d stderr=%q (want reused)", code, stderr)
	}
	if !strings.Contains(out, "project pg-55 reused") {
		t.Fatalf("expected project pg-55 to be reused, got:\n%s", out)
	}
}
