package material

// 纯密码学面测试（P1a-3a 拆分后）：Seal/Open 往返、指纹稳定性、现役唯一
// Seal 与退役兜底。文件面（装载/首启生成/退役装载/权限落盘）测试在
// internal/infrastructure/keyring。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"filippo.io/age"
)

// newTestCipher 生成单 key 形态加密面（无退役；测试夹具）。
func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return NewCipher(id, nil)
}

// 信封往返 + 跨 KEK 不可解。
func TestSealOpenRoundtrip(t *testing.T) {
	c1 := newTestCipher(t)
	secret := []byte("hunter2-but-longer-1234")
	ct, err := c1.Seal(secret)
	require.NoError(t, err)
	assert.NotContains(t, string(ct), string(secret), "ciphertext must not contain plaintext")

	// 同 KEK 重开。
	pt, err := c1.Open(ct)
	require.NoError(t, err)
	assert.Equal(t, secret, pt)

	// 不同 KEK（另把现役）解不开。
	c3 := newTestCipher(t)
	_, err = c3.Open(ct)
	assert.Error(t, err, "another master key must not decrypt")
}

func TestFingerprintStable(t *testing.T) {
	assert.Equal(t, Fingerprint([]byte("v")), Fingerprint([]byte("v")))
	assert.NotEqual(t, Fingerprint([]byte("v")), Fingerprint([]byte("w")))
	assert.Len(t, Fingerprint([]byte("v")), 16)
}

// 轮换窗口密码学面：Seal 恒用现役，Open 现役优先、退役兜底；
// OpensWithActive 是重封幂等分类的判定轴。
func TestRetiredFallback(t *testing.T) {
	oldID, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	oldCipher := NewCipher(oldID, nil)

	// 旧 KEK 密文 + 新 KEK 密文各一份。
	oldCT, err := oldCipher.Seal([]byte("sealed-with-old"))
	require.NoError(t, err)

	newID, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	rotated := NewCipher(newID, []*age.X25519Identity{oldID})
	newCT, err := rotated.Seal([]byte("sealed-with-new"))
	require.NoError(t, err)

	// Seal 用现役：新密文现役可解、旧密文现役不可解（分类轴）。
	assert.True(t, rotated.OpensWithActive(newCT), "fresh seal must open with the active key")
	assert.False(t, rotated.OpensWithActive(oldCT), "old-sealed ciphertext must not classify as current")

	// Open 兜底：旧密文经退役 key 解开；新密文现役直解。
	pt, err := rotated.Open(oldCT)
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-with-old"), pt)
	pt, err = rotated.Open(newCT)
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-with-new"), pt)
}

// HasActive 报告现役在场（缺席由装载侧裁决）。
func TestHasActive(t *testing.T) {
	c := NewCipher(nil, nil)
	assert.False(t, c.HasActive())
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	assert.True(t, NewCipher(id, nil).HasActive())
}
