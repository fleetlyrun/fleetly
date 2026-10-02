package dockerbuild

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestEncodeRegistryAuth 钉推送凭证编码（X-Registry-Auth = base64 JSON；
// daemon 认证面）。
func TestEncodeRegistryAuth(t *testing.T) {
	enc, err := encodeRegistryAuth(capability.RegistryCredential{
		Server: "10.124.0.3:5000", Username: "fleetly", Secret: "pw",
	})
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, map[string]string{
		"username": "fleetly", "password": "pw", "serveraddress": "10.124.0.3:5000",
	}, got)
}

// TestDigestFromAux 钉推送流 aux 的 digest 提取（非 digest 载荷容忍为空）。
func TestDigestFromAux(t *testing.T) {
	aux := json.RawMessage(`{"Tag":"r3","Digest":"sha256:abc"}`)
	assert.Equal(t, "sha256:abc", digestFromAux(&aux))
	other := json.RawMessage(`{"id":"sha256:config"}`)
	assert.Empty(t, digestFromAux(&other), "non-digest aux payloads must be ignored")
	assert.Empty(t, digestFromAux(nil))
	bad := json.RawMessage(`{`)
	assert.Empty(t, digestFromAux(&bad), "malformed aux must not fail the push scan")
}

// TestTargetRepoPrefix 钉 RepoDigests 匹配前缀（host:port 与 tag 冒号
// 区分）。
func TestTargetRepoPrefix(t *testing.T) {
	assert.Equal(t, "10.124.0.3:5000/app", targetRepoPrefix("10.124.0.3:5000/app:r3"))
	assert.Equal(t, "reg.example.com/team/app", targetRepoPrefix("reg.example.com/team/app:v1"))
	assert.Equal(t, "localhost/app", targetRepoPrefix("localhost/app"))
}
