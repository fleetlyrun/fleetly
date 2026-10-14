package schema

// P7 digest 确定性纪律的性质测试（§P7）：CanonicalJSON 是 golden 漂移门
// 的底座——"同输入同输出"是测试钉住的承诺。稳定性与 map 构造序无关性
// 两条性质（条目集与 schema 树都是 map 构造面）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCanonicalJSONStability：重复渲染稳定。
func TestCanonicalJSONStability(t *testing.T) {
	doc := Build()
	first := doc.CanonicalJSON()
	for i := 0; i < 1000; i++ {
		assert.Equal(t, first, Build().CanonicalJSON(), "iteration %d", i)
	}
	assert.True(t, strings.HasSuffix(first, "\n"), "canonical form keeps the trailing newline")
}

// TestCanonicalJSONMapOrderIndependence：schema 树是 map 构造面——同内容
// 不同构造序的树渲染一致（map 遍历序随机化语言保证 + json 键排序契约）。
func TestCanonicalJSONMapOrderIndependence(t *testing.T) {
	base := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"name":  map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
		},
		"required": []any{"id"},
	}
	direct := Document{Entries: []Entry{{Name: "b", Kind: "event", Summary: "s", Schema: base}, {Name: "a", Kind: "event", Summary: "s", Schema: base}}}
	first := direct.CanonicalJSON()
	require.NotEmpty(t, first)

	// 200 轮逐轮随机序拷贝构造（嵌套 map 同样走遍历序随机化）。
	var copyTree func(m map[string]any) map[string]any
	copyTree = func(m map[string]any) map[string]any {
		out := make(map[string]any, len(m))
		for k, v := range m {
			if nested, ok := v.(map[string]any); ok {
				out[k] = copyTree(nested)
				continue
			}
			out[k] = v
		}
		return out
	}
	for i := 0; i < 200; i++ {
		shuffled := Document{Entries: []Entry{
			{Name: "b", Kind: "event", Summary: "s", Schema: copyTree(base)},
			{Name: "a", Kind: "event", Summary: "s", Schema: copyTree(base)},
		}}
		assert.Equal(t, first, shuffled.CanonicalJSON(),
			"round %d: map construction order leaked into the canonical form", i)
	}
}
