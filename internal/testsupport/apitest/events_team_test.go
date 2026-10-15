package apitest_test

// 事件面 Team 行级过滤矩阵（P0-2，S2 跨队事件流泄漏止血验收）：
// B 队 member 只见本队 + 平台级（team_id=''）事件，看不见 A 队的
// app/deployment 类事件；A 队成员照常可见本队事件（防过紧）；平台 owner
// 全量可见。三读径同锚：ListEvents / StreamEvents / SSE 票据（payload 绑
// 铸造者 Team 可见域）。断档 410 语义不变（既有 events 测试钉死，本文件
// 只钉过滤面）。

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// collectEvents 拉取一方的全量事件（ListEvents 分页收敛；返回 name/聚合
// ID 的集合形态断言用）。
func collectEvents(t *testing.T, ctx context.Context, events telemetryv1.EventsServiceClient) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	after := int64(0)
	for {
		resp, err := events.ListEvents(ctx, &telemetryv1.ListEventsRequest{AfterSeq: after, Limit: 1000})
		require.NoError(t, err)
		for _, ev := range resp.GetEvents() {
			got[ev.GetName()+"/"+ev.GetAggregateId()] = true
			after = ev.GetSeq()
		}
		if len(resp.GetEvents()) == 0 {
			return got
		}
	}
}

