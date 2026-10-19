package keyring

// KEK 文件面测试（P1a-3a 自 domain/services/material 平移，断言零漂移；
// 新增首启生成的权限钉扎断言）：装载/生成/退役装载/维护面形态。

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"filippo.io/age"

	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
)

// writeKeyFile 把 identity 落成 KEK 文件（测试夹具：模拟操作者放置动作）。
func writeKeyFile(t *testing.T, path, identity string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(identity), 0o600)) //nolint:gosec // 测试夹具：路径由本文件字面量拼装（t.TempDir 下）
}

// readKeyFile 读回 KEK 文件文本（测试夹具：取现役 identity 串——装载面
// 不回显密钥材料，文件是测试唯一可达面）。
func readKeyFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // 测试夹具：读回本测试在 t.TempDir 内写下的文件
	require.NoError(t, err)
	return string(bytes.TrimSpace(data))
}

// rotateKeys 模拟轮换文件序：现役改名为退役（master-<标记>.agekey），
// 新 key 就位 master.agekey。返回新现役 Cipher。
func rotateKeys(t *testing.T, dataRoot, oldIdentity string) *material.Cipher {
	t.Helper()
	writeKeyFile(t, filepath.Join(dataRoot, "keys", "master-retired-test.agekey"), oldIdentity)
	fresh, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	writeKeyFile(t, filepath.Join(dataRoot, "keys", kekFileName), fresh.String())
	c, err := LoadExistingCipher(dataRoot)
	require.NoError(t, err)
	return c
}

// 首启生成 + 持久性：权限钉扎（目录 0700、文件 0600），同数据根二次
// 加载可解、异数据根不可解。
func TestLoadCipherGeneratesPersistsAndPerms(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadCipher(dir)
	require.NoError(t, err)

	// 权限断言：keys 目录与 KEK 文件都是平台私有形态。
	info, err := os.Stat(filepath.Join(dir, keysDirName))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, fs.FileMode(0o700), info.Mode().Perm(), "keys dir must be owner-only")
	keyInfo, err := os.Stat(filepath.Join(dir, keysDirName, kekFileName))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), keyInfo.Mode().Perm(), "master key must be owner-only")

	secret := []byte("hunter2-but-longer-1234")
	ct, err := c1.Seal(secret)
	require.NoError(t, err)
	assert.NotContains(t, string(ct), string(secret), "ciphertext must not contain plaintext")

	// 同数据根重载（持久性）。
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

// 轮换窗口装载面：现役 + 退役双 key 在场——Seal 恒用现役，Open 现役
// 优先、退役兜底；daemon 路径（LoadCipher）与维护面同款装载退役 key。
func TestRotationWindowLoadAndOpen(t *testing.T) {
	dir := t.TempDir()
	oldCipher, err := LoadCipher(dir)
	require.NoError(t, err)
	oldID := readKeyFile(t, filepath.Join(dir, keysDirName, kekFileName))

	// 旧 KEK 密文。
	oldCT, err := oldCipher.Seal([]byte("sealed-with-old"))
	require.NoError(t, err)

	rotated := rotateKeys(t, dir, oldID)
	newCT, err := rotated.Seal([]byte("sealed-with-new"))
	require.NoError(t, err)

	assert.True(t, rotated.OpensWithActive(newCT), "fresh seal must open with the active key")
	assert.False(t, rotated.OpensWithActive(oldCT), "old-sealed ciphertext must not classify as current")
	pt, err := rotated.Open(oldCT)
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-with-old"), pt)

	// daemon 路径（LoadCipher）同样装载退役 key：轮换中重启不破坏旧密
	// 文注入路径；且现役在场时不重新生成。
	daemon, err := LoadCipher(dir)
	require.NoError(t, err)
	pt, err = daemon.Open(oldCT)
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-with-old"), pt, "daemon must keep decrypting old-sealed rows during rotation")
	assert.True(t, daemon.OpensWithActive(newCT))
}

// 退役 key 文件损坏必须装载即错（静默跳过会把解不开的行拖到 rewrap 报
// 告才暴露，且掩盖放置失误）。
func TestRetiredKeyParseFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadCipher(dir)
	require.NoError(t, err)
	activeID := readKeyFile(t, filepath.Join(dir, keysDirName, kekFileName))
	writeKeyFile(t, filepath.Join(dir, "keys", "master-broken.agekey"), "not-an-age-identity")
	_, err = LoadExistingCipher(dir)
	require.ErrorContains(t, err, "master-broken.agekey")

	// 修复（换成合法 key）后装载过，且旧 Cipher 的密文仍可经退役兜底解。
	writeKeyFile(t, filepath.Join(dir, "keys", "master-broken.agekey"), activeID)
	c2, err := LoadExistingCipher(dir)
	require.NoError(t, err)
	ct, err := c2.Seal([]byte("v"))
	require.NoError(t, err)
	_, err = c1.Open(ct)
	assert.NoError(t, err, "the retired-loaded original key must still decrypt via fallback")
}

// LoadExistingCipher 是维护面形态：现役缺席即错且绝不落盘（对比
// LoadCipher 的首启生成行为）。
func TestLoadExistingCipherNeverGenerates(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadExistingCipher(dir)
	require.ErrorContains(t, err, "no active master key")
	_, statErr := os.Stat(filepath.Join(dir, "keys", kekFileName))
	assert.True(t, os.IsNotExist(statErr), "maintenance loader must not create a key file")

	// 现役在场：正常装载。
	_, err = LoadCipher(dir)
	require.NoError(t, err)
	_, err = LoadExistingCipher(dir)
	assert.NoError(t, err)
}

