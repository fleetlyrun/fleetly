package builders

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEncodeRegistryAuth 已随单源收口迁至
// internal/capability/registryauth_test.go（X-Registry-Auth 编码形态的
// 单点锚定；builders 侧经 push_seam_test.go 的凭证透传用例继续覆盖）。

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
