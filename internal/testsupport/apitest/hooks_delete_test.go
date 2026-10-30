package apitest_test

// DeleteGitHook 语义钉测（W-8 三件套的 API 面）+ FB-1 级联回归：
// 删除通道此前不存在——SetGitHook 是全量 upsert，表达不了"解除"，材料
// 闸门成单向门（ADR-0073 在案项）。hook_deliveries.app_id REFERENCES
// app_hooks 且无 ON DELETE CASCADE，去重行不清会让 DeleteApp 撞 FOREIGN
// KEY constraint failed（staging 真机实咬，运维手工 sqlite 清表解围）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// TestDeleteGitHookSemantics：无 hook 先删 404（非幂等，误删可见）→ 配置
// → 删除 → 读/换面 404 → 事件与审计留痕 → 重配铸全新材料（旧 URL token
// 与 webhook secret 即死，接收面在活跃行里查 token）。
func TestDeleteGitHookSemantics(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "hookrm").GetId(), "web").GetId()

	// 无 hook 即删 → E_NOT_FOUND（与 Get 同形；配置面显式状态机）。
	_, err := hooks.DeleteGitHook(ctx, &deliveryv1.DeleteGitHookRequest{AppId: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")

	set, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)
	require.NotEmpty(t, set.GetSecret())

	_, err = hooks.DeleteGitHook(ctx, &deliveryv1.DeleteGitHookRequest{AppId: appID})
	require.NoError(t, err)

	// 读面与换面随即 404：hook 行随聚合移除。
	_, err = hooks.GetGitHook(ctx, &deliveryv1.GetGitHookRequest{AppId: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
	_, err = hooks.RotateHookToken(ctx, &deliveryv1.RotateHookTokenRequest{AppId: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")

	// 事件 + 审计双留痕（CRUD 事实默认进流）。
	evs, err := events.ListEvents(ctx, &telemetryv1.ListEventsRequest{Limit: 200})
	require.NoError(t, err)
	var deletedSeen bool
	for _, ev := range evs.GetEvents() {
		if ev.GetName() == "hook.deleted" {
			deletedSeen = true
			require.Contains(t, ev.GetPayload(), `"app_id":"`+appID+`"`)
		}
	}
	require.True(t, deletedSeen, "hook.deleted must be recorded as an event")
	entries, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: "hook.", Limit: 100})
	require.NoError(t, err)
	var deleteAudited bool
	for _, e := range entries.GetEntries() {
		if e.GetAction() == "hook.delete" {
			deleteAudited = true
		}
	}
	require.True(t, deleteAudited, "hook deletion must be audited")

	// 重配是全新铸造：新 secret 一次、token 前缀换代（旧串不可能复现）。
	reset, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)
	require.NotEmpty(t, reset.GetSecret(), "re-configuring after delete must mint a fresh token")
	require.NotEqual(t, set.GetHook().GetTokenPrefix(), reset.GetHook().GetTokenPrefix(),
		"the fresh token must not reuse the deleted one")
}

// TestDeleteAppWithHookDeliveriesCascades（FB-1 回归，编排层）：配置过
// GitHook 且有重投去重行的 App 删除必须整体成功——级联缺陷形态是
// tombstone 事务撞 FOREIGN KEY constraint failed（E_INTERNAL，App 删不掉）。
func TestDeleteAppWithHookDeliveriesCascades(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "hookcascade").GetId(), "web").GetId()
	_, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git",
	})
	require.NoError(t, err)

	// 植两行去重锚（webhook 重投的行形态；不经接收链——本测试钉的是
	// tombstone 事务的级联，不是 webhook 校验）。
	err = h.DB.Tx(ctx, func(run state.Runner) error {
		for _, d := range []string{"d-1", "d-2"} {
			if _, txErr := run.ExecContext(ctx,
				`INSERT INTO hook_deliveries (app_id, delivery, received_at) VALUES (?, ?, '2026-10-17T00:00:00Z')`,
				appID, d); txErr != nil {
				return txErr
			}
		}
		return nil
	})
	require.NoError(t, err)

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Confirmation: yesSkip(), Id: appID})
	require.NoError(t, err, "delete must cascade the hook aggregate (deliveries included), never fail on the FK")

	// hook 行随 tombstone 走：配置面 404（与接收面同语义）。
	_, err = hooks.GetGitHook(ctx, &deliveryv1.GetGitHookRequest{AppId: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
}
