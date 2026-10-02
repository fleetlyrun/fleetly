package zot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestCredentialPersistRoundTrip 钉附录 B.3：同 dataRoot 二次构造同密码
// （幂等持久）；文件权限 0o600；缺 dataRoot 拒绝。
func TestCredentialPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	p2, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	assert.Equal(t, p1.cred.Secret, p2.cred.Secret, "credential must persist across constructions")

	info, err := os.Stat(filepath.Join(dir, "keys", credentialFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // POSIX 权限位（Windows 无此语义）
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "credential file must be owner-only")
	}

	_, err = New("10.0.0.1:5000", "")
	require.Error(t, err, "empty data root must be rejected")

	// 损坏文件（坏 JSON/空字段）拒绝而非静默再生成——轮换是显式动作。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keys", credentialFile), []byte("{"), 0o600))
	_, err = New("10.0.0.1:5000", dir)
	require.Error(t, err, "malformed credential must fail loudly")
}

// TestEndpointCarriesPlatformCredential 钉附录 B.3：端点携带平台凭证
// （推送/拉取三面同源的真源形态）。
func TestEndpointCarriesPlatformCredential(t *testing.T) {
	p, err := New("10.124.0.3:5000", t.TempDir())
	require.NoError(t, err)
	ep, err := p.Endpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "10.124.0.3:5000", ep.Addr)
	assert.Equal(t, credentialUser, ep.Cred.Username)
	assert.Equal(t, "10.124.0.3:5000", ep.Cred.Server)
	assert.NotEmpty(t, ep.Cred.Secret)
}

// TestManagedWorkloadsShape 钉附录 B.1：受管形态钉版（镜像/发布 5000/
// 数据卷/单副本）+ 系统域隔离。
func TestManagedWorkloadsShape(t *testing.T) {
	p, err := New("10.124.0.3:5000", t.TempDir())
	require.NoError(t, err)

	assert.Equal(t, capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}, p.ManagedNamespace())

	ws := p.ManagedWorkloads()
	require.Len(t, ws, 1)
	w := ws[0]
	assert.Equal(t, "fleetly-registry-zot", w.ID)
	assert.Equal(t, Image, w.Image, "managed image must be version-pinned")
	assert.Equal(t, int64(1), w.Replicas)
	require.Len(t, w.Publish, 1)
	assert.Equal(t, int32(5000), w.Publish[0].PublishedPort)
	assert.Equal(t, int32(5000), w.Publish[0].TargetPort)
	require.Len(t, w.Volumes, 1)
	assert.Equal(t, "fleetly-registry-zot", w.Volumes[0].VolumeID)
	assert.Equal(t, storageRoot, w.Volumes[0].Target)
	assert.Empty(t, w.Networks, "managed registry must not attach project networks (B.1)")
	require.Len(t, w.Command, 3)
	// 入口是镜像 ENTRYPOINT 的绝对路径形态（zot-minimal 二进制名带平台
	// 后缀，PATH 查找 "zot" 恒败——staging 真机实证，2026-10-02）。
	assert.Equal(t, "/usr/local/bin/zot-linux-amd64-minimal", w.Command[0])
	assert.True(t, strings.HasSuffix(w.Command[2], configFile), "serve must point at the material-injected config")
}

// TestManagedMaterials 钉附录 B.1/B.3：材料含 config+htpasswd；config 形态
// （存储根/监听/htpasswd 引用）正确、htpasswd bcrypt 可验、材料幂等
// （同实例两次同字节）。
func TestManagedMaterials(t *testing.T) {
	p, err := New("10.124.0.3:5000", t.TempDir())
	require.NoError(t, err)

	m1 := p.ManagedMaterials()
	m2 := p.ManagedMaterials()
	require.Len(t, m1.SecretFiles, 2)
	assert.Equal(t, m1.SecretFiles[configFile], m2.SecretFiles[configFile], "materials must be a stable pure function")

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(m1.SecretFiles[configFile], &cfg))
	httpc := cfg["http"].(map[string]any)
	assert.Equal(t, "0.0.0.0", httpc["address"])
	assert.Equal(t, "5000", httpc["port"])
	auth := httpc["auth"].(map[string]any)["htpasswd"].(map[string]any)
	assert.NotContains(t, auth, "user", "zot v2.1.21 htpasswd block rejects extra keys (username lives in the file)")
	assert.Equal(t, "/run/secrets/"+htpasswdFile, auth["path"])
	storage := cfg["storage"].(map[string]any)
	assert.Equal(t, storageRoot, storage["rootDirectory"])

	line := string(m1.SecretFiles[htpasswdFile])
	parts := strings.SplitN(line, ":", 2)
	require.Len(t, parts, 2)
	assert.Equal(t, credentialUser, parts[0])
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte(p.cred.Secret)),
		"htpasswd must be a verifiable bcrypt hash of the platform password")
}

// TestDescribeManagedPinned 钉自描述面（能力发现端点消费）。
func TestDescribeManagedPinned(t *testing.T) {
	p, err := New("10.0.0.1:5000", t.TempDir())
	require.NoError(t, err)
	d := p.Describe()
	assert.Equal(t, "zot", d.Name)
	assert.Equal(t, capability.KindRegistry, d.Capability)
	assert.True(t, d.Managed)
	assert.NotEmpty(t, d.Notes)
}
