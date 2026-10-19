package node_test

// 节点个体 relay 凭证材质（H10 §4.3）：前缀可扫描、32B 熵、sha256 存储
// 键——与 domain/identity Token 材质同款纪律的机械锚。

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
)

func TestNewRelayCredentialShape(t *testing.T) {
	mat, err := node.NewRelayCredential()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(mat.Secret, node.RelayCredentialPrefix),
		"material must carry the scannable fltrelay_ prefix")

	// 随机段是 32B 的 base64url（url-safe 字母表，无填充）。
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(mat.Secret, node.RelayCredentialPrefix))
	require.NoError(t, err)
	assert.Len(t, raw, 32, "256-bit entropy")

	// hash 是 64 hex，与 HashRelayCredential 同源。
	assert.Len(t, mat.SHA256, 64)
	assert.Regexp(t, `^[0-9a-f]{64}$`, mat.SHA256)
	assert.Equal(t, node.HashRelayCredential(mat.Secret), mat.SHA256)

	// 审计/日志面只带前缀（前 12 字符）。
	assert.Len(t, mat.Prefix, 12)
	assert.True(t, strings.HasPrefix(mat.Secret, mat.Prefix))

	// 每次生成独立（crypto/rand）。
	other, err := node.NewRelayCredential()
	require.NoError(t, err)
	assert.NotEqual(t, mat.Secret, other.Secret)
	assert.NotEqual(t, mat.SHA256, other.SHA256)
}
