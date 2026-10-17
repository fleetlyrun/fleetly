package apitest_test

// SubmitSpec 单咽喉的九条不变式（P3 批 7 第 3+4 步收官，ADR-0072/0074 追记
// 的验收面）：api Deploy 与 webhook/refresh 链全部切换 engine.SubmitSpec
// 之后，在真装配（bufconn + 真 engine + SQLite + 假 Provider）上逐条钉死
// 受理语义。每条断言点以"不变式N"显式标注：
//
//	① 同 body 内容寻址复用零新行/审计/seq
//	② 三级变量合成序 Shared < 源声明 env < AppVariable，digest 覆盖合成后全集
//	③ push = 新材料 + 冻结运行时；overlay = 反向且提交件 source 忽略
//	④ webhook 审计覆写先于冻结；复用不落审计
//	⑤ 幂等双层窗分立（拦截器重放窗 vs admission 活跃窗）
//	⑥ freeze Tx ≠ Submit Tx（冻结行存活于受理失败之后）
//	⑦ E_* 文案逐字节（E_APP_HOOK_OWNED/四源互斥/FrozenSpecError）
//	⑧ CLI golden + generate/console verify 零漂移——结论在批报告，不在本文件
//	⑨ freeze 段序经 writes kernel 单源（outbox 无 revision 流面事件 + 审计在场）

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// ---- 夹具助手（inv 前缀——避免与既有用例助手重名） ----

// invNewApp 建项目 + App，返回 appID 与 projectID。
func invNewApp(t *testing.T, h *apitest.Harness, projectName, appName string) (string, string) {
	t.Helper()
	projectID := h.CreateProject(t, projectName).GetId()
	appID := h.CreateApp(t, projectID, appName).GetId()
	return appID, projectID
}

// invHookApp 建项目 + App + 真 hook 链（SetGitHook），返回 appID 与 hook
// token 明文（push 验签凭证）。
func invHookApp(t *testing.T, h *apitest.Harness, projectName, appName, repo string) (string, string) {
	t.Helper()
	appID, _ := invNewApp(t, h, projectName, appName)
	set, err := deliveryv1.NewHooksServiceClient(h.Conn).SetGitHook(h.Ctx(), &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: repo, Branch: "main",
	})
	require.NoError(t, err)
	return appID, set.GetSecret()
}

// invPush 经 ReceiveWebhook 投递一次 push（真链：验签 → 过滤 → 摄取核），
// 断言 accepted 并返回响应。
func invPush(t *testing.T, h *apitest.Harness, secret, delivery, commit string) *deliveryv1.ReceiveWebhookResponse {
	t.Helper()
	body := pushBody("refs/heads/main", commit, "feat: invariant", "src/a.go")
	resp, err := deliveryv1.NewHooksServiceClient(h.Conn).ReceiveWebhook(context.Background(),
		&deliveryv1.ReceiveWebhookRequest{
			Token: secret, Payload: []byte(body), Event: "push", Delivery: delivery,
			Signature: signPayload(secret, body),
		})
	require.NoError(t, err)
	require.Equal(t, "accepted", resp.GetStatus())
	return resp
}

// invRevisionRows 读 App 全部 Revision 行（冻结行集合断言面）。
func invRevisionRows(t *testing.T, h *apitest.Harness, ctx context.Context, appID string) []revision.Revision {
	t.Helper()
	rows, err := h.Services.Revisions.ListByApp(ctx, appID, 0, 100)
	require.NoError(t, err)
	return rows
}

// invAuditRows 按动作前缀拉审计行。
func invAuditRows(t *testing.T, ctx context.Context, auditq identityv1.AuditQueryServiceClient, action string) []*identityv1.AuditEntry {
	t.Helper()
	resp, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: action, Limit: 1000})
	require.NoError(t, err)
	return resp.GetEntries()
}

// invCountAction 统计动作精确命中的审计行数。
func invCountAction(t *testing.T, ctx context.Context, auditq identityv1.AuditQueryServiceClient, action string) int {
	t.Helper()
	n := 0
	for _, e := range invAuditRows(t, ctx, auditq, action) {
		if e.GetAction() == action {
			n++
		}
	}
	return n
}

