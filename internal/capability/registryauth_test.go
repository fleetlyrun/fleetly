package capability

// registry 身份规则表测（单源收口后的锚）：host 提取（host:port/localhost/
// 官方命名空间/默认归一）与 X-Registry-Auth 编码形态。

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageRegistryHost(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"nginx:1.27", "docker.io"},                                                  // 无首段 = 默认仓库官方命名空间
		{"library/nginx:1.27", "docker.io"},                                          // 命名空间段不是主机
		{"ghcr.io/acme/web:1", "ghcr.io"},                                            // 点即主机
		{"registry.example.com:5000/team/app@sha256:x", "registry.example.com:5000"}, // host:port
		{"localhost:5000/app", "localhost:5000"},                                     // localhost 系
		{"10.124.0.3:5000/app:v1", "10.124.0.3:5000"},                                // IP:port
		{"team/app:v1", "docker.io"},                                                 // 组织命名空间不是主机
	}
	for _, c := range cases {
		assert.Equal(t, c.want, ImageRegistryHost(c.ref), "ref=%s", c.ref)
	}
}

func TestEncodeRegistryAuth(t *testing.T) {
	enc, err := EncodeRegistryAuth(RegistryCredential{
		Server: "10.124.0.3:5000", Username: "fleetly", Secret: "pw",
	})
	require.NoError(t, err)
	raw, derr := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, derr)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, map[string]string{
		"username": "fleetly", "password": "pw", "serveraddress": "10.124.0.3:5000",
	}, got, "X-Registry-Auth payload shape is frozen (daemon auth face)")
}
