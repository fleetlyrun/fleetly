package spec

// dokploy 迁移计划纯叶子单测（P0-7.6）：未消解插值孔的 skip 理由必须陈述
// 现役语义（受理面 fail-closed，ADR-0074 决策 5）——已废除的"落字面值"
// 旧口径文案不得回流（golden 双形态在 cmd 侧另行钉死输出面）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDokployUnresolvedVarSkipReason(t *testing.T) {
	body := []byte(`{"compose":[{"composeId":"cmp-1","name":"holey",` +
		`"composeContent":"services:\n  web:\n    image: nginx:1.27\n    environment:\n      X: ${MISSING}\n"}]}`)
	plan, err := ParseDokploy(body)
	require.NoError(t, err)
	require.Len(t, plan.Skipped, 1)
	require.Empty(t, plan.Apps, "unresolved hole skips the whole compose item")

	reason := plan.Skipped[0].Reason
	assert.Contains(t, reason, "MISSING")
	assert.Contains(t, reason, "rejects unresolved variables at admission",
		"skip reason must state the live fail-closed admission semantics (ADR-0074 decision 5)")
	assert.False(t, strings.Contains(reason, "literal values"),
		"the abolished lands-as-literal wording must not resurface")
}

// TestParseDokployEnvPanelSubstitution：env 面供值的孔在客户端消解——
// 到达受理面已无孔（ADR-0074 决策 5 迁移路径不变）。
func TestParseDokployEnvPanelSubstitution(t *testing.T) {
	body := []byte(`{"compose":[{"composeId":"cmp-1","name":"sealed",` +
		`"composeContent":"services:\n  web:\n    image: nginx:1.27\n    environment:\n      X: ${PG_URL}\n",` +
		`"env":"PG_URL=postgres://db"}]}`)
	plan, err := ParseDokploy(body)
	require.NoError(t, err)
	require.Empty(t, plan.Skipped)
	require.Len(t, plan.Apps, 1)
	assert.Contains(t, plan.Apps[0].ComposeYAML, "postgres://db")
	assert.Equal(t, map[string]string{"PG_URL": "postgres://db"}, plan.Apps[0].Env)
}
