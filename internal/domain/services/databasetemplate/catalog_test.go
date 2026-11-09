package databasetemplate

// Catalog 装配面的单测（B2c，ADR-0094 决策 4 / design §2.3）：database 类型
// 全员、排序稳定、dialect = engine 自名、能力面诚实缺省（migrate 恒 false
// 至统一转储契约落地）、投影不回头污染注册表（副本语义）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogDatabaseKind(t *testing.T) {
	entries := Catalog()
	require.Len(t, entries, len(Engines()), "one catalog entry per registered engine")

	// 引擎序 = Engines() 排序稳定（目录确定性）。
	for i, e := range entries {
		assert.Equal(t, Engines()[i], e.Engine, "catalog order must follow Engines() sort")
		assert.Equal(t, CatalogKind, e.Kind)
		assert.Equal(t, e.Engine, e.Dialect, "database kind dialect is the engine's own name")
		assert.NotZero(t, e.Port)
		// 每引擎版本矩阵非空且恰一 Default（门禁钉死的不变量在目录面复察）。
		defaults := 0
		for _, v := range e.Versions {
			if v.Default {
				defaults++
			}
		}
		assert.Equal(t, 1, defaults, "%s: exactly one default version", e.Engine)
		// 能力面诚实缺省：migrate（跨引擎统一转储契约）随后续批次开面。
		assert.True(t, e.Capabilities.Backup)
		assert.True(t, e.Capabilities.Browse)
		assert.True(t, e.Capabilities.RestoreCompat)
		assert.False(t, e.Capabilities.Migrate)
		assert.Empty(t, e.LicenseNote, "the database kind carries no license disclosure obligation")
	}

	// 投影副本语义：改目录条目不回写注册表（VersionsFor 二读零漂移）。
	entries[0].Versions[0].Deprecated = true
	again := Catalog()
	for _, e := range again {
		for _, v := range e.Versions {
			assert.False(t, v.Deprecated, "the registry carries no deprecated entries in the first matrix; catalog projection must not mutate it")
		}
	}
	// 旋钮 schema 形状抽查：postgres 三旋钮、class 值域合法（表单面消费前提）。
	var pg *CatalogEntry
	for i := range again {
		if again[i].Engine == "postgres" {
			pg = &again[i]
		}
	}
	require.NotNil(t, pg)
	require.Len(t, pg.Knobs, 3)
	for _, k := range pg.Knobs {
		assert.Contains(t, []dataservicetemplate.KnobClass{dataservicetemplate.KnobStatic, dataservicetemplate.KnobDynamic}, k.Class)
		assert.NotEmpty(t, k.Key)
	}
}
