package guards

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// irOrchestratorTokens 是 IR（proto/fleetly/spec/** 与 internal/model/**
// 的 proto 契约形态——ADR-0001 原文即 spec+model）禁用的编排器语义词
// （架构 §4：IR 不含编排器 label、载体命名、约束语法、namespace、探针的
// 编排器方言；平台自有实体名如 Task/TaskSpec 不在此列）。
var irOrchestratorTokens = []string{
	"service", "pod", "label", "selector", "unit",
	"affinity", "constraint", "namespace", "ingress",
	"toleration", "taint", "replicaset",
}

var irTokenRe = regexp.MustCompile(`(?i)\b(` + strings.Join(irOrchestratorTokens, "|") + `)s?\b`)

// irSubtrees 是 IR 边界子树及其扫描后缀：spec 的 proto 源 + model 的
// Go 注册表（errcode 错误文案与 eventcode 事件名/摘要同属运行时中立面；
// N0 修复批 C4 扩面、N0.1 P2-5 实装——此前 model 子树按 .proto 过滤，
// 而该目录无 proto 文件，扩面一直空转）。
var irSubtrees = []struct {
	dir string
	ext string
}{
	{filepath.Join("proto", "fleetly", "spec"), ".proto"},
	{filepath.Join("internal", "model"), ".go"},
}

// TestSpecIRHasNoOrchestratorVocabulary：Spec IR 的 proto 源与 model 的
// 错误/事件文案零编排器词汇（注释亦不使用——IR 是运行时中立的唯一边界，
// ADR-0001）。
func TestSpecIRHasNoOrchestratorVocabulary(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	for _, sub := range irSubtrees {
		scanned := 0
		err := filepath.WalkDir(filepath.Join(root, sub.dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, sub.ext) {
				return nil
			}
			scanned++
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
		// 非空断言：子树扫描到 0 个文件说明目录/后缀失配（扩面空转的
		// 复发形态——P2-5 的教训）。
		if scanned == 0 {
			t.Errorf("%s matched no *%s files — subtree scan is vacuous", sub.dir, sub.ext)
		}
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
