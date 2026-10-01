package systemgrpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// 注意：本包测试只链接 systemgrpc → schema（builtin Spec 条目），
// 事件 payload 的全量贡献对账在 internal/assembly（组合根链接全部
// 贡献方后的完备性守卫）。

func testService() *Service { return New(buildinfo.BuildInfo{Version: "0.1.0-test"}) }

func TestGetSchemaListsEntriesSorted(t *testing.T) {
	resp, err := testService().GetSchema(context.Background(), &systemv1.GetSchemaRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetEntries())
	for i := 1; i < len(resp.GetEntries()); i++ {
		assert.Less(t, resp.GetEntries()[i-1].GetName(), resp.GetEntries()[i].GetName())
	}
	// schema_json 是可解析的 canonical JSON 且带方言自报键。
	var tree map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.GetEntries()[0].GetSchemaJson()), &tree))
	assert.Contains(t, tree, "$schema")
}

func TestExplainResolvesSpecKind(t *testing.T) {
	resp, err := testService().Explain(context.Background(), &systemv1.ExplainRequest{Resource: "app"})
	require.NoError(t, err)
	assert.Equal(t, "app", resp.GetEntry().GetName())
	assert.Equal(t, "spec", resp.GetEntry().GetKind())
	assert.NotEmpty(t, resp.GetEntry().GetSummary())
	assert.Contains(t, resp.GetEntry().GetSchemaJson(), `"type":"object"`)
}

func TestExplainRejectsUnknownAndEmpty(t *testing.T) {
	_, err := testService().Explain(context.Background(), &systemv1.ExplainRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")

	_, err = testService().Explain(context.Background(), &systemv1.ExplainRequest{Resource: "definitely.not-a-name"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	assert.Contains(t, err.Error(), "fleetly schema")
}