// invEventNames 全量拉事件名（分页游标）。
func invEventNames(t *testing.T, ctx context.Context, events telemetryv1.EventsServiceClient) []string {
	t.Helper()
	var names []string
	after := int64(0)
	for i := 0; i < 50; i++ {
		evs, err := events.ListEvents(ctx, &telemetryv1.ListEventsRequest{AfterSeq: after, Limit: 200})
		require.NoError(t, err)
		for _, ev := range evs.GetEvents() {
			names = append(names, ev.GetName())
		}
		if evs.GetLastSeq() == after {
			break
		}
		after = evs.GetLastSeq()
	}
	return names
}

// invStatusMessage 提取 gRPC status 完整 message（"E_*: 文案"逐字节比对面）。
func invStatusMessage(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	return st.Message()
}

// invFrozenEnv 回读指定 seq 冻结体的单进程 env（GetRevision 用户读面）。
func invFrozenEnv(t *testing.T, ctx context.Context, revisions deliveryv1.RevisionsServiceClient, appID string, seq int64, process string) map[string]string {
	t.Helper()
	resp, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: seq})
	require.NoError(t, err)
	for _, p := range resp.GetSpec().GetProcesses() {
		if p.GetName() == process {
			return p.GetEnv()
		}
	}
	t.Fatalf("process %q not found in revision seq %d", process, seq)
	return nil
}

// ---- 不变式①：同 body → FindByDigest 复用，零新行/审计/seq ----

func TestInvariant1ContentAddressedReuse(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)
	appID, _ := invNewApp(t, h, "inv1", "web")

	// 同 body 两次 deploy（无键无 commit——只考内容寻址复用本身）。
	req := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"}
	d1, err := deployments.Deploy(ctx, req)
	require.NoError(t, err)
	d2, err := deployments.Deploy(ctx, req)
	require.NoError(t, err)

	// 不变式①：revisions 恰一行（FindByDigest 复用，不冻新行）。
	rows := invRevisionRows(t, h, ctx, appID)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(1), rows[0].Seq, "不变式①：复用受理不推进 Revision seq")

	// 不变式①：revision.create 审计恰一行（复用零写零事实）。
	assert.Equal(t, 1, invCountAction(t, ctx, auditq, "revision.create"),
		"不变式①：reuse must not append a second revision.create audit row")

	// 不变式①：两条 Deployment 各自新行（Deployment 不复用），但锚定同一
	// Revision 行（同 id 同 seq）。
	deps, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, deps.GetDeployments(), 2, "不变式①：each submit admits its own deployment row")
	assert.NotEqual(t, d1.GetDeployment().GetId(), d2.GetDeployment().GetId())
	assert.Equal(t, rows[0].ID, d1.GetDeployment().GetToRevision())
	assert.Equal(t, rows[0].ID, d2.GetDeployment().GetToRevision(),
		"不变式①：both submissions anchor the identical frozen revision")
}

// ---- 不变式②：合成序 Shared < 源 env < AppVariable；digest 覆盖合成后全集 ----

