package guards

// 双代收口守卫（ADR-0048 验收锚；AGENTS 纪律：可静态执法承诺同批开守卫
// 任务）。两条不变式：
//
//  1. App 域载体收口只有期望集一条通道（Ensure"不含即移除"）——engine
//     对 runtime.Remove 的调用面收口在四个整域拆除点（App/Database/Task
//     删除 + Task 终态清扫），全部是域级 ns 拆除。新的 Remove 调用点 =
//     绕过期望集的第二拆除通道（蓝绿切换/收口/supersede 收口若走它，
//     "期望集不含即移除"的单机制裁决即被架空——孤儿清扫零新增承诺的
//     静态面）。
//  2. 代次名/代次 ID 公式单源（genScopedWorkloadID（engine）+ swarm 载体
//     名公式的 scoped 分支）——散落拼接一旦漂移即静默失配（WorkloadID
//     同款纪律的蓝绿对应物）。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// removeCallAllowlist 是 engine 内 runtime.Remove 的合法调用面（域级拆除）。
var removeCallAllowlist = map[string]string{
	"internal/engine/teardown.go": "DeleteApp 整域拆除（App 删除动词）",
	"internal/engine/database.go": "Database 拆除（ADR-0029 域删除动词）",
	"internal/engine/task.go":     "DeleteTask 整域拆除（Task 域删除动词）",
	"internal/engine/hygiene.go":  "Task 终态载体清扫（janitor 面）",
	"internal/engine/browse.go":   "Browse 会话回收（ADR-0051：per-会话独立命名空间即独立收敛单元——Remove 拆单会话域，不经 App 期望集）",
}

var runtimeRemoveRe = regexp.MustCompile(`runtime\.Remove\(`)

func TestBlueGreenCollectionUsesExpectationSetsOnly(t *testing.T) {
	root := repoRoot(t)
	engineDir := filepath.Join(root, "internal", "engine")
	err := filepath.WalkDir(engineDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Errorf("read %s: %v", rel, rerr)
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if runtimeRemoveRe.MatchString(line) {
				if _, ok := removeCallAllowlist[rel]; !ok {
					t.Errorf("%s:%d calls runtime.Remove outside the domain-teardown allowlist — "+
						"app-domain carrier collection must go through Ensure expectation sets (ADR-0048 decision 1/2: \"期望集不含即移除\" is the single collection mechanism; a second removal channel would void the zero-orphan promise)", rel, i+1)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 白名单双向保鲜：条目文件消失即红（惯例）。
	for rel := range removeCallAllowlist {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("remove-call allowlist entry %s no longer exists", rel)
		}
	}
}

// genSuffixRe 匹配手搓的代次后缀拼接形态（"-g" 字面量或 Sprintf 的 g%d
// 转换）——公式单源（engine.genScopedWorkloadID 与 swarm 载体名公式的
// scoped 分支）之外命中即红。
var genSuffixRe = regexp.MustCompile(`"-g|%s-g|-g"|g%d`)

var genNameAllowlist = map[string]string{
	"internal/engine/bluegreen.go":                                   "genScopedWorkloadID 铸名公式单源（平台 Workload ID 面）",
	"internal/providers/swarm/translate.go":                          "workloadServiceName 的 scoped 分支（Provider 私有载体名公式）",
	"internal/state/migrations/00024_deployment_from_generation.sql": "迁移列名（非拼接）",
	"internal/guards/bluegreen_test.go":                              "本守卫的模式定义",
}

func TestBlueGreenGenerationNamingSingleSource(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd", "e2e"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			base := filepath.Base(path)
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(base, "_test.go") ||
				strings.HasSuffix(base, ".pb.go") || base == "wire_gen.go" {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, ok := genNameAllowlist[rel]; ok {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("read %s: %v", rel, rerr)
				return nil
			}
			for i, line := range strings.Split(string(data), "\n") {
				if genSuffixRe.MatchString(line) {
					t.Errorf("%s:%d hand-rolls a generation suffix — use engine.genScopedWorkloadID (workload ID face) or let the swarm name formula derive it from GenerationScoped (carrier name face); scattered copies drift silently (ADR-0048 decision 1.4)", rel, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for rel := range genNameAllowlist {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("generation-name allowlist entry %s no longer exists", rel)
		}
	}
}
