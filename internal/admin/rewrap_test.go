package admin

// rewrap 维护面的 hermetic 验收（temp 数据根真 SQLite + 真 age key）：
// 轮换全流程（旧 KEK 播种 → 文件序轮换 → dry-run → 执行 → 幂等重跑）+
// 不可解行的拒绝语义。明文断言只在测试内短驻，不落日志。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
)

// seedOldWorld 用旧 KEK 播种数据根：两行活跃 secret + 一行 tombstone +
// 一行 hook webhook secret。返回旧 KEK 文件内容（构造"旧 key 单独在场"
// 断言用）与打开的库。
func seedOldWorld(t *testing.T, root string) (string, *state.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(root, "fleetly.db"), state.WallClock())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	cipher, err := material.LoadCipher(root)
	require.NoError(t, err)

	secrets := secret.New(db.Clock())
	mustSeal := func(v string) []byte {
		ct, err := cipher.Seal([]byte(v))
		require.NoError(t, err)
		return ct
	}
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "s1", ProjectID: "proj-a", Name: "key-a", Ciphertext: mustSeal("value-a1"), Fingerprint: material.Fingerprint([]byte("value-a1")),
	}))
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "s2", ProjectID: "proj-a", Name: "db_password", Ciphertext: mustSeal("value-a2"), Fingerprint: material.Fingerprint([]byte("value-a2")),
	}))
	// tombstone 行同样进重封面（undelete 路径依赖密文可解）。
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "s3", ProjectID: "proj-b", Name: "gone", Ciphertext: mustSeal("value-b"), Fingerprint: material.Fingerprint([]byte("value-b")),
	}))
	require.NoError(t, secrets.SoftDelete(ctx, db.Runner(), "proj-b", "gone"))

	// app_hooks.app_id 有 FK：插一行最小 App 载体行（projects 无 FK 链）。
	_, err = db.Runner().ExecContext(ctx, `INSERT INTO apps (id, project_id, name, created_at, updated_at, deleted_at) VALUES ('app-1', 'proj-a', 'web', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`)
	require.NoError(t, err)
	require.NoError(t, hook.New(db.Clock()).Create(ctx, db.Runner(), &hook.Hook{
		AppID: "app-1", Repo: "acme/web", Branch: "main", Dockerfile: "Dockerfile",
		TokenSHA256: "abc", TokenPrefix: "abcd",
		SecretCiphertext: mustSeal("hook-secret"),
	}))

	oldKey, err := os.ReadFile(filepath.Join(root, "keys", "master.agekey")) //nolint:gosec // 测试夹具读测试自铸的 KEK
	require.NoError(t, err)
	return string(oldKey), db
}

// rotateKeyFiles 模拟操作者轮换文件序：现役改名为退役文件，新 KEK 就位
// master.agekey。返回装载后的轮换 Cipher（现役=新 key，退役=旧 key）。
func rotateKeyFiles(t *testing.T, root, oldKeyContent string) *material.Cipher {
	t.Helper()
	writeFile := func(name, content string) {
		p := filepath.Join(root, "keys", name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	writeFile("master-retired-2026-10.agekey", oldKeyContent)
	fresh, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	writeFile("master.agekey", fresh.String())
	c, err := material.LoadExistingCipher(root)
	require.NoError(t, err)
	return c
}

// listSecretCT / listHookCT 读全部密文（断言比对用）。
func listSecretCT(t *testing.T, db *state.DB) map[string][]byte {
	t.Helper()
	rows, err := secret.New(db.Clock()).ListAll(context.Background(), db.Runner())
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, r := range rows {
		out[r.ID] = r.Ciphertext
	}
	return out
}

func listHookCT(t *testing.T, db *state.DB) map[string][]byte {
	t.Helper()
	rows, err := hook.New(db.Clock()).ListAll(context.Background(), db.Runner())
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, r := range rows {
		out[r.AppID] = r.SecretCiphertext
	}
	return out
}

func TestRewrapRotationFlow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	oldKey, db := seedOldWorld(t, root)
	oldCTSecrets := listSecretCT(t, db)
	oldCTHooks := listHookCT(t, db)
	require.Len(t, oldCTSecrets, 3)
	require.Len(t, oldCTHooks, 1)

	cipher := rotateKeyFiles(t, root, oldKey)

	// dry-run：全量可解验证 + 报告条数，不落库。
	rep, err := RewrapSecrets(ctx, db, cipher, true)
	require.NoError(t, err)
	assert.Equal(t, 3, rep.SecretsTotal, "tombstoned row is included")
	assert.Equal(t, 3, rep.SecretsRewrapped)
	assert.Equal(t, 0, rep.SecretsCurrent)
	assert.Equal(t, 1, rep.HooksTotal)
	assert.Equal(t, 1, rep.HooksRewrapped)
	assert.Empty(t, rep.Failures)
	for id, ct := range listSecretCT(t, db) {
		assert.Equal(t, oldCTSecrets[id], ct, "dry run must not rewrite secret %s", id)
	}
	for id, ct := range listHookCT(t, db) {
		assert.Equal(t, oldCTHooks[id], ct, "dry run must not rewrite hook %s", id)
	}

	// 执行：单事务全量重封。
	rep, err = RewrapSecrets(ctx, db, cipher, false)
	require.NoError(t, err)
	assert.Equal(t, 3, rep.SecretsRewrapped)
	assert.Equal(t, 1, rep.HooksRewrapped)

	// 轮换生效断言：全部行现役（新）key 可解；新 key 单独在场可解；
	// 旧 key 单独在场不再可解（旧 key 退役前提成立）。
	for id, ct := range listSecretCT(t, db) {
		assert.True(t, cipher.OpensWithActive(ct), "secret %s must be sealed to the active key", id)
		assert.NotEqual(t, oldCTSecrets[id], ct, "secret %s must have been rewritten", id)
		pt, err := cipher.Open(ct)
		require.NoError(t, err)
		assert.NotEmpty(t, pt)
	}
	for id, ct := range listHookCT(t, db) {
		assert.True(t, cipher.OpensWithActive(ct), "hook %s must be sealed to the active key", id)
	}
	require.NoError(t, os.Remove(filepath.Join(root, "keys", "master-retired-2026-10.agekey")))
	retiredDone, err := material.LoadExistingCipher(root)
	require.NoError(t, err)
	for id, ct := range listSecretCT(t, db) {
		_, err := retiredDone.Open(ct)
		assert.NoError(t, err, "active-only key must open secret %s after rotation", id)
	}
	oldOnlyRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(oldOnlyRoot, "keys"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(oldOnlyRoot, "keys", "master.agekey"), []byte(oldKey), 0o600))
	oldOnly, err := material.LoadExistingCipher(oldOnlyRoot)
	require.NoError(t, err)
	for id, ct := range listSecretCT(t, db) {
		_, err := oldOnly.Open(ct)
		assert.Error(t, err, "retired key alone must no longer open secret %s", id)
	}

	// 幂等重跑：全部已现行 → 零重写（密文逐字节不变）。
	currentCTSecrets := listSecretCT(t, db)
	currentCTHooks := listHookCT(t, db)
	rep, err = RewrapSecrets(ctx, db, cipher, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rep.SecretsRewrapped)
	assert.Equal(t, 3, rep.SecretsCurrent)
	assert.Equal(t, 0, rep.HooksRewrapped)
	assert.Equal(t, 1, rep.HooksCurrent)
	for id, ct := range listSecretCT(t, db) {
		assert.Equal(t, currentCTSecrets[id], ct, "idempotent rerun must not rewrite secret %s", id)
	}
	for id, ct := range listHookCT(t, db) {
		assert.Equal(t, currentCTHooks[id], ct, "idempotent rerun must not rewrite hook %s", id)
	}
}