func TestInvariant2SynthesisOrderAndDigestCoverage(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	sharedVars := structurev1.NewSharedVariablesServiceClient(h.Conn)
	appVars := structurev1.NewAppVariablesServiceClient(h.Conn)
	appID, projectID := invNewApp(t, h, "inv2", "web")

	// 三层同键 LAYER：SharedVariable 底、image 源 env 直传中、AppVariable 顶；
	// 另设 Shared 独有键证共享层在场。
	_, err := sharedVars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: projectID, Name: "LAYER", Value: "from-shared",
	})
	require.NoError(t, err)
	_, err = sharedVars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: projectID, Name: "ONLY_SHARED", Value: "s-only",
	})
	require.NoError(t, err)
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{
		AppId: appID, Name: "LAYER", Value: "from-app-v1",
	})
	require.NoError(t, err)

	req := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27", Env: map[string]string{"LAYER": "from-source"}}
	_, err = deployments.Deploy(ctx, req)
	require.NoError(t, err)

	// 不变式②：冻结体 env = 合成后最终生效集，同键由顶层 AppVariable 胜出
	//（Shared 底 < 源声明 env 中 < AppVariable 顶）。
	env := invFrozenEnv(t, ctx, revisions, appID, 1, "web")
	assert.Equal(t, "from-app-v1", env["LAYER"], "不变式②：AppVariable overrides shared and source layers")
	assert.Equal(t, "s-only", env["ONLY_SHARED"], "不变式②：shared layer synthesizes in")

	// 合成输入变化（AppVariable 轮换）+ 同 body 重发：digest 覆盖合成后全集
	// ——合成输入变化即新内容，冻结新行。
	_, err = appVars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{
		AppId: appID, Name: "LAYER", Value: "from-app-v2",
	})
	require.NoError(t, err)
	_, err = deployments.Deploy(ctx, req)
	require.NoError(t, err)

	rows := invRevisionRows(t, h, ctx, appID)
	require.Len(t, rows, 2, "不变式②：synthesis input change must freeze a new revision")
	assert.Equal(t, "from-app-v2", invFrozenEnv(t, ctx, revisions, appID, 2, "web")["LAYER"],
		"不变式②：the new freeze carries the rotated value")
	assert.Equal(t, "from-app-v1", invFrozenEnv(t, ctx, revisions, appID, 1, "web")["LAYER"],
		"不变式②：the old frozen revision is immutable")
}

// ---- 不变式③：push = 新材料 + 冻结运行时；overlay = 反向且提交件 source 忽略 ----

func TestInvariant3MergeDirectionsEndToEnd(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	const repo = "https://github.com/acme/inv3.git"
	appID, secret := invHookApp(t, h, "inv3", "gitapp", repo)

	revAt := func(seq int64) *specv1.AppSpec {
		t.Helper()
		resp, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: seq})
		require.NoError(t, err)
		return resp.GetSpec()
	}

	// push #1：首触发无冻结体 = 纯合成（web/from_build、replicas 1）。
	const commit1 = "3111111111111111111111111111111111111111"
	invPush(t, h, secret, "inv3-1", commit1)
	r1 := revAt(1)
	assert.Equal(t, commit1, r1.GetSource().GetGit().GetRef(), "不变式③：first push takes the submission material as-is")
	require.Len(t, r1.GetProcesses(), 1)
	assert.Equal(t, int64(1), r1.GetProcesses()[0].GetReplicas())

	// spec_file overlay（hook 应用的运行时写面）：replicas 4 + 额外进程，
	// 提交件故意携带过期 ref——不变式③ overlay 方向：材料半边取冻结侧
	//（提交件 source 被忽略，防旧 ref 构建旧 commit）、运行时半边取提交件。
	edited := r1
	edited.Processes[0].Replicas = 4
	edited.Processes = append(edited.Processes, &specv1.ProcessSpec{
		Name:        "worker",
		ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"},
		Command:     []string{"sleep", "3600"},
	})
	edited.Source.GetGit().Ref = "0000000000000000000000000000000000000000"
	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, SpecFile: marshalSpec(t, edited)})
	require.NoError(t, err)

	r2 := revAt(2)
	assert.Equal(t, commit1, r2.GetSource().GetGit().GetRef(),
		"不变式③：overlay keeps the frozen material half and drops the submitted source")
	require.Len(t, r2.GetProcesses(), 2, "不变式③：overlay takes the runtime half from the submission")
	assert.Equal(t, int64(4), r2.GetProcesses()[0].GetReplicas())
	assert.Equal(t, "busybox:1.37", r2.GetProcesses()[1].GetImage())

	// push #2：不变式③ push 方向——材料半边 = 提交件（新 ref/build），运行时
	// 半边 = 冻结体（replicas 4 与额外进程跨 push 存活）。
	const commit2 = "3222222222222222222222222222222222222222"
	invPush(t, h, secret, "inv3-2", commit2)
	r3 := revAt(3)
	assert.Equal(t, commit2, r3.GetSource().GetGit().GetRef(),
		"不变式③：push takes the material half from the submission")
	assert.Equal(t, r1.GetBuild().GetDockerfile(), r3.GetBuild().GetDockerfile(),
		"不变式③：build belongs to the material half (regenerated by the push)")
	require.Len(t, r3.GetProcesses(), 2, "不变式③：push keeps the runtime half from the frozen spec")
	assert.Equal(t, int64(4), r3.GetProcesses()[0].GetReplicas())
	assert.Equal(t, "busybox:1.37", r3.GetProcesses()[1].GetImage())
}

