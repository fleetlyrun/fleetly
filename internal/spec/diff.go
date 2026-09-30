package spec

import (
	"encoding/json"
	"fmt"
	"sort"
)

// DiffEntry 是一条字段级差异（path 是点分字段路径，如
// "processes[0].image"；old/new 是 JSON 标量形态，删除侧为 nil）。
type DiffEntry struct {
	Path string
	Old  any
	New  any
}

// String 渲染单条差异（人读面；CLI/Console 共用）。
func (d DiffEntry) String() string {
	return fmt.Sprintf("%s: %v -> %v", d.Path, jsonOrNull(d.Old), jsonOrNull(d.New))
}

// Diff 对比两份 Revision 冻结体（protojson 规范序列化输入）：递归展开
// 对象/数组，输出扁平字段差异列表（路径字典序——确定性，golden 钉死）。
func Diff(oldSpec, newSpec []byte) ([]DiffEntry, error) {
	var oldTree, newTree any
	if len(oldSpec) > 0 {
		if err := json.Unmarshal(oldSpec, &oldTree); err != nil {
			return nil, fmt.Errorf("diff: parse old spec: %w", err)
		}
	}
	if len(newSpec) > 0 {
		if err := json.Unmarshal(newSpec, &newTree); err != nil {
			return nil, fmt.Errorf("diff: parse new spec: %w", err)
		}
	}
	var out []DiffEntry
	walk("", oldTree, newTree, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func walk(path string, oldV, newV any, out *[]DiffEntry) {
	switch o := oldV.(type) {
	case map[string]any:
		n, ok := newV.(map[string]any)
		if !ok {
			*out = append(*out, DiffEntry{Path: pathOrRoot(path), Old: oldV, New: newV})
			return
		}
		keys := map[string]bool{}
		for k := range o {
			keys[k] = true
		}
		for k := range n {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			child := joinPath(path, k)
			ov, hasOld := o[k]
			nv, hasNew := n[k]
			switch {
			case hasOld && hasNew:
				walk(child, ov, nv, out)
			case hasOld:
				*out = append(*out, DiffEntry{Path: child, Old: ov, New: nil})
			default:
				*out = append(*out, DiffEntry{Path: child, Old: nil, New: nv})
			}
		}
	case []any:
		n, ok := newV.([]any)
		if !ok {
			*out = append(*out, DiffEntry{Path: pathOrRoot(path), Old: oldV, New: newV})
			return
		}
		max := len(o)
		if len(n) > max {
			max = len(n)
		}
		for i := 0; i < max; i++ {
			child := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i < len(o) && i < len(n):
				walk(child, o[i], n[i], out)
			case i < len(o):
				*out = append(*out, DiffEntry{Path: child, Old: o[i], New: nil})
			default:
				*out = append(*out, DiffEntry{Path: child, Old: nil, New: n[i]})
			}
		}
	default:
		if !equalJSON(oldV, newV) {
			*out = append(*out, DiffEntry{Path: pathOrRoot(path), Old: oldV, New: newV})
		}
	}
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func jsonOrNull(v any) string {
	if v == nil {
		return "(none)"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