// 轮换窗口语义（R14-M8）：现役缺席但退役在场 = 轮换不完整——必须硬错，
// 绝不静默铸新钥（新钥会顶掉轮换中的现役，窗口内新密文随后永久不可解）。
func TestLoadCipherRefusesMintWhenRetiredExists(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCipher(dir)
	require.NoError(t, err)
	keys := filepath.Join(dir, keysDirName)
	oldID := readKeyFile(t, filepath.Join(keys, kekFileName))

	// 轮换第 1 步到一半崩溃：现役改名为退役，新现役尚未写入。
	require.NoError(t, os.Rename(filepath.Join(keys, kekFileName), filepath.Join(keys, "master-oldrotation.agekey")))
	require.NoError(t, os.WriteFile(filepath.Join(keys, "master-oldrotation.agekey"), []byte(oldID), 0o600))

	_, err = LoadCipher(dir)
	require.Error(t, err, "missing active + present retired must be a hard error")
	assert.Contains(t, err.Error(), "rotation is incomplete")
	assert.Contains(t, err.Error(), "refusing to mint")
	_, statErr := os.Stat(filepath.Join(keys, kekFileName))
	assert.True(t, os.IsNotExist(statErr), "the daemon loader must not mint a replacement active key")

	// 维护面同判据（它本就不生成，只做错误面回归）。
	_, err = LoadExistingCipher(dir)
	require.ErrorContains(t, err, "no active master key")
}

// 首启崩溃窗（R07-L3）：tmp 文件残留在 keys/ 且无现役/退役 → 仍可安全
// 生成（此时尚无密文行），残留 tmp 不得被当作 KEK 装载。
func TestLoadCipherIgnoresCrashedTempFile(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, keysDirName)
	require.NoError(t, os.MkdirAll(keys, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(keys, kekFileName+".tmp-12345"), []byte("partial"), 0o600))

	c, err := LoadCipher(dir)
	require.NoError(t, err, "a crashed first-boot temp file must not brick the next start")
	ct, err := c.Seal([]byte("v"))
	require.NoError(t, err)
	_, err = c.Open(ct)
	assert.NoError(t, err)
}

// KEK 落盘原子性（R07-L3）：成功 = 正式路径完整内容 + 无 tmp 残留 + 权限
// 0600/0700（Windows 跳过权限位断言——已知 ACL 环境项）。
func TestWriteFileAtomicSuccess(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, keysDirName)
	target := filepath.Join(keys, kekFileName)
	require.NoError(t, writeFileAtomic(target, []byte("AGE-SECRET-KEY-1TEST"), 0o600))

	data, err := os.ReadFile(target) //nolint:gosec // 本测试 t.TempDir 内自建路径
	require.NoError(t, err)
	assert.Equal(t, "AGE-SECRET-KEY-1TEST", string(data))
	entries, err := os.ReadDir(keys)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the temp file must be renamed into place, not left behind")
	assert.Equal(t, kekFileName, entries[0].Name())
	if runtime.GOOS != "windows" {
		di, err := os.Stat(keys)
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o700), di.Mode().Perm())
		fi, err := os.Stat(target)
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o600), fi.Mode().Perm())
	}
}

// 写失败窗（rename 目标被目录占位）：错误返回、正式路径原状、tmp 清理
// 干净——不留半文件。
func TestWriteFileAtomicFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, kekFileName)
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "occupied"), []byte("x"), 0o600))

	err := writeFileAtomic(target, []byte("half"), 0o600)
	require.Error(t, err)
	entries, rerr := os.ReadDir(dir)
	require.NoError(t, rerr)
	require.Len(t, entries, 1, "the failed write must remove its temp file")
	assert.Equal(t, kekFileName, entries[0].Name())
	info, serr := os.Stat(target)
	require.NoError(t, serr)
	assert.True(t, info.IsDir(), "the existing target must not be replaced")
}

// 权限复核：已存在过宽（0755/0644）的 keys 目录与 KEK 文件被收敛到
// 0700/0600；权限位断言只对 POSIX 有效（Windows ACL 已知环境项）。
func TestWriteFileAtomicEnforcesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("known environment item: Go permission bits do not carry Windows ACL semantics")
	}
	dir := t.TempDir()
	keys := filepath.Join(dir, keysDirName)
	require.NoError(t, os.MkdirAll(keys, 0o755))
	target := filepath.Join(keys, kekFileName)
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	require.NoError(t, writeFileAtomic(target, []byte("new"), 0o600))
	di, err := os.Stat(keys)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), di.Mode().Perm(), "the keys directory must be owner-only")
	fi, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), fi.Mode().Perm(), "the KEK file must be owner-only")
}

// 非 KEK 文件不进退役装载面（形态过滤）；目录条目忽略。
func TestRetiredKeyGlobFiltering(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCipher(dir)
	require.NoError(t, err)
	keys := filepath.Join(dir, "keys")
	writeKeyFile(t, filepath.Join(keys, "registry.json"), "{}") // 同目录他类密钥文件
	writeKeyFile(t, filepath.Join(keys, "master.agekey.bak"), "junk")
	require.NoError(t, os.MkdirAll(filepath.Join(keys, "master-dir.agekey"), 0o700))
	c, err := LoadExistingCipher(dir)
	require.NoError(t, err, "non-KEK files and directories must not break loading")
	ct, err := c.Seal([]byte("v"))
	require.NoError(t, err)
	_, err = c.Open(ct)
	assert.NoError(t, err)
}