// ---- 不变式④：webhook 审计覆写先于冻结；复用不落审计 ----

func TestInvariant4WebhookAuditOverrideBeforeFreeze(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)
	const repo = "https://github.com/acme/inv4.git"
	appID, secret := invHookApp(t, h, "inv4", "audithook", repo)

	const commit = "4111111111111111111111111111111111111111"
	first := invPush(t, h, secret, "inv4-1", commit)
	require.NotEmpty(t, first.GetDeploymentId())

	// 不变式④：revision.create 审计行 actor=hook:<App 名> 且 source=webhook
	//——若覆写晚于冻结注入，该行会错标为 api（ADR-0072 批修复的时序锚）。
	revCreates := invAuditRows(t, ctx, auditq, "revision.create")
	require.Len(t, revCreates, 1)
	assert.Equal(t, "hook:audithook", revCreates[0].GetActor(), "不变式④：actor must carry the hook override")
	assert.Equal(t, "webhook", revCreates[0].GetSource(), "不变式④：source must carry the webhook override")
	// 覆写同 ctx 流入 admission——deployment.create 审计行同形。
	depCreates := invAuditRows(t, ctx, auditq, "deployment.create")
	require.Len(t, depCreates, 1)
	assert.Equal(t, "hook:audithook", depCreates[0].GetActor())
	assert.Equal(t, "webhook", depCreates[0].GetSource())

	// 同 commit 重投（新 delivery id）：admission deduplicated；冻结命中内容
	// 寻址复用——不变式④：零新 revision.create 审计、零新冻结行。
	again := invPush(t, h, secret, "inv4-2", commit)
	require.NotNil(t, again.GetAdmission())
	assert.Equal(t, "deduplicated", again.GetAdmission().GetOutcome(),
		"不变式④：same-commit redelivery dedups in admission")
	assert.Equal(t, first.GetDeploymentId(), again.GetDeploymentId())
	assert.Equal(t, 1, invCountAction(t, ctx, auditq, "revision.create"),
		"不变式④：reuse must not append a second revision.create audit row")
	assert.Len(t, invRevisionRows(t, h, ctx, appID), 1, "不变式④：redelivery freezes no new revision")
}

// ---- 不变式⑤：幂等双层窗分立（拦截器重放窗 vs admission 活跃窗） ----

func TestInvariant5DualIdempotencyWindows(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	appID, _ := invNewApp(t, h, "inv5", "web")

	// 窗一（拦截器 24h 窗）：同 IdempotencyKey 同 body 两次——第二响应逐字节
	// 重放首响应（不达受理面）。
	req := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.30"}
	kctx := sdk.WithIdempotencyKey(ctx, "inv5-key")
	resp1, err := deployments.Deploy(kctx, req)
	require.NoError(t, err)
	resp2, err := deployments.Deploy(kctx, req)
	require.NoError(t, err)
	assert.True(t, proto.Equal(resp1, resp2),
		"不变式⑤：keyed replay must replay the first response byte-for-byte")
	deps, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, deps.GetDeployments(), 1, "不变式⑤：replay admits no second deployment")

	// 窗二（admission 活跃窗）：同 body 无键两次——键缺席则拦截器窗不咬，
	// 每发各得自己的 Deployment 行（活跃期 latest-wins 合并语义照常），冻结
	// 层内容寻址复用。
	resp3, err := deployments.Deploy(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp3.GetAdmission(), "不变式⑤：unkeyed submits still get an admission verdict")
	resp4, err := deployments.Deploy(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp4.GetAdmission())
	assert.NotEqual(t, resp3.GetDeployment().GetId(), resp4.GetDeployment().GetId(),
		"不变式⑤：unkeyed resubmits each admit a fresh deployment")

	deps, err = deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Len(t, deps.GetDeployments(), 3,
		"不变式⑤：one keyed + two unkeyed deployment rows (the two windows are independent)")
	assert.Len(t, invRevisionRows(t, h, ctx, appID), 1,
		"不变式⑤：identical body reuses the frozen revision across all submits")
}

