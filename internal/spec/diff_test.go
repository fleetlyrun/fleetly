package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Revision diff：字段级扁平差异（路径字典序确定；增/删/改三态）。
func TestDiff(t *testing.T) {
	old := []byte(`{"schema_version":1,"app":{"id":"a","project":"p"},
		"source":{"image":{"ref":"nginx:1.26"}},
		"processes":[{"name":"web","image":"nginx:1.26","replicas":1},{"name":"worker","image":"busybox"}]}`)
	new := []byte(`{"schema_version":1,"app":{"id":"a","project":"p"},
		"source":{"image":{"ref":"nginx:1.27"}},
		"processes":[{"name":"web","image":"nginx:1.26","replicas":2}]}`)

	entries, err := Diff(old, new)
	require.NoError(t, err)

	byPath := map[string]DiffEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	assert.Contains(t, byPath, "processes[0].replicas")
	assert.Equal(t, float64(1), byPath["processes[0].replicas"].Old)
	assert.Equal(t, float64(2), byPath["processes[0].replicas"].New)
	assert.Contains(t, byPath, "processes[1]")
	assert.Nil(t, byPath["processes[1]"].New, "removed process shows as deletion")
	assert.Contains(t, byPath, "source.image.ref")
	assert.Equal(t, "nginx:1.27", byPath["source.image.ref"].New)

	// 排序确定性（golden 面基础）。
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	assert.IsNonDecreasing(t, paths)
}

func TestDiffIdentical(t *testing.T) {
	spec := []byte(`{"a":1,"b":[1,2,{"c":"x"}]}`)
	entries, err := Diff(spec, spec)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDiffBadJSON(t *testing.T) {
	_, err := Diff([]byte(`{`), []byte(`{}`))
	assert.ErrorContains(t, err, "parse old spec")
}
