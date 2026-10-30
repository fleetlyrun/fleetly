package apitest_test

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// AppVariable 面（ADR-0072 三级合成顶层）：Put/List/Delete 聚合动词 +
// 冻结咽喉三级合成——AppVariable 顶覆盖源声明 env、源声明 env 覆盖
// SharedVariable；变量轮换重发产生新 Revision（digest 覆盖合成值）。
func TestAppVariablesVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	appVars := structurev1.NewAppVariablesServiceClient(h.Conn)
	sharedVars := structurev1.NewSharedVariablesServiceClient(h.Conn)

	proj := h.CreateProject(t, "appvar")
	appID := h.CreateApp(t, proj.GetId(), "svc").GetId()

	// 三层同键：Shared 底、源声明 env 中、AppVariable 顶。
	_, err := sharedVars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetId(), Name: "TIER", Value: "shared",
	})
	require.NoError(t, err)
	_, err = sharedVars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetId(), Name: "SHARED_ONLY", Value: "yes",
	})
	require.NoError(t, err)
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{AppId: appID, Name: "TIER", Value: "appvar"})
	require.NoError(t, err)
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{AppId: appID, Name: "APP_ONLY", Value: "1"})
	require.NoError(t, err)

	list, err := appVars.ListAppVariables(ctx, &structurev1.ListAppVariablesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, list.GetVariables(), 2)

	// 部署（源声明 env TIER=intake）→ 冻结体 = 三级合成终态。
	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1,"env":{"TIER":"intake"}}]}`
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: body})
	require.NoError(t, err)

	frozen, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 1})
	require.NoError(t, err)
	env := frozen.GetSpec().GetProcesses()[0].GetEnv()
	assert.Equal(t, "appvar", env["TIER"], "AppVariable must override the source-declared env")
	assert.Equal(t, "1", env["APP_ONLY"])
	assert.Equal(t, "yes", env["SHARED_ONLY"], "shared keys absent from the source still land")

	// 轮换：改 AppVariable → 重发冻结 Spec → 新 Revision 取新值（顶层
	// 覆盖合成产生新 digest）。
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{AppId: appID, Name: "TIER", Value: "rotated"})
	require.NoError(t, err)
	redeploy := frozen.GetSpec()
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: marshalSpec(t, redeploy)})
	require.NoError(t, err)
	frozen2, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 2})
	require.NoError(t, err)
	assert.Equal(t, "rotated", frozen2.GetSpec().GetProcesses()[0].GetEnv()["TIER"])
	assert.NotEqual(t, frozen.GetRevision().GetDigest(), frozen2.GetRevision().GetDigest())

	// 删除 store 键 → 回落源声明值（中间层语义）。
	_, err = appVars.DeleteAppVariable(ctx, &structurev1.DeleteAppVariableRequest{AppId: appID, Name: "TIER"})
	require.NoError(t, err)
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: body})
	require.NoError(t, err)
	frozen3, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 3})
	require.NoError(t, err)
	assert.Equal(t, "intake", frozen3.GetSpec().GetProcesses()[0].GetEnv()["TIER"], "removing the store key falls back to the source-declared value")

	// 删不存在的名 → E_NOT_FOUND；幽灵 App → E_NOT_FOUND。
	_, err = appVars.DeleteAppVariable(ctx, &structurev1.DeleteAppVariableRequest{AppId: appID, Name: "NOPE"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{AppId: "01JD0APP0000000000000MISS", Name: "X", Value: "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")

	// 名字白名单（env 键形态）拒绝。
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{AppId: appID, Name: "bad-name", Value: "1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
}

// marshalSpec 用 protojson 序列化冻结体（Redeploy 同款第四源形态）。
func marshalSpec(t *testing.T, spec *specv1.AppSpec) string {
	t.Helper()
	data, err := protojson.Marshal(spec)
	require.NoError(t, err)
	return string(data)
}
