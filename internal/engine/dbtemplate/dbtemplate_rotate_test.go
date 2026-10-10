package dbtemplate

// RotatePassword 方言面测试（IA v3 二期⑤b）：数据面三方言的 argv/材料
// 形状 + 声明式方言（redis）零 utility 契约 + 字符集闸 fail-closed。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotatePasswordFaces(t *testing.T) {
	for _, tc := range []struct {
		engine    string
		argvTail  string
		env       map[string]string
		matKeys   []string
		declarive bool
	}{
		{
			engine:   "postgres",
			argvTail: "ALTER USER fleetly WITH PASSWORD 'nextpw'",
			env:      map[string]string{"PGPASSFILE": "/run/secrets/database-backup-pgpass"}, //nolint:gosec // G101 误报：路径串非凭证
			matKeys:  []string{"database-backup-pgpass"},
		},
		{
			engine:   "pgvector",
			argvTail: "ALTER USER fleetly WITH PASSWORD 'nextpw'",
			env:      map[string]string{"PGPASSFILE": "/run/secrets/database-backup-pgpass"}, //nolint:gosec // G101 误报：路径串非凭证
			matKeys:  []string{"database-backup-pgpass"},
		},
		{
			engine:   "mysql",
			argvTail: "ALTER USER 'fleetly'@'%' IDENTIFIED BY 'nextpw'",
			matKeys:  []string{"database-backup-defaults"},
		},
		{
			engine:   "mongo",
			argvTail: "db.getSiblingDB('fleetly').changeUserPassword('fleetly', 'nextpw')",
			matKeys:  []string{"database-backup-config"},
		},
		{
			engine:    "redis",
			declarive: true, // 声明式：空 Argv，Secret 重写 + 载体重下发生效
		},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			tpl, ok := For(tc.engine)
			require.True(t, ok)
			spec, err := tpl.RotatePassword("db-01j8", "oldpw", "nextpw")
			require.NoError(t, err)
			if tc.declarive {
				assert.Empty(t, spec.Argv)
				assert.Empty(t, spec.Env)
				assert.Empty(t, spec.SecretFiles)
				return
			}
			assert.Equal(t, tc.argvTail, spec.Argv[len(spec.Argv)-1])
			assert.Equal(t, tc.env, spec.Env)
			for _, key := range tc.matKeys {
				assert.Contains(t, spec.SecretFiles, key)
			}
			// 认证材料携带 current（旧值），绝不含 next（改密目标只进 argv
			// 语句本身——材料面是认证面）。
			for name, body := range spec.SecretFiles {
				assert.NotContains(t, string(body), "nextpw", "material %s must authenticate with the current password", name)
				assert.Contains(t, string(body), "oldpw")
			}
		})
	}
}

// TestRotatePasswordCharsetGate：渲染闸对 current/next 双向 fail-closed
// （与 Backup/Restore 同闸——敌意值到不了 argv 插值点）。
func TestRotatePasswordCharsetGate(t *testing.T) {
	for _, engine := range Engines() {
		t.Run(engine, func(t *testing.T) {
			tpl, ok := For(engine)
			require.True(t, ok)
			hostile := "pw'; DROP TABLE secrets;--"
			_, err := tpl.RotatePassword("db-01j8", hostile, "nextpw")
			assert.Error(t, err, "hostile current must be rejected")
			_, err = tpl.RotatePassword("db-01j8", "oldpw", hostile)
			assert.Error(t, err, "hostile next must be rejected")
			_, err = tpl.RotatePassword("db-01j8", "", "nextpw")
			assert.Error(t, err, "empty current must be rejected")
			_, err = tpl.RotatePassword("db-01j8", "oldpw", "")
			assert.Error(t, err, "empty next must be rejected")
		})
	}
}

// TestMintPassword：铸面契约——hex 48、取样不重叠（熵面冒烟）。
func TestMintPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		pw := MintPassword()
		assert.Len(t, pw, 48)
		assert.NotContains(t, strings.ToLower(pw), "g", "hex alphabet only")
		assert.False(t, seen[pw], "minted passwords must not repeat")
		seen[pw] = true
	}
}
