package guards

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// irOrchestratorTokens 是 IR（proto/fleetly/spec/**）禁用的编排器语义词
// （架构 §4：IR 不含编排器 label、载体命名、约束语法、namespace、探针的
// 编排器方言；平台自有实体名如 Task/TaskSpec 不在此列）。
var irOrchestratorTokens = []string{
	"service", "pod", "label", "selector", "unit",
	"affinity", "constraint", "namespace", "ingress",
	"toleration", "taint", "replicaset",
}

var irTokenRe = regexp.MustCompile(`(?i)\b(` + strings.Join(irOrchestratorTokens, "|") + `)s?\b`)

// TestSpecIRHasNoOrchestratorVocabulary：Spec IR 的 proto 源零编排器词汇
// （注释亦不使用——IR 是运行时中立的唯一边界，ADR-0001）。
func TestSpecIRHasNoOrchestratorVocabulary(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	err := filepath.WalkDir(filepath.Join(root, "proto", "fleetly", "spec"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		content := readFileLF(t, rel)
		if m := irTokenRe.FindAllString(content, -1); len(m) > 0 {
			hits = append(hits, rel+": "+strings.Join(unique(m), ", "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("%s — orchestrator vocabulary is forbidden in the Spec IR (architecture §4 / ADR-0001)", h)
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
