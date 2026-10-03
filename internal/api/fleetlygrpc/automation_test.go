package fleetlygrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestListLimit（N1 C21）：List limit 归一——缺省/显式/钳上界三形态。
// n<=0 回落缺省 50；显式小值直通；n>200 钳到上界 200（不回落缺省——调用方
// 明确要大页时静默砍到 50 是对显式意图的覆盖）。
func TestListLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int32
		want int
	}{
		{"zero falls back to default", 0, 50},
		{"negative falls back to default", -7, 50},
		{"small explicit value passes through", 1, 1},
		{"default-ish explicit value passes through", 50, 50},
		{"just under the cap passes through", 199, 199},
		{"cap passes through", 200, 200},
		{"just over the cap clamps", 201, 200},
		{"large request clamps", 500, 200},
		{"int32 max clamps", 2147483647, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, listLimit(tc.in))
		})
	}
}
