package material

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 信封往返 + 指纹稳定性 + KEK 持久性（同数据根二次加载可解）。
func TestSealOpenRoundtrip(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadCipher(dir)
	require.NoError(t, err)

	secret := []byte("hunter2-but-longer-1234")
	ct, err := c1.Seal(secret)
	require.NoError(t, err)
	assert.NotContains(t, string(ct), string(secret), "ciphertext must not contain plaintext")

	// 同 KEK 重开。
	c2, err := LoadCipher(dir)
	require.NoError(t, err)
	pt, err := c2.Open(ct)
	require.NoError(t, err)
	assert.Equal(t, secret, pt)

	// 不同数据根（不同 KEK）解不开。
	c3, err := LoadCipher(t.TempDir())
	require.NoError(t, err)
	_, err = c3.Open(ct)
	assert.Error(t, err, "another master key must not decrypt")
}

func TestFingerprintStable(t *testing.T) {
	assert.Equal(t, Fingerprint([]byte("v")), Fingerprint([]byte("v")))
	assert.NotEqual(t, Fingerprint([]byte("v")), Fingerprint([]byte("w")))
	assert.Len(t, Fingerprint([]byte("v")), 16)
}
