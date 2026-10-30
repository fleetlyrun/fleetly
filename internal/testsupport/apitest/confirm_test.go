package apitest_test

// B1 强制确认全链测试（设计稿 2026-10-30 §1.2/§1.4）：REQUIRED/skip/无效
// 票/带票成功、链序（idem 重放先于 confirm——重放不验票不烧票）、
// Rollback/DeleteApp 强制腿、webhook 豁免腿（webhook 路径不经 API 拦截器
// 天然豁免——无 confirmation 正常受理）。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	revisionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// yesSkip 是既有测试的脚本形态确认（--yes 显式通道）：B1 默认强制立法后，
// 直发形态的既有用例（部署链/投影/收敛等行为锚）以 skip 放行——与生产
// CLI 的过渡形态同款（审计 proof=skipped 照落，取证面可辨）。
func yesSkip() *deliveryv1.Confirmation { return &deliveryv1.Confirmation{Skip: true} }

// confirmDeploySpec 经 skip 部署一次目标 spec，返回其冻结行的规范形与
// digest（预览侧事实的夹具形态：W2a 铸票面在此模拟——同参数冻结 + base
// 快照）。
func confirmDeploySpec(t *testing.T, h *apitest.Harness, appID, image string) (string, string) {
	t.Helper()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	resp, err := dc.Deploy(h.Ctx(), &deliveryv1.DeployRequest{
		AppId: appID, Image: image, ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.NoError(t, err)
	rev, err := revisionrepo.New(h.DB.Clock(), h.DB.Runner()).Get(context.Background(), resp.GetDeployment().GetToRevision())
	require.NoError(t, err)
	return string(rev.Spec), rev.Digest
}

// mintDeployTicket 铸一枚绑定当前世界（Latest 冻结体 = 该 spec 自身、空
// 变量环境、无 hook、无成功部署）与请求者（bootstrap Token 身份——票不
// 得跨操作者，§1.4 ③）的 Deploy 票。
func mintDeployTicket(t *testing.T, h *apitest.Harness, appID string, specBody, frozenDigest string) string {
	t.Helper()
	// 请求者身份快照：夹具的首个 Token 即 bootstrap Token（h.Ctx() 的
	// Bearer；bootstrap Token 无属主用户——user_id NULL 归空串，与 authn
	// Identity 的解析形态一致）。
	var tokenID, teamID string
	var userID sql.NullString
	require.NoError(t, h.DB.Runner().QueryRowContext(
		context.Background(), `SELECT id, user_id, team_id FROM tokens ORDER BY created_at LIMIT 1`).
		Scan(&tokenID, &userID, &teamID))
	ticket, _, err := h.Services.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          "/fleetly.delivery.v1.DeploymentsService/Deploy",
		TargetApp:     appID,
		RequestDigest: governance.ConfirmRequestDigest([]byte(specBody), governance.ConfirmSemantics{}),
		Base: governance.ConfirmBase{
			LatestFrozenDigest: frozenDigest,
			VarEnvDigest:       governance.ConfirmVarEnvDigest(nil),
		},
		PreviewDigest: "preview-" + frozenDigest[:8],
		Operator:      governance.ConfirmOperator{TokenID: tokenID, UserID: userID.String, TeamID: teamID},
	})
	require.NoError(t, err)
	return ticket
}

// 无票直发拒（默认强制）；skip 逃生门放行；伪造票 MISMATCH；带票成功
// 且烧票（第二发同票 → 已确认过 MISMATCH）。
func TestDeployConfirmationFullChain(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "confirm-proj").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	req := &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27", ProcessName: "web"}

	// 无票无 skip：E_CONFIRMATION_REQUIRED。
	_, err := dc.Deploy(ctx, req)
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_REQUIRED", codeOf(t, err))

	// 伪造票：E_CONFIRMATION_MISMATCH（拦截器通用验票腿）。
	_, err = dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: "forged-token"},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_MISMATCH", codeOf(t, err))

	// skip 逃生门：受理成功（--yes）。
	_, err = dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.31", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.NoError(t, err)

	// 带真票：成功受理且票被烧。
	specBody, digest := confirmDeploySpec(t, h, appID, "nginx:1.27")
	ticket := mintDeployTicket(t, h, appID, specBody, digest)
	adm, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: ticket},
	})
	require.NoError(t, err, "a ticket matching the frozen payload and the preview-time world must admit")
	assert.Equal(t, "merged", adm.GetAdmission().GetOutcome(), "the queued skip deploy is merged away by latest-wins")

	// 同票重发（不同幂等键）：tombstone 辨因 → 已确认过 MISMATCH。
	_, err = dc.Deploy(sdk.WithIdempotencyKey(ctx, "confirm-second"), &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: ticket},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_MISMATCH", codeOf(t, err))
}