func TestEventsTeamFilterMatrix(t *testing.T) {
	h := apitest.New(t)
	anon := context.Background()
	owner := sdk.WithToken(anon, h.Token)

	// 夹具：A = default Team（owner 铺资源）；B = "acme" + member Token
	//（events:read 在 member 角色的业务资源读权内）。
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	teamB, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "acme"})
	require.NoError(t, err)
	bID := teamB.GetTeam().GetId()
	bMember := sdk.WithToken(anon, mintTeamToken(t, h, "acme-member", bID, identity.RoleMemberID))
	aMember := sdk.WithToken(anon, mintTeamToken(t, h, "shop-dev", identity.DefaultTeamID, identity.RoleMemberID))

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	tasks := automationv1.NewTasksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	// ---- A 队资源（owner 代铺）：project + app + task + deployment ----
	projA, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	projAID := projA.GetProject().GetId()
	appA, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projAID, Name: "web"})
	require.NoError(t, err)
	appAID := appA.GetApp().GetId()
	taskA, err := tasks.CreateTask(owner, &automationv1.CreateTaskRequest{ProjectId: projAID, Name: "migrate", Image: "nginx:1.27"})
	require.NoError(t, err)
	depA, err := deployments.Deploy(owner, &deliveryv1.DeployRequest{AppId: appAID, Image: "nginx:1.27"})
	require.NoError(t, err)
	depAID := depA.GetDeployment().GetId()

	// ---- B 队资源：owner 显式落队（member 只持读权——订阅面验收用） ----
	projB, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "acme-core", TeamId: bID})
	require.NoError(t, err)
	require.Equal(t, bID, projB.GetProject().GetTeamId())
	appB, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projB.GetProject().GetId(), Name: "api"})
	require.NoError(t, err)

	// ---- 四族 CRUD 事件夹具（P2 批二补事件批）：A/B 双队各铺 hook 与
	// alert_rule；github_app 落 A 队项目（Project 级材料）；channel 是平台级
	// 材料（'' 轴——双队均应可见）。 ----
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	alerting := telemetryv1.NewAlertingServiceClient(h.Conn)
	_, err = hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{AppId: appAID, Repo: "https://github.com/acme/shop-web.git"})
	require.NoError(t, err)
	_, err = hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{AppId: appB.GetApp().GetId(), Repo: "https://github.com/acme/api.git"})
	require.NoError(t, err)
	_, err = hooks.RotateHookToken(owner, &deliveryv1.RotateHookTokenRequest{AppId: appB.GetApp().GetId()})
	require.NoError(t, err)
	ruleA, err := alerting.CreateAlertRule(owner, &telemetryv1.CreateAlertRuleRequest{AppId: appAID, Metric: "cpu_percent", Threshold: 90})
	require.NoError(t, err)
	ruleB, err := alerting.CreateAlertRule(owner, &telemetryv1.CreateAlertRuleRequest{AppId: appB.GetApp().GetId(), Metric: "memory_working_set_bytes", Threshold: 80})
	require.NoError(t, err)
	fakeGH := newFakeGitHub(t, h)
	ghReq := putReq(projAID, fakeGH.base)
	ghReq.PrivateKeyPem = string(fakeGH.pem)
	ghApp, err := deliveryv1.NewGitHubAppsServiceClient(h.Conn).SetGitHubApp(owner, ghReq)
	require.NoError(t, err)
	channel, err := alerting.CreateNotificationChannel(owner, &telemetryv1.CreateNotificationChannelRequest{
		Name: "ops", Kind: "webhook", Url: "http://127.0.0.1:9/x",
	})
	require.NoError(t, err)

	// ---- 平台级事件（team_id=''，白箱直落——node.* 域在生产由 engine 铸） ----
	_, err = h.Services.OutboxEvents.Append(context.Background(), h.DB.Runner(), "",
		"node.joined", "node", "01JD0NODE0000000000000000X", []byte(`{}`))
	require.NoError(t, err)

	// engine 驱动异步：等到 A 的 deployment 事件全局在场（owner 视角），
	// 再钉 B 视角的"看不见"才有意义。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ownerView := collectEvents(t, owner, events)
		for key := range ownerView {
			if strings.HasPrefix(key, "deployment.") && strings.HasSuffix(key, depAID) {
				goto deployed
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no deployment event appeared within 5s (owner view)")
deployed:

	// ---- B 队 member：本队 + 平台级可见；A 队 app/deployment 类不可见 ----
	bView := collectEvents(t, bMember, events)
	assert.Contains(t, bView, "project.created/"+projB.GetProject().GetId(), "team B sees its own project events")
	assert.Contains(t, bView, "app.created/"+appB.GetApp().GetId(), "team B sees its own app events")
	assert.Contains(t, bView, "node.joined/01JD0NODE0000000000000000X", "platform-level ('') events are visible to every team")
	// 四族双队断言（P2 批二）：本队 hook/alert_rule 事件可见；channel 是
	// 平台级材料（'' 轴）全员可见；A 队的 hook/alert_rule/github_app 事件
	// 不可见。
	assert.Contains(t, bView, "hook.created/"+appB.GetApp().GetId(), "team B sees its own hook.created events")
	assert.Contains(t, bView, "hook.rotated/"+appB.GetApp().GetId(), "team B sees its own hook.rotated events")
	assert.Contains(t, bView, "alert_rule.created/"+ruleB.GetRule().GetId(), "team B sees its own alert_rule.created events")
	assert.Contains(t, bView, "channel.created/"+channel.GetChannel().GetId(), "channel events are platform-level ('') and visible to every team")
	for key := range bView {
		assert.NotEqual(t, "project.created/"+projAID, key, "team A's project events must not leak")
		assert.NotEqual(t, "app.created/"+appAID, key, "team A's app events must not leak")
		assert.NotEqual(t, taskA.GetTask().GetId(), key, "team A's task events must not leak")
		assert.NotEqual(t, depAID, key, "team A's deployment events must not leak")
		assert.False(t, strings.HasSuffix(key, appAID), "no event of team A's app may appear")
		assert.NotEqual(t, "hook.created/"+appAID, key, "team A's hook.created must not leak")
		assert.NotEqual(t, "hook.rotated/"+appAID, key, "team A's hook.rotated must not leak")
		assert.NotEqual(t, "alert_rule.created/"+ruleA.GetRule().GetId(), key, "team A's alert_rule.created must not leak")
		assert.NotContains(t, key, "github_app.connected", "team A's project-scoped github_app material events must not leak")
	}

	// ---- A 队成员照常可见本队事件（防过紧） ----
	aView := collectEvents(t, aMember, events)
	assert.Contains(t, aView, "project.created/"+projAID, "team A members keep seeing their own events")
	assert.Contains(t, aView, "app.created/"+appAID, "team A members keep seeing their own app events")
	assert.NotContains(t, aView, "app.created/"+appB.GetApp().GetId(), "team B's events must not leak into team A's view")
	assert.Contains(t, aView, "hook.created/"+appAID, "team A members see their own hook.created")
	assert.Contains(t, aView, "alert_rule.created/"+ruleA.GetRule().GetId(), "team A members see their own alert_rule.created")
	assert.Contains(t, aView, "github_app.connected/"+ghApp.GetApp().GetId(), "team A members see their project-scoped github_app.connected")
	assert.Contains(t, aView, "channel.created/"+channel.GetChannel().GetId(), "the platform-level channel event is visible to team A too")
	assert.NotContains(t, aView, "hook.created/"+appB.GetApp().GetId(), "team B's hook events must not leak into team A's view")
	assert.NotContains(t, aView, "alert_rule.created/"+ruleB.GetRule().GetId(), "team B's alert rule events must not leak into team A's view")
	foundDep := false
	for key := range aView {
		if strings.HasPrefix(key, "deployment.") && strings.HasSuffix(key, depAID) {
			foundDep = true
		}
	}
	assert.True(t, foundDep, "team A members see their deployment events (engine transition rows carry the resource-axis team)")

	// ---- 平台 owner：全量可见（双队 + 平台级都在场） ----
	ownerView := collectEvents(t, owner, events)
	assert.Contains(t, ownerView, "project.created/"+projAID)
	assert.Contains(t, ownerView, "app.created/"+appB.GetApp().GetId())
	assert.Contains(t, ownerView, "node.joined/01JD0NODE0000000000000000X")

	// ---- StreamEvents（gRPC 订阅径）与 ListEvents 同锚 ----
	replay, err := events.StreamEvents(bMember, &telemetryv1.StreamEventsRequest{Follow: false})
	require.NoError(t, err)
	streamLeak := false
	for {
		frame, rerr := replay.Recv()
		if rerr == io.EOF {
			break
		}
		require.NoError(t, rerr)
		id := frame.GetEvent().GetAggregateId()
		if id == projAID || id == appAID || id == depAID || id == taskA.GetTask().GetId() {
			streamLeak = true
		}
	}
	assert.False(t, streamLeak, "the gRPC stream must enforce the same team filter as ListEvents")

	// ---- SSE 票据径：payload 绑铸造者可见域，兑换后据此过滤 ----
	tk, err := events.IssueEventTicket(bMember, &telemetryv1.IssueEventTicketRequest{})
	require.NoError(t, err)
	sc, _ := sseSession(t, h, tk.GetTicket(), "&follow=0")
	sseLeak, sseOwn := false, false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if strings.Contains(line, appAID) || strings.Contains(line, projAID) || strings.Contains(line, depAID) {
			sseLeak = true
		}
		if strings.Contains(line, appB.GetApp().GetId()) {
			sseOwn = true
		}
	}
	assert.False(t, sseLeak, "the SSE ticket path must not deliver other teams' events")
	assert.True(t, sseOwn, "the SSE ticket path still delivers the minting team's own events")

	// owner 票据（"*" 可见域）：全量重放（A 事件在场，钉 owner 豁免口径）。
	ownerTk, err := events.IssueEventTicket(owner, &telemetryv1.IssueEventTicketRequest{})
	require.NoError(t, err)
	sc, _ = sseSession(t, h, ownerTk.GetTicket(), "&follow=0")
	sawA := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), appAID) {
			sawA = true
		}
	}
	assert.True(t, sawA, "an owner-minted ticket replays every team's events")
}