func TestRewrapRefusesUnopenableRows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	oldKey, db := seedOldWorld(t, root)
	// 不可解行：垃圾密文直插两表（模拟 key 全损/外来行）。
	secrets := secret.New(db.Clock())
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "s-x", ProjectID: "proj-a", Name: "corrupt", Ciphertext: []byte("garbage"), Fingerprint: "ff",
	}))
	hooks := hook.New(db.Clock())
	require.NoError(t, hooks.UpdateSecretCiphertext(ctx, db.Runner(), "app-1", []byte("garbage")))

	cipher := rotateKeyFiles(t, root, oldKey)

	// dry-run：逐条报不可解，不落库。
	rep, err := RewrapSecrets(ctx, db, cipher, true)
	require.ErrorContains(t, err, "2 row(s) cannot be opened")
	require.NotNil(t, rep)
	assert.Len(t, rep.Failures, 2)
	assert.Contains(t, []string{rep.Failures[0].Key, rep.Failures[1].Key}, "proj-a/corrupt")
	assert.Contains(t, []string{rep.Failures[0].Key, rep.Failures[1].Key}, "app-1")
	assert.Equal(t, 3, rep.SecretsRewrapped, "openable rows are still counted for the report")
	assert.Equal(t, 1, rep.HooksTotal)
	assert.Equal(t, 0, rep.HooksRewrapped, "the only hook row is the corrupt one")
	secretsAfter := listSecretCT(t, db)
	assert.Equal(t, []byte("garbage"), secretsAfter["s-x"], "dry run must not write")

	// 执行：拒执行，好行也不写（全有或全无）。
	_, err = RewrapSecrets(ctx, db, cipher, false)
	require.ErrorContains(t, err, "cannot be opened")
	for id, ct := range listSecretCT(t, db) {
		if id == "s-x" {
			continue
		}
		assert.False(t, cipher.OpensWithActive(ct), "secret %s must stay old-sealed after a refused run", id)
	}
}

func TestRewrapEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	_, err := material.LoadCipher(root)
	require.NoError(t, err)
	db, err := state.Open(ctx, filepath.Join(root, "fleetly.db"), state.WallClock())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cipher, err := material.LoadExistingCipher(root)
	require.NoError(t, err)

	rep, err := RewrapSecrets(ctx, db, cipher, true)
	require.NoError(t, err)
	assert.Equal(t, 0, rep.SecretsTotal)
	assert.Equal(t, 0, rep.HooksTotal)
	rep, err = RewrapSecrets(ctx, db, cipher, false)
	require.NoError(t, err)
	assert.Equal(t, 0, rep.SecretsTotal)
	assert.Equal(t, 0, rep.HooksTotal)
}
