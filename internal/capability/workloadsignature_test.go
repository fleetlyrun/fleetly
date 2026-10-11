package capability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkloadSetSignatureFullSetStable：全集 canonical 签名的不变式锚。
// EgressNetworks 子测是 ensure memo 盲区的回归锚——2026-10-11 前手挑五字段
// 签名使 ADR-0052 投影期现值解析字段静默掉出跳过判定（runbook 记录·三十
// 七）；签名知识沉入 capability 后，全集 marshal 让任何新字段落类型即落
// 签名。
func TestWorkloadSetSignatureFullSetStable(t *testing.T) {
	base := []Workload{
		{ID: "w2", Image: "img:1", Env: map[string]string{"A": "1", "B": "2"}},
		{ID: "w1", Image: "img:1"},
	}

	reordered := []Workload{base[1], base[0]}
	assert.Equal(t, WorkloadSetSignature(base), WorkloadSetSignature(reordered),
		"ID 排序消除集合序，同集不同序同签名")

	envReordered := []Workload{base[0], base[1]}
	envReordered[0].Env = map[string]string{"B": "2", "A": "1"}
	assert.Equal(t, WorkloadSetSignature(base), WorkloadSetSignature(envReordered),
		"map 键序内建排序，env 迭代序无关")

	egressed := []Workload{base[0], base[1]}
	egressed[0].EgressNetworks = []string{"workers"}
	assert.NotEqual(t, WorkloadSetSignature(base), WorkloadSetSignature(egressed),
		"全集签名：EgressNetworks 变化必须变签名（重建 flag-day 击穿 memo 的回归锚）")

	addressed := []Workload{base[0], base[1]}
	addressed[0].Addressing = []Address{{Name: "pool"}}
	assert.NotEqual(t, WorkloadSetSignature(base), WorkloadSetSignature(addressed),
		"全集签名：Addressing 变化必须变签名")

	replica := []Workload{base[0], base[1]}
	replica[0].Replicas = 2
	require.NotEqual(t, WorkloadSetSignature(base), WorkloadSetSignature(replica),
		"全集签名：Replicas 变化必须变签名（既有子集语义保持）")

	assert.NotEmpty(t, WorkloadSetSignature(base))
	assert.Equal(t, WorkloadSetSignature(nil), WorkloadSetSignature([]Workload{}),
		"空集形态确定性（nil 与空集同签名）")
}
