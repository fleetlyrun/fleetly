package material

// SealedValue 配对纪律测试（2026-10-03 架构评审候选 7）：同源配对由构造
// 保证（密文与指纹派生自同一次 Produce 的同一明文）+ 字符串面脱敏。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProducePairsCiphertextWithSamePlaintextFingerprint：同源配对——
// 指纹 = 明文指纹、密文解回同一明文（此前四处手搓两连调，配对一致性
// 只靠相邻调用自觉）。
func TestProducePairsCiphertextWithSamePlaintextFingerprint(t *testing.T) {
	c, err := LoadCipher(t.TempDir())
	require.NoError(t, err)

	v, err := c.Produce([]byte("wombat-quarrel-4f7d"))
	require.NoError(t, err)
	assert.Equal(t, Fingerprint([]byte("wombat-quarrel-4f7d")), v.Fingerprint,
		"fingerprint derives from the sealed plaintext")

	plain, oerr := c.Open(v.Ciphertext)
	require.NoError(t, oerr)
	assert.Equal(t, "wombat-quarrel-4f7d", string(plain),
		"ciphertext opens back to the same plaintext")
}

// TestSealedValueStringIsRedacted：字符串面只露指纹——密文与明文不进
// 日志/错误文本形态。
func TestSealedValueStringIsRedacted(t *testing.T) {
	v := SealedValue{Ciphertext: []byte("AGE ENCRYPTED BODY"), Fingerprint: "deadbeefcafe1234"}
	s := v.String()
	assert.Contains(t, s, "deadbeefcafe1234")
	assert.NotContains(t, s, "AGE ENCRYPTED BODY", "ciphertext stays out of string faces")
}
