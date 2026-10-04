package zot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

// TestHTPasswdPersistsAcrossConstructions 钉 E28：htpasswd 行持久化于
// keys/registry-htpasswd——双次 New 同行（重启不再新铸 = 受管载体指纹不
// 随进程重启漂移）；文件权限 0o600 同凭证口径；行可验当前密码。
func TestHTPasswdPersistsAcrossConstructions(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	p2, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)

	line1 := string(p1.ManagedMaterials().SecretFiles[htpasswdFile])
	line2 := string(p2.ManagedMaterials().SecretFiles[htpasswdFile])
	require.NotEmpty(t, line1)
	assert.Equal(t, line1, line2, "htpasswd line must persist across constructions (restart-stable carrier fingerprint)")

	path := filepath.Join(dir, "keys", htpasswdStoreFile)
	b, err := os.ReadFile(path) //nolint:gosec // 测试读数据根私有目录
	require.NoError(t, err)
	assert.Equal(t, line1, strings.TrimRight(string(b), "\r\n"), "persisted file must hold the served line")
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // POSIX 权限位（Windows 无此语义）
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "htpasswd file must be owner-only like the credential")
	}
}

// TestHTPasswdCorruptFailsLoudly 钉 E28：损坏/异主行拒绝构造（fail loud），
// 不静默再生成——静默会把凭证面故障伪装成正常重启。
func TestHTPasswdCorruptFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	_, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	path := filepath.Join(dir, "keys", htpasswdStoreFile)

	// 异主 bcrypt 哈希（对其他密码有效）不算损坏——那是轮换形态，由
	// TestHTPasswdFollowsCredentialRotation 钉自愈；此处只钉形态坏面
	//（无冒号结构/空文件/他人用户名的行都进不了验证面）。
	for name, content := range map[string]string{
		"garbage":    "not-a-bcrypt-line",
		"empty":      "",
		"other-user": "mallory:$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B0S4M0Zzq7JcXTYceHcYf0oEJ2HW",
	} {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		_, err := New("10.0.0.1:5000", dir)
		require.Error(t, err, "%s must fail loudly", name)
		assert.Contains(t, err.Error(), "malformed", "%s must be reported as malformed", name)
	}
}

// TestHTPasswdFollowsCredentialRotation 钉 E28 轮换自愈：凭证文件换新密码
// 后（轮换序 = 删 registry.json 重启的物化形态），htpasswd 旧行按
// mismatch 识别为轮换并重铸覆写——三面（zot/推送/拉取）同源自愈。
func TestHTPasswdFollowsCredentialRotation(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	oldLine := string(p1.ManagedMaterials().SecretFiles[htpasswdFile])

	// 物化轮换：新密码落凭证文件（tmp+rename 的并发形态此处直写等价）。
	raw := make([]byte, passwordBytes)
	_, err = rand.Read(raw)
	require.NoError(t, err)
	newPassword := hex.EncodeToString(raw)
	credBytes, err := json.Marshal(map[string]string{"username": credentialUser, "password": newPassword})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keys", credentialFile), credBytes, 0o600))

	p2, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	newLine := string(p2.ManagedMaterials().SecretFiles[htpasswdFile])
	assert.NotEqual(t, oldLine, newLine, "rotated credential must mint a new htpasswd line")
	assert.Equal(t, credentialUser+":", newLine[:len(credentialUser)+1])
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(strings.SplitN(newLine, ":", 2)[1]), []byte(newPassword)),
		"new line must verify against the rotated password")
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
	// 数据面停止宽限：滚动替换（密码轮换/升级）不得落进编排器缺省 10s
	// 硬杀窗（staging pgvector 同类事故实证，2026-10-03）。
	assert.Equal(t, 60*time.Second, w.StopGrace)
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

// TestFactoryAddressResolutionOrder 钉 ADR-0036 地址解析序：装配 ctx
// （config.registry.addr 形态）优先，env FLEETLY_REGISTRY_ADDR 旧通道兜底，
// 两者皆空 = 停用（现状语义，build 源部署在 prepare 精确失败）。
func TestFactoryAddressResolutionOrder(t *testing.T) {
	t.Setenv("FLEETLY_DATA_ROOT", t.TempDir())
	t.Setenv("FLEETLY_REGISTRY_ADDR", "10.0.0.9:5000")

	endpointAddr := func(t *testing.T, ctx context.Context) string {
		t.Helper()
		p, err := capability.Build(ctx, capability.KindRegistry, "zot")
		require.NoError(t, err)
		reg, ok := p.(capability.Registry)
		require.True(t, ok, "zot provider must implement the Registry port")
		ep, err := reg.Endpoint(context.Background())
		require.NoError(t, err)
		return ep.Addr
	}

	// config 注入优先于 env。
	ctx := capability.WithRegistryAddr(context.Background(), "10.124.0.3:5000")
	assert.Equal(t, "10.124.0.3:5000", endpointAddr(t, ctx))

	// env 旧通道兜底（无 ctx 注入）。
	assert.Equal(t, "10.0.0.9:5000", endpointAddr(t, context.Background()))

	// 空注入不覆盖 env（WithRegistryAddr 空值透传原 ctx）。
	emptyCtx := capability.WithRegistryAddr(context.Background(), "")
	assert.Equal(t, "10.0.0.9:5000", endpointAddr(t, emptyCtx))

	// 皆空 = 受管仓库停用（错误明示双通道，不静默跳过）。
	t.Setenv("FLEETLY_REGISTRY_ADDR", "")
	_, err := capability.Build(context.Background(), capability.KindRegistry, "zot")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stays disabled")
}

