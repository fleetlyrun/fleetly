package engine

// P7 digest 确定性纪律的性质测试（docs/design/2026-10-03-optimization-proposals.md
// §P7）：canonical 指纹是"同输入同输出"的**测试钉住承诺**，不是假设——
// 稳定性（重复调用）与输入序无关性（map 构造序不进入指纹）两条性质各
// 自钉死。指纹漂移的实害=受管域无谓滚替（gen 推进）与材料重复下发。

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestFingerprintStability：重复调用 1000 次指纹稳定（managedFingerprint
// 的退化分支"unserializable-%p"会把每次调用变指纹——本性质同时钉住退
// 化分支不可达）。
func TestFingerprintStability(t *testing.T) {
	ws := []capability.Workload{
		{ID: "w1", Process: "web", Image: "nginx:1.27", Replicas: 2},
		{ID: "w2", Process: "store", Image: "redis:7.4"},
	}
	first := managedFingerprint(ws)
	for i := 0; i < 1000; i++ {
		assert.Equal(t, first, managedFingerprint(ws), "iteration %d", i)
	}

	m := capability.Materials{SecretFiles: map[string][]byte{
		"zot-config":   []byte(`{"http":{"port":"5000"}}`),
		"zot-htpasswd": []byte("fleetly:$2a$10$abc"),
	}}
	mFirst := materialsFingerprint(m)
	for i := 0; i < 1000; i++ {
		assert.Equal(t, mFirst, materialsFingerprint(m), "iteration %d", i)
	}
}

// TestFingerprintMapOrderIndependence：同内容、不同 map 构造序的输入指纹
// 一致——map 遍历序不得进入指纹（encoding/json 键排序契约的行为钉）。
// 每轮用不同插入序构造；Go map 遍历序本身随机化，遍历拷贝再构造等价输入。
func TestFingerprintMapOrderIndependence(t *testing.T) {
	base := map[string][]byte{
		"alpha": []byte("one"), "beta": []byte("two"), "gamma": []byte("three"),
		"delta": []byte("four"), "epsilon": []byte("five"),
	}
	first := materialsFingerprint(capability.Materials{SecretFiles: base})
	for i := 0; i < 200; i++ {
		shuffled := make(map[string][]byte, len(base))
		// Go map 遍历序逐轮随机（语言保证）——拷贝序即天然打乱序。
		for k, v := range base {
			shuffled[k] = v
		}
		assert.Equal(t, first,
			materialsFingerprint(capability.Materials{SecretFiles: shuffled}),
			"round %d: map construction order leaked into the fingerprint", i)
	}
}