// ---- 不变式⑥：freeze Tx ≠ Submit Tx（冻结行存活于受理失败之后） ----

func TestInvariant6FreezeTxOutlivesSubmitTx(t *testing.T) {
	// QueueCapacity=1 注入小容量 engine（手动驱动：首条部署恒驻 queued）。
	h := apitest.NewManualOpts(t, func(o *engine.Options) { o.Deploy.QueueCapacity = 1 })
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	appID, _ := invNewApp(t, h, "inv6", "web")

	// 占满 per-App 队列（容量 1）。
	first, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:2.0"})
	require.NoError(t, err)

	// 第二发：冻结相先行（独立 Tx），admission 相命中 queue-full 拒绝——
	// 不变式⑥：E_QUEUE_FULL 到达调用方，但冻结行已落库存活。该行为正是
	// 禁止冻结与受理并单 Tx 的原因：并单则 queue-full 回滚连带丢弃冻结行，
	// 同 body 重发只能重冻结（内容寻址复用窗被受理失败撕开）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:2.1"})
	require.Error(t, err)
	assert.Equal(t, "E_QUEUE_FULL", codeOf(t, err), "不变式⑥：queue-full is the explicit backpressure code")

	rows := invRevisionRows(t, h, ctx, appID)
	require.Len(t, rows, 2, "不变式⑥：the frozen revision must outlive the failed admission")
	frozen, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 2})
	require.NoError(t, err)
	assert.Equal(t, "nginx:2.1", frozen.GetSpec().GetSource().GetImage().GetRef(),
		"不变式⑥：the surviving row is the submission frozen before admission rejected it")
	deps, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Len(t, deps.GetDeployments(), 1, "不变式⑥：the failed admission leaves no deployment row")

	// 腾空队列后同 body 重发：命中既有冻结行复用（零新冻结）——受理失败
	// 之后的冻结行是可复用资产。
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: first.GetDeployment().GetId()})
	require.NoError(t, err)
	third, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:2.1"})
	require.NoError(t, err)
	assert.Len(t, invRevisionRows(t, h, ctx, appID), 2,
		"不变式⑥：resubmitting the same body reuses the surviving frozen row")
	assert.Equal(t, frozen.GetRevision().GetId(), third.GetDeployment().GetToRevision(),
		"不变式⑥：the new deployment anchors the revision frozen before the earlier failure")
}

// ---- 不变式⑦：E_* 文案逐字节 ----

