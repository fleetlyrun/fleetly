package apitest_test

// Spec 所有权分裂（ADR-0072）：push 合并保留 console 运行时改动（replicas/
// env/额外进程跨 push 存活 + ref 更新）；Deploy(spec_file) 对 hook 应用是
// 运行时 overlay（提交件 source 被忽略）；材料源硬闸门 E_APP_HOOK_OWNED
// + override_hook 一次性接管；webhook 冻结的 revision.create 审计行
// source=webhook（时序修复锚）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestSpecOwnershipSplit(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	appVars := structurev1.NewAppVariablesServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	p := h.CreateProject(t, "ownsplit")
	appID := h.CreateApp(t, p.GetId(), "gitapp").GetId()

	set, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/ownsplit.git", Branch: "main",
	})
	require.NoError(t, err)
	secret := set.GetSecret()

	push := func(delivery, commit string) *deliveryv1.ReceiveWebhookResponse {
		t.Helper()
		body := pushBody("refs/heads/main", commit, "feat", "src/a")
		resp, rerr := hooks.ReceiveWebhook(context.Background(), &deliveryv1.ReceiveWebhookRequest{
			Token: secret, Payload: []byte(body), Event: "push", Delivery: delivery, Signature: signPayload(secret, body),
		})
		require.NoError(t, rerr)
		require.Equal(t, "accepted", resp.GetStatus())
		return resp
	}
	revAt := func(seq int64) *deliveryv1.GetRevisionResponse {
		t.Helper()
		got, rerr := revisions.GetRevision(owner, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: seq})
		require.NoError(t, rerr)
		return got
	}

	// Push #1：首 push 纯合成（web/from_build、replicas 1）。
	push("own-1", "1111111111111111111111111111111111111111")
	r1 := revAt(1)
	require.Len(t, r1.GetSpec().GetProcesses(), 1)
	assert.Equal(t, "1111111111111111111111111111111111111111", r1.GetSpec().GetSource().GetGit().GetRef())
	assert.Equal(t, int64(1), r1.GetSpec().GetProcesses()[0].GetReplicas())
	assert.Equal(t, deliveryv1.SourceKind_SOURCE_KIND_GIT, r1.GetRevision().GetSourceKind())

	// App 级变量（git 应用的 env 之家）。
	_, err = appVars.PutAppVariable(owner, &structurev1.PutAppVariableRequest{AppId: appID, Name: "TIER", Value: "appvar"})
	require.NoError(t, err)

	// Console 运行时编辑（spec_file）：replicas 4 + 源声明 env + 额外进程，
	// 提交件故意携带过期 ref（overlay 必须丢弃）。
	edited := r1.GetSpec()
	edited.Processes[0].Replicas = 4
	edited.Processes[0].Env = map[string]string{"EDGE": "console"}
	edited.Processes = append(edited.Processes, &specv1.ProcessSpec{
		Name:        "worker",
		ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"},
		Command:     []string{"sleep", "3600"},
	})
	edited.Source.GetGit().Ref = "0000000000000000000000000000000000000000"
	body, err := protojson.Marshal(edited)
	require.NoError(t, err)
	_, err = deployments.Deploy(owner, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: string(body)})
	require.NoError(t, err)

	r2 := revAt(2)
	// 材料半边取冻结侧（旧 ref 丢弃）；运行时半边取提交件；AppVariable 顶层合成。
	assert.Equal(t, "1111111111111111111111111111111111111111", r2.GetSpec().GetSource().GetGit().GetRef(),
		"overlay must keep the frozen material and drop the submitted source")
	require.Len(t, r2.GetSpec().GetProcesses(), 2)
	assert.Equal(t, int64(4), r2.GetSpec().GetProcesses()[0].GetReplicas())
	assert.Equal(t, "console", r2.GetSpec().GetProcesses()[0].GetEnv()["EDGE"])
	assert.Equal(t, "appvar", r2.GetSpec().GetProcesses()[0].GetEnv()["TIER"], "AppVariable must synthesize on top")
	assert.Equal(t, "busybox:1.37", r2.GetSpec().GetProcesses()[1].GetImage())

	// Push #2：合并——新材料（ref 更新）+ 运行时半边存活 + store 重合成。
	push("own-2", "2222222222222222222222222222222222222222")
	r3 := revAt(3)
	assert.Equal(t, "2222222222222222222222222222222222222222", r3.GetSpec().GetSource().GetGit().GetRef(),
		"push must refresh the material half")
	require.Len(t, r3.GetSpec().GetProcesses(), 2, "console-declared runtime half must survive the push")
	assert.Equal(t, int64(4), r3.GetSpec().GetProcesses()[0].GetReplicas())
	assert.Equal(t, "console", r3.GetSpec().GetProcesses()[0].GetEnv()["EDGE"])
	assert.Equal(t, "appvar", r3.GetSpec().GetProcesses()[0].GetEnv()["TIER"], "app variables re-synthesize on every freeze")

	// 材料源硬闸门：image 直投拒收；override_hook 一次性接管。
	_, err = deployments.Deploy(owner, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_APP_HOOK_OWNED")
	takeover, err := deployments.Deploy(owner, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", OverrideHook: true})
	require.NoError(t, err)
	require.NotEmpty(t, takeover.GetDeployment().GetId())
	r4 := revAt(4)
	assert.Equal(t, deliveryv1.SourceKind_SOURCE_KIND_IMAGE, r4.GetRevision().GetSourceKind(),
		"override_hook takes material ownership for one deploy")

	// 非 hook 应用不受闸门（材料源直投照旧）。
	p2 := h.CreateProject(t, "ownsplit2")
	a2 := h.CreateApp(t, p2.GetId(), "plain")
	_, err = deployments.Deploy(owner, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: a2.GetId(), Image: "nginx:1.27"})
	require.NoError(t, err)

	// 审计时序修复锚：webhook 冻结新 Revision 的 revision.create 审计行
	// source=webhook、actor=hook:<App 名>（此前错标为 api）。
	entries, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Source: "webhook", Limit: 100})
	require.NoError(t, err)
	revCreateActor := ""
	for _, e := range entries.GetEntries() {
		if e.GetAction() == "revision.create" {
			revCreateActor = e.GetActor()
			break
		}
	}
	assert.Equal(t, "hook:gitapp", revCreateActor, "webhook-frozen revisions must carry the hook audit annotation")
}
