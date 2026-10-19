package apitest_test

// 告警规则/状态 List 面 Team 过滤（R08-M2，S13）：非平台管理员只看到
// 本 Team App 的规则与状态（不泄露他队数量/ID）；平台 owner 全量；系统
// 内置行无 Team 轴，沿用既有全员可见语义（Console 平台告警卡不破坏）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// ruleIDs 投影规则 ID 集。
func ruleIDs(resp *telemetryv1.ListAlertRulesResponse) map[string]bool {
	out := map[string]bool{}
	for _, r := range resp.GetRules() {
		out[r.GetId()] = true
	}
	return out
}

// TestAlertRulesTeamFilter：两队各建规则 → 各自只见本队；owner 全量；
// 跨队指定 app_id 仍是 403（行级授权先于过滤，无存在性泄露）。
func TestAlertRulesTeamFilter(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	acmeTeam, adminCtx := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")

	alerting := telemetryv1.NewAlertingServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	// acme 队：项目 + App + 规则（admin 产品面铸造，落在本队）。
	acmeProj, err := projects.CreateProject(adminCtx, &structurev1.CreateProjectRequest{Name: "acme-alerting"})
	require.NoError(t, err)
	acmeApp, err := apps.CreateApp(adminCtx, &structurev1.CreateAppRequest{
		ProjectId: acmeProj.GetProject().GetId(), Name: "web",
	})
	require.NoError(t, err)
	acmeRule, err := alerting.CreateAlertRule(adminCtx, &telemetryv1.CreateAlertRuleRequest{
		AppId: acmeApp.GetApp().GetId(), Metric: "cpu_percent", Threshold: 90, ForSeconds: 300,
	})
	require.NoError(t, err)

	// default 队：项目 + App + 规则（owner）。
	defaultApp := h.CreateApp(t, h.CreateProject(t, "default-alerting").GetId(), "web")
	defaultRule, err := alerting.CreateAlertRule(owner, &telemetryv1.CreateAlertRuleRequest{
		AppId: defaultApp.GetId(), Metric: "memory_working_set_bytes", Threshold: 1, ForSeconds: 60,
	})
	require.NoError(t, err)

	// 同队普通 member（alerts:read）应看到与 admin 相同的本队目录。
	createUserAs(t, h, "acme-member", acmeTeam, identity.RoleMemberID, "acme-member-pass")
	memberCtx, _ := loginSession(t, h, "acme-member", "acme-member-pass")

	// ---- ListAlertRules：本队只见本队，owner 全量 ----
	adminRules, err := alerting.ListAlertRules(adminCtx, &telemetryv1.ListAlertRulesRequest{})
	require.NoError(t, err)
	assert.True(t, ruleIDs(adminRules)[acmeRule.GetRule().GetId()], "own-team rule must be visible")
	assert.False(t, ruleIDs(adminRules)[defaultRule.GetRule().GetId()], "other-team rule must not be visible")
	assert.Len(t, adminRules.GetRules(), 1, "other-team rule must not leak into the count")

	memberRules, err := alerting.ListAlertRules(memberCtx, &telemetryv1.ListAlertRulesRequest{})
	require.NoError(t, err)
	assert.Equal(t, ruleIDs(adminRules), ruleIDs(memberRules), "member sees the same team-scoped rules as its admin")

	ownerRules, err := alerting.ListAlertRules(owner, &telemetryv1.ListAlertRulesRequest{})
	require.NoError(t, err)
	assert.True(t, ruleIDs(ownerRules)[acmeRule.GetRule().GetId()] && ruleIDs(ownerRules)[defaultRule.GetRule().GetId()],
		"platform owner sees every team's rules")

	// ---- 跨队指定 app_id：行级授权 403（不是空列表，且不泄露行数） ----
	_, err = alerting.ListAlertRules(adminCtx, &telemetryv1.ListAlertRulesRequest{AppId: defaultApp.GetId()})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
	ownScoped, err := alerting.ListAlertRules(owner, &telemetryv1.ListAlertRulesRequest{AppId: acmeApp.GetApp().GetId()})
	require.NoError(t, err)
	require.Len(t, ownScoped.GetRules(), 1, "owner may read any app's rules by app_id")

	// ---- ListAlertStates：用户状态行同过滤；系统内置行全员可见 ----
	adminStates, err := alerting.ListAlertStates(adminCtx, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	userState := map[string]bool{}
	system := map[string]bool{}
	for _, s := range adminStates.GetStates() {
		if s.GetSystem() {
			system[s.GetRuleId()] = true
			continue
		}
		userState[s.GetRuleId()] = true
	}
	assert.True(t, userState[acmeRule.GetRule().GetId()])
	assert.False(t, userState[defaultRule.GetRule().GetId()], "other-team state must not be visible")
	assert.Len(t, userState, 1, "state count must not leak other-team rules")
	assert.True(t, system["platform-offsite-backup"] && system["converge-failed"],
		"built-in system rows stay platform-visible (existing Console behavior)")

	memberStates, err := alerting.ListAlertStates(memberCtx, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	assert.Len(t, memberStates.GetStates(), len(adminStates.GetStates()), "member sees the same team-scoped state page")

	ownerStates, err := alerting.ListAlertStates(owner, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	ownerUserState := map[string]bool{}
	for _, s := range ownerStates.GetStates() {
		if !s.GetSystem() {
			ownerUserState[s.GetRuleId()] = true
		}
	}
	assert.True(t, ownerUserState[acmeRule.GetRule().GetId()] && ownerUserState[defaultRule.GetRule().GetId()],
		"platform owner sees every team's states")
}