// per-Project 凭证域隔离测试批（ADR-0036 N2 兑现节 2）：铸造幂等持久、
// 材料随集再生成且字节稳定、门禁形状（adminPolicy/前缀仓/扁平可读）、
// 单 Project 坏盘时材料冻结语义、projectID 钳制。

// tProjectID 是测试用 ULID 形态 projectID（Crockford base32 大写 26 位）。
const tProjectID = "01J8ZQ5T8HBSC3XR2N1G4D9K2M"

// TestProjectCredentialPersistRoundTrip 钉铸造幂等：同 dataRoot 双次构造同
// 密码同行（E28 按面适用）；文件 0o600；行可验密码；形态坏 fail loud。
func TestProjectCredentialPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	ep1, err := p1.EndpointForProject(context.Background(), tProjectID)
	require.NoError(t, err)
	p2, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	ep2, err := p2.EndpointForProject(context.Background(), tProjectID)
	require.NoError(t, err)
	assert.Equal(t, ep1.Cred.Secret, ep2.Cred.Secret, "project credential must persist across constructions")
	assert.Equal(t, tProjectID, ep1.Cred.Username, "project credential username is the project id")
	assert.Equal(t, "10.0.0.1:5000", ep1.Cred.Server)
	assert.NotEqual(t, p1.cred.Secret, ep1.Cred.Secret, "project credential must differ from the platform admin credential")

	path := filepath.Join(dir, "keys", projectCredDir, tProjectID+".json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "project credential file must be owner-only")
	}
	// 材料里的行必须可验该 Project 密码（htpasswd 行与端点凭证同源）。
	ht := string(p1.ManagedMaterialsFor([]string{tProjectID}).SecretFiles[htpasswdFile])
	found := false
	for _, line := range strings.Split(ht, "\n") {
		u, h, ok := strings.Cut(line, ":")
		if ok && u == tProjectID {
			found = true
			require.NoError(t, bcrypt.CompareHashAndPassword([]byte(h), []byte(ep1.Cred.Secret)))
		}
	}
	assert.True(t, found, "materials htpasswd must carry the project user line")

	// 形态坏（坏 JSON）fail loud——静默再铸会把凭证面故障伪装成正常重启。
	// 断言走新实例（本实例的缓存按设计对坏盘无感——进程内值恒定）。
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	p3, err := New("10.0.0.1:5000", dir)
	require.NoError(t, err)
	_, err = p3.EndpointForProject(context.Background(), tProjectID)
	require.Error(t, err)
}

// TestProjectCredentialIDClamp 钉 projectID 三面钳制：路径分隔符/穿越/
// htpasswd 冒号/空白/超长一律拒绝（端口不信任调用方）。
func TestProjectCredentialIDClamp(t *testing.T) {
	p, err := New("10.0.0.1:5000", t.TempDir())
	require.NoError(t, err)
	for _, id := range []string{"", "..", "../escape", "a/b", "a:b", "a b", strings.Repeat("x", projectIDMaxLen+1)} {
		_, err := p.EndpointForProject(context.Background(), id)
		require.Error(t, err, "project id %q must be rejected", id)
	}
	_, err = p.EndpointForProject(context.Background(), tProjectID)
	require.NoError(t, err, "ULID-shaped id must pass")
}

// TestProjectMaterialsGateShape 钉门禁形状（zot v2.1.21 语义实证）：adminPolicy
// = 平台用户全域；"<projectID>/**" = 该用户 read/create/update 且前缀小写化；
// "*" = defaultPolicy read（扁平存量可读，`*` 不跨 `/`）；无 "**" catch-all。
func TestProjectMaterialsGateShape(t *testing.T) {
	dir := t.TempDir()
	p, err := New("10.124.0.3:5000", dir)
	require.NoError(t, err)
	other := "01J8ZQ5T8HBSC3XR2N1G4D9K2Z"
	m := p.ManagedMaterialsFor([]string{tProjectID, other})

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(m.SecretFiles[configFile], &cfg))
	httpc := cfg["http"].(map[string]any)
	ac := httpc["accessControl"].(map[string]any)
	admin := ac["adminPolicy"].(map[string]any)
	assert.Equal(t, []any{credentialUser}, admin["users"])
	assert.Equal(t, []any{"read", "create", "update", "delete"}, admin["actions"])

	repos := ac["repositories"].(map[string]any)
	// 前缀仓：ULID 大写 → 仓前缀小写（docker 引用小写约束），用户名保原样。
	prefixed := repos[strings.ToLower(tProjectID)+"/**"].(map[string]any)
	policies := prefixed["policies"].([]any)
	require.Len(t, policies, 1)
	pol := policies[0].(map[string]any)
	assert.Equal(t, []any{tProjectID}, pol["users"], "policy username keeps the raw project id")
	assert.Equal(t, []any{"read", "create", "update"}, pol["actions"])
	// 扁平仓：defaultPolicy read（存量 digest 引用全用户可读）。
	flat := repos["*"].(map[string]any)
	assert.Equal(t, []any{"read"}, flat["defaultPolicy"])
	assert.NotContains(t, repos, "**", "no catch-all: longest-match would beat '*' and break flat readability")
	assert.Len(t, repos, 3, "exactly '*', and one gate per project")

	// htpasswd = 平台行 + 集序行。
	lines := strings.Split(string(m.SecretFiles[htpasswdFile]), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, credentialUser, strings.SplitN(lines[0], ":", 2)[0])
}

