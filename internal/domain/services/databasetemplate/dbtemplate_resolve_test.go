package databasetemplate

// 版本解析纯核（resolveVersionSpec）与受理门（AdmitCreateVersion/
// RestoreCompat）的语义面白昼测试（B2a 落、B2c 分档改造——ADR-0094 决策
// 2/3/5/6）：空 version = Default、Default 缺席 = 注册表失不变量、未知版本
// 列合法值；「受理拒 vs 行解析放行」分档——resolveVersionSpec 照常解析
// 弃用条目（存量续跑/灾备重建/目录装配面），全新建的弃用门在
// AdmitCreateVersion（同版本带 restore 放行——灾备义）。合成表承载——
// 首版矩阵无弃用条目（TestImageDigestsPinned 钉 Default 非弃用），真值域
// 上的弃用语义经纯核钉死（scanEngineNameLiterals 红灯实验先例：纯核直测，
// 不造真注册表条目）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVersionSpecSemantics(t *testing.T) {
	specs := []dataservicetemplate.VersionSpec{
		{ID: "7", Tag: "7-x", Digest: "sha256:" + strings.Repeat("a", 64)},
		{ID: "8", Tag: "8-x", Digest: "sha256:" + strings.Repeat("b", 64), Deprecated: true, Note: "EOL 2027-01; move to 9"},
		{ID: "9", Tag: "9-x", Digest: "sha256:" + strings.Repeat("c", 64), Default: true},
	}

	got, err := resolveVersionSpec("demo", specs, "")
	require.NoError(t, err)
	assert.Equal(t, "9", got.ID, "empty version resolves the default")

	got, err = resolveVersionSpec("demo", specs, "9")
	require.NoError(t, err)
	assert.Equal(t, "9", got.ID)

	// B2c 分档：弃用条目照常解析（行解析放行——存量续跑/灾备重建/目录
	// 面共用；拒全新建的门在 AdmitCreateVersion）。
	got, err = resolveVersionSpec("demo", specs, "8")
	require.NoError(t, err)
	assert.Equal(t, "8", got.ID)
	assert.True(t, got.Deprecated)
	assert.Equal(t, "EOL 2027-01; move to 9", got.Note, "deprecation note rides the spec (catalog/admission face)")

	_, err = resolveVersionSpec("demo", specs, "6")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownVersion)
	assert.Contains(t, err.Error(), "7", "error lists every in-registry version id (deprecated ones included)")

	noDefault := []dataservicetemplate.VersionSpec{{ID: "7", Tag: "7-x", Digest: "sha256:" + strings.Repeat("a", 64)}}
	_, err = resolveVersionSpec("demo", noDefault, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default version")
}

func TestAdmitCreateVersionDeprecationTiers(t *testing.T) {
	deprecated := dataservicetemplate.VersionSpec{
		ID: "8", Tag: "8-x", Digest: "sha256:" + strings.Repeat("b", 64),
		Deprecated: true, Note: "EOL 2027-01; move to 9",
	}
	legal := []string{"7", "8", "9"}

	// 全新建（无恢复源）：弃用拒——文案列合法值 + 弃用说明 + 灾备指引。
	err := AdmitCreateVersion("demo", deprecated, legal, false)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrVersionDeprecated, "fresh creates on deprecated versions are refused (distinguishable)")
	assert.Contains(t, err.Error(), "EOL 2027-01", "deprecation note rides the admission error")
	assert.Contains(t, err.Error(), "available", "error lists the legal version ids")
	assert.Contains(t, err.Error(), "disaster recovery", "error points at the same-version restore escape hatch")

	// 灾备义：同版本带 restore 的重建放行（版本轴由 RestoreCompat 另裁）。
	require.NoError(t, AdmitCreateVersion("demo", deprecated, legal, true),
		"same-version recreate with a restore source is the disaster-recovery escape hatch (ADR-0094 decision 6)")

	// 非弃用条目两态恒放行。
	fresh := dataservicetemplate.VersionSpec{ID: "9", Tag: "9-x", Digest: "sha256:" + strings.Repeat("c", 64), Default: true}
	require.NoError(t, AdmitCreateVersion("demo", fresh, legal, false))
	require.NoError(t, AdmitCreateVersion("demo", fresh, legal, true))
}

func TestRestoreCompatRules(t *testing.T) {
	t.Run("same version passes", func(t *testing.T) {
		require.NoError(t, RestoreCompat("postgres", "17", "17"))
		require.NoError(t, RestoreCompat("pgvector", "0.8-pg18", "0.8-pg18"))
	})

	t.Run("numeric engines upgrade-only", func(t *testing.T) {
		require.NoError(t, RestoreCompat("postgres", "16", "17"))
		require.NoError(t, RestoreCompat("postgres", "16", "18"))
		require.NoError(t, RestoreCompat("mysql", "8.4", "9"))
		require.NoError(t, RestoreCompat("redis", "7.4", "8.2"))
		require.NoError(t, RestoreCompat("mongo", "8.0", "8.2"))
		for _, tc := range [][2]string{{"17", "16"}, {"18", "16"}, {"9", "8.4"}, {"8.2", "7.4"}} {
			err := RestoreCompat("postgres", tc[0], tc[1])
			require.Error(t, err, "downgrade %s -> %s must be refused", tc[0], tc[1])
			assert.Contains(t, err.Error(), "downgrade")
		}
		// mysql 的降级文案引擎名无关（numericOnlyUp 共用核）。
		err := RestoreCompat("mysql", "9", "8.4")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "9 -> 8.4")
	})

	t.Run("pgvector axes compared independently", func(t *testing.T) {
		// pg 轴升、vector 轴平：放行。
		require.NoError(t, RestoreCompat("pgvector", "0.8-pg17", "0.8-pg18"))
		// 两轴同升：放行（未来 vector minor 追新）。
		require.NoError(t, RestoreCompat("pgvector", "0.8-pg17", "0.9-pg18"))
		// 任一轴降即拒（红队 A10/B-AT-13：pg 降=数据面不兼容；vector 降=
		// 扩展 ABI 退回）。
		for _, tc := range [][2]string{{"0.8-pg18", "0.8-pg17"}, {"0.9-pg18", "0.8-pg18"}} {
			err := RestoreCompat("pgvector", tc[0], tc[1])
			require.Error(t, err, "axis downgrade %s -> %s must be refused", tc[0], tc[1])
			assert.Contains(t, err.Error(), "composite id")
		}
		// 混合升降（一轴升一轴降）：拒——分轴各自只升。
		require.Error(t, RestoreCompat("pgvector", "0.8-pg18", "0.9-pg17"),
			"a pg-major downgrade is not bought back by a vector-minor upgrade")
	})

	t.Run("malformed ids refuse precisely", func(t *testing.T) {
		err := RestoreCompat("postgres", "seventeen", "18")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a numeric engine version id")
		err = RestoreCompat("pgvector", "0.8", "0.8-pg18")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "composite version id")
	})

	t.Run("unknown engine refuses", func(t *testing.T) {
		err := RestoreCompat("oracle", "18", "19")
		require.Error(t, err)
		require.ErrorIs(t, err, ErrUnknownEngine)
	})
}