// 链序断言：idem 命中重放先于 confirm——同键重放不验票不烧票（若
// confirm 在前，重放会撞已烧票 tombstone 被 MISMATCH 拒）。
func TestConfirmationChainOrderIdemReplayBeforeConfirm(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "confirm-chain").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	specBody, digest := confirmDeploySpec(t, h, appID, "nginx:1.27")
	ticket := mintDeployTicket(t, h, appID, specBody, digest)
	req := &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: ticket},
	}

	kctx := sdk.WithIdempotencyKey(ctx, "confirm-chain-1")
	d1, err := dc.Deploy(kctx, req)
	require.NoError(t, err, "the ticketed first admission must succeed and burn the ticket")

	// 同键同体重放：idem 拦截器在 confirm 之前——重放原始响应，不 peek
	// 票、不撞 tombstone。
	d2, err := dc.Deploy(kctx, req)
	require.NoError(t, err, "an idempotent replay must not reach the confirm face (chain order: idem → confirm)")
	assert.Equal(t, d1.GetDeployment().GetId(), d2.GetDeployment().GetId())

	list, err := dc.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Len(t, list.GetDeployments(), 2, "replay must not append a deployment (skip-fixture deploy + ticketed deploy)")
}

// Rollback 强制腿：无票拒（E_CONFIRMATION_REQUIRED）——RollbackRequest 携
// 带 confirmation 字段的拦截器执法。
func TestRollbackRequiresConfirmation(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "confirm-rb").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	_, err := dc.Rollback(ctx, &deliveryv1.RollbackRequest{AppId: appID})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_REQUIRED", codeOf(t, err))

	// skip 逃生门同形放行（无成功基线 → E_NO_BASELINE，但不是 REQUIRED——
	// 拦截器已放行、受理位给出诚实拒绝）。
	_, err = dc.Rollback(ctx, &deliveryv1.RollbackRequest{
		AppId:        appID,
		Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.Error(t, err)
	assert.Equal(t, "E_NO_BASELINE", codeOf(t, err))
}

// DeleteApp 强制腿：无票拒；skip 放行；LedgerOnly 审计行（proof=redeemed/
// skipped）与 app.delete 同事务落库。
func TestDeleteAppRequiresConfirmation(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "confirm-del").GetId(), "web").GetId()
	ac := structurev1.NewAppsServiceClient(h.Conn)

	_, err := ac.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_REQUIRED", codeOf(t, err))

	// skip 逃生门：拆除成功。
	_, err = ac.DeleteApp(ctx, &structurev1.DeleteAppRequest{
		Id:           appID,
		Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.NoError(t, err)
}

// webhook 豁免腿（设计稿 §1.2/§1.4）：webhook 路径不经 API 拦截器——
// push 触发的部署无 confirmation 正常受理（材料归 git ADR-0072，代码评审
// 即确认；ReceiveWebhook 不在 ConfirmableVerbs）。
func TestWebhookDeployExemptFromConfirmation(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "confirm-hook").GetId(), "web").GetId()
	set, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main", WatchPaths: []string{"web/"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, set.GetSecret())

	// push 触发（签名即凭证的匿名面）：受理成功、部署行落库——无任何
	// confirmation 参与。
	body := pushBody("refs/heads/main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "feat: one", "web/index.ts")
	accepted, err := hooks.ReceiveWebhook(context.Background(), &deliveryv1.ReceiveWebhookRequest{
		Token: set.GetSecret(), Payload: []byte(body), Event: "push",
		Delivery: "confirm-exempt-1", Signature: signPayload(set.GetSecret(), body),
	})
	require.NoError(t, err)
	assert.Equal(t, "accepted", accepted.GetStatus())

	list, err := deployments.ListDeployments(owner, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, list.GetDeployments(), 1,
		"the webhook-triggered deploy must be admitted without any confirmation (exemption family)")
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", list.GetDeployments()[0].GetCommitSha())
}