// TestProjectMaterialsByteStability 钉 P7 性质按面适用：同集异序/重复调用
// 字节恒等（载体指纹只随集与凭证变——zot 滚动一次性的前提）。
func TestProjectMaterialsByteStability(t *testing.T) {
	p, err := New("10.124.0.3:5000", t.TempDir())
	require.NoError(t, err)
	ids := []string{"01J8ZQ5T8HBSC3XR2N1G4D9K2M", "01J8ZQ5T8HBSC3XR2N1G4D9K2Z", "01AAAAAAAAAAAAAAAAAAAAAAAA"}
	a := p.ManagedMaterialsFor(ids)
	b := p.ManagedMaterialsFor([]string{ids[2], ids[0], ids[1], ids[0]})
	assert.Equal(t, a.SecretFiles[configFile], b.SecretFiles[configFile], "input order must not leak into config bytes")
	assert.Equal(t, a.SecretFiles[htpasswdFile], b.SecretFiles[htpasswdFile], "input order must not leak into htpasswd bytes")
	c := p.ManagedMaterialsFor(ids)
	assert.Equal(t, a.SecretFiles, c.SecretFiles, "repeated calls must be byte-identical")
}

// TestProjectMaterialsFrozenOnCorruptDisk 钉失败语义分层：
// ①进程已铸过完整集 → 集内新增坏项时材料冻结上一稳定集（在场用户一个
// 不滚）；②无前史（首铸即遇坏盘）→ 跳过坏项带全健康项；端点面均精确
// fail loud。
func TestProjectMaterialsFrozenOnCorruptDisk(t *testing.T) {
	dir := t.TempDir()
	p, err := New("10.124.0.3:5000", dir)
	require.NoError(t, err)
	a := "01J8ZQ5T8HBSC3XR2N1G4D9K2M"
	b := "01J8ZQ5T8HBSC3XR2N1G4D9K2Z"
	healthy := p.ManagedMaterialsFor([]string{a}) // 铸 A 并冻结 [A] 集

	// 物化坏盘：B 的凭证文件直接写坏（B 从未进缓存）。
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "keys", projectCredDir, b+".json"), []byte("corrupt"), 0o600))

	// ①冻结：[A,B] 集因 B 失败 → 返回上一稳定 [A] 集。
	got := p.ManagedMaterialsFor([]string{a, b})
	assert.Equal(t, healthy.SecretFiles, got.SecretFiles,
		"materials must freeze at the last good set instead of rolling users off")
	_, err = p.EndpointForProject(context.Background(), b)
	require.Error(t, err, "the endpoint face must fail loudly on the same corruption")
	_, err = p.EndpointForProject(context.Background(), a)
	require.NoError(t, err, "healthy projects are unaffected")

	// ②无前史：同 dataRoot 新进程（缓存/前史全空）遇坏 B → 跳过坏项，
	// A 的用户与门禁保持在场。
	fresh, err := New("10.124.0.3:5000", dir)
	require.NoError(t, err)
	m := fresh.ManagedMaterialsFor([]string{a, b})
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(m.SecretFiles[configFile], &cfg))
	repos := cfg["http"].(map[string]any)["accessControl"].(map[string]any)["repositories"].(map[string]any)
	assert.Contains(t, repos, strings.ToLower(a)+"/**", "healthy project survives the no-history fallback")
	assert.NotContains(t, repos, strings.ToLower(b)+"/**", "broken project is absent from the fallback set")
	assert.Contains(t, string(m.SecretFiles[htpasswdFile]), a+":",
		"healthy project's htpasswd line survives")
}

// TestUnscopedMaterialsCarryGate 钉零集同形状：ManagedMaterials()（面探测
// 未协商子面的回退路径）与 ManagedMaterialsFor(nil) 逐位一致，且 config
// 恒带 accessControl（升级后单一 config 形状）。
func TestUnscopedMaterialsCarryGate(t *testing.T) {
	p, err := New("10.124.0.3:5000", t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, p.ManagedMaterialsFor(nil).SecretFiles, p.ManagedMaterials().SecretFiles)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal(p.ManagedMaterials().SecretFiles[configFile], &cfg))
	ac := cfg["http"].(map[string]any)["accessControl"].(map[string]any)
	assert.Contains(t, ac["repositories"], "*", "flat repos stay readable with zero projects")
}
