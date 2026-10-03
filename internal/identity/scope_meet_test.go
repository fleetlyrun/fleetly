package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func sc(resource, action string) Scope { return Scope{Resource: resource, Action: action} }

// TestMeet 是 ADR-0038 求交算子的语义矩阵：min(声明, creator 当前)。
func TestMeet(t *testing.T) {
	star := sc("*", "*")
	cases := []struct {
		name      string
		declared  []Scope
		grants    []Scope
		want      []string
		wantEmpty bool
	}{
		{
			name:     "creator wildcard keeps declaration intact",
			declared: []Scope{sc("apps", "write"), sc("events", "read")},
			grants:   []Scope{star},
			want:     []string{"apps:write", "events:read"},
		},
		{
			name:     "declared wildcard collapses to creator grants",
			declared: []Scope{star},
			grants:   []Scope{sc("apps", "write")},
			want:     []string{"apps:write"},
		},
		{
			name:     "declared wildcard under creator wildcard stays wildcard",
			declared: []Scope{star},
			grants:   []Scope{star},
			want:     []string{"*"},
		},
		{
			name:     "ladder demotion admin onto write",
			declared: []Scope{sc("apps", "admin")},
			grants:   []Scope{sc("apps", "write")},
			want:     []string{"apps:write"},
		},
		{
			name:     "ladder demotion write onto read",
			declared: []Scope{sc("apps", "write")},
			grants:   []Scope{sc("apps", "read")},
			want:     []string{"apps:read"},
		},
		{
			name:     "same rank passes through",
			declared: []Scope{sc("apps", "write")},
			grants:   []Scope{sc("apps", "write")},
			want:     []string{"apps:write"},
		},
		{
			name:     "unrelated resources drop out, covered ones survive",
			declared: []Scope{sc("apps", "write"), sc("events", "read"), sc("nodes", "read")},
			grants:   []Scope{sc("apps", "read"), sc("events", "read")},
			want:     []string{"apps:read", "events:read"},
		},
		{
			name:      "disjoint resources meet to empty",
			declared:  []Scope{sc("deployments", "write")},
			grants:    []Scope{sc("events", "read")},
			wantEmpty: true,
		},
		{
			name:      "rank too low meets to empty",
			declared:  []Scope{sc("apps", "write")},
			grants:    []Scope{sc("nodes", "write")},
			wantEmpty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Meet(tc.declared, tc.grants)
			if tc.wantEmpty {
				assert.Nil(t, got)
				return
			}
			assert.ElementsMatch(t, tc.want, ScopeStrings(got))
		})
	}
}

// TestMeetStability：求交是纯函数——重复调用与输入顺序无关（P7 digest
// 确定性纪律同源：map/切片遍历序不进入结果形态）。
func TestMeetStability(t *testing.T) {
	declared := []Scope{sc("apps", "write"), sc("events", "read"), sc("routes", "admin")}
	grants := []Scope{sc("apps", "read"), sc("events", "read"), sc("routes", "write"), sc("nodes", "read")}
	first := ScopeStrings(Meet(declared, grants))
	for i := 0; i < 100; i++ {
		assert.Equal(t, first, ScopeStrings(Meet(declared, grants)))
	}
}
