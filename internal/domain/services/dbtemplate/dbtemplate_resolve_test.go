package dbtemplate

// 版本解析纯核（resolveVersionSpec）的语义面白昼测试（B2a，ADR-0094 决策
// 2/3/6）：Deprecated 拒（Note 附带）、Default 缺席 = 注册表失不变量、空
// version = Default。合成表承载——首版矩阵无弃用条目（TestImageDigestsPinned
// 钉 Default 非弃用），真值域上的弃用语义经纯核钉死（scanEngineNameLiterals
// 红灯实验先例：纯核直测，不造真注册表条目）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVersionSpecSemantics(t *testing.T) {
	specs := []enginetemplate.VersionSpec{
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

	_, err = resolveVersionSpec("demo", specs, "8")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrVersionDeprecated, "deprecated versions refuse new bindings (distinguishable)")
	assert.Contains(t, err.Error(), "EOL 2027-01", "deprecation note rides the error (catalog/admission face)")
	assert.Contains(t, err.Error(), "available", "error lists the legal version ids")

	_, err = resolveVersionSpec("demo", specs, "6")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownVersion)
	assert.Contains(t, err.Error(), "7", "error lists every in-registry version id (deprecated ones included)")

	noDefault := []enginetemplate.VersionSpec{{ID: "7", Tag: "7-x", Digest: "sha256:" + strings.Repeat("a", 64)}}
	_, err = resolveVersionSpec("demo", noDefault, "")
	require.Error(t, err, "a registry row without a default is an invariant break, not a silent pick")
	assert.Contains(t, err.Error(), "no default version")
}