func TestInvariant7UserVisibleErrorTextsByteStable(t *testing.T) {
	t.Run("hook-owned gate", func(t *testing.T) {
		h := apitest.NewManual(t)
		ctx := h.Ctx()
		deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
		const repo = "https://github.com/acme/inv7gate.git"
		appID, _ := invHookApp(t, h, "inv7a", "gitapp", repo)

		// 不变式⑦：E_APP_HOOK_OWNED 全文案逐字节（用户可见行为零变化）。
		_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
		require.Error(t, err)
		assert.Equal(t, "E_APP_HOOK_OWNED", codeOf(t, err))
		assert.Equal(t,
			"E_APP_HOOK_OWNED: app "+appID+" is deployed from a git hook (repo "+repo+"); "+
				"material sources are hook-owned — runtime changes go through spec_file, "+
				"env through app variables, and material through the repo",
			invStatusMessage(t, err), "不变式⑦：E_APP_HOOK_OWNED full text must stay byte-identical")
	})

	t.Run("four-source mutual exclusion", func(t *testing.T) {
		h := apitest.NewManual(t)
		ctx := h.Ctx()
		deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
		appID, _ := invNewApp(t, h, "inv7b", "web")

		// 不变式⑦：四源互斥全文案逐字节。
		_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
			AppId: appID, Image: "nginx:1.27", ComposeYaml: "services:\n  web:\n    image: nginx:1.27\n",
		})
		require.Error(t, err)
		assert.Equal(t, "E_INVALID_ARGUMENT", codeOf(t, err))
		assert.Equal(t,
			"E_INVALID_ARGUMENT: image, compose_yaml, upload_id and spec_file are mutually exclusive; "+
				"exactly one source form per deploy",
			invStatusMessage(t, err), "不变式⑦：four-source exclusion full text must stay byte-identical")
	})

	t.Run("frozen spec corruption", func(t *testing.T) {
		h := apitest.NewManual(t)
		ctx := h.Ctx()
		deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
		revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
		const repo = "https://github.com/acme/inv7c.git"
		appID, secret := invHookApp(t, h, "inv7c", "gitapp", repo)

		// 先冻结一份完整体并取其 protojson（spec_file 受理前置要过解析），
		// 再直改 DB 把冻结体换成坏 protojson。
		invPush(t, h, secret, "inv7c-1", "7111111111111111111111111111111111111111")
		got, err := revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: appID, Seq: 1})
		require.NoError(t, err)
		body := marshalSpec(t, got.GetSpec())
		_, err = h.DB.Runner().ExecContext(context.Background(),
			"UPDATE revisions SET spec = ? WHERE app_id = ?", "{not-protojson", appID)
		require.NoError(t, err)

		// 不变式⑦（errors 面精确比对）：同一损坏场景在受理咽喉的 Go error
		// 面上恰为全句 "hook-owned overlay: frozen revision is not valid
		// protojson (...)"——api mapStateError 以该 err.Error() 原文铸
		// E_INTERNAL message（切换前后逐字节同形）。
		_, _, _, err = h.Engine.SubmitSpec(context.Background(), engine.SubmitSpecRequest{
			AppID: appID, Spec: got.GetSpec(), HookOwned: true, Merge: engine.MergeOverlay,
		})
		var fse *engine.FrozenSpecError
		require.ErrorAs(t, err, &fse)
		assert.Equal(t, "hook-owned overlay: frozen revision is not valid protojson ("+fse.Err.Error()+")",
			fse.Error(), "不变式⑦：FrozenSpecError sentence must stay byte-identical")

		// 不变式⑦（用户可见面）：spec_file overlay deploy 过 api 层同一映射
		// 落 E_INTERNAL；出口纪律（P0-3，仅 E_INTERNAL）把 message 收口为脱敏
		// 文案——切换前后线上形态逐字节同形（全句只落服务端日志）。
		_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, SpecFile: body})
		require.Error(t, err)
		assert.Equal(t, "E_INTERNAL", codeOf(t, err))
		assert.Equal(t, "E_INTERNAL: internal server error", invStatusMessage(t, err),
			"不变式⑦：the wire face of E_INTERNAL stays byte-identical (exit redaction)")
		assert.NotContains(t, invStatusMessage(t, err), "protojson", "不变式⑦：cause text must not leak to the wire")
	})
}

// ---- 不变式⑨：freeze 段序经 writes kernel 单源（运行时投影面） ----
//
// 分工注记：kernel 单源（事实双行配对、段序、LedgerOnly 偏离纪律）的静态
// 面由 internal/guards 的 factappend/factdual 执法；本测试钉的是运行时
// 投影——deploy 之后 outbox 流面无 revision 相关事件（偏离仅在取证面），
// 而审计行在场（取证面完整）。

func TestInvariant9FreezeFactsLedgerOnly(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)
	appID, _ := invNewApp(t, h, "inv9", "web")

	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)

	// 不变式⑨：outbox 流面零 revision 事件（LedgerOnly 偏离——材料面不是
	// 状态机）；部署事件照常在流。
	names := invEventNames(t, ctx, events)
	for _, n := range names {
		assert.False(t, strings.HasPrefix(n, "revision"),
			"不变式⑨：revision freeze must not emit stream events, got %q", n)
	}
	assert.Contains(t, names, "deployment.queued", "不变式⑨：the deployment lifecycle event stays on the stream")

	// 不变式⑨：审计行在场（取证面）——revision.create 恰一条。
	revCreates := invAuditRows(t, ctx, auditq, "revision.create")
	require.Len(t, revCreates, 1)
	assert.Equal(t, "revision.create", revCreates[0].GetAction())
	assert.True(t, strings.HasPrefix(revCreates[0].GetResource(), "revision/"),
		"不变式⑨：the audit row anchors the frozen revision resource")
}
