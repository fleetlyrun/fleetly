package apitest_test

// ADR-0035 行级授权验收矩阵：team B Token 对 team A 资源的读写全拒
//（E_FORBIDDEN），同队访问不受影响，owner（平台管理员）跨队全通；List 面
// 按队过滤；ListRuns 强制过滤；identity 面（CreateToken/CreateProject）目标
// Team 校验与缺省=调用方 Team；审计 Team 轴过滤；freeze 面（全局仅 owner、
// 本队冻结/解除）。

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// mintTeamToken 直铸一枚挂指定 Team/角色的平台 Token（ADR-0035 跨队夹具；
// 明文只在本函数返回值存活）。
func mintTeamToken(t *testing.T, h *apitest.Harness, name, teamID, roleID string) string {
	t.Helper()
	material, err := identity.NewToken()
	require.NoError(t, err)
	repo := tokenrepo.New(h.DB.Clock())
	require.NoError(t, repo.Create(context.Background(), h.DB.Runner(), &tokenrepo.Token{
		ID: ulid.Make().String(), Name: name,
		TeamID: teamID, RoleID: roleID,
		SHA256: material.SHA256, Prefix: material.Prefix,
	}))
	return material.Secret
}

// waitRunUnderTask 轮询等 engine 拍出 Task 名下首条 Run（自动驱动形态的
// 异步收敛；5s 上限防悬挂）。
func waitRunUnderTask(t *testing.T, ctx context.Context, c automationv1.RunsServiceClient, taskID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID, Limit: 1})
		require.NoError(t, err)
		if len(resp.GetRuns()) > 0 {
			return resp.GetRuns()[0].GetId()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no run appeared under task %s within 5s", taskID)
	return ""
}

func TestRowLevelTeamAuthorization(t *testing.T) {
	h := apitest.New(t)
	anon := context.Background()
	owner := sdk.WithToken(anon, h.Token)

	// 夹具：team "acme"(B) + B 的 admin token；default(team A) 的资源由
	// owner 铺（owner 跨队豁免通道本身即验收锚之一）。
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	teamB, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "acme"})
	require.NoError(t, err)
	bID := teamB.GetTeam().GetId()
	bAdmin := sdk.WithToken(anon, mintTeamToken(t, h, "acme-admin", bID, identity.RoleAdminID))
	aMember := sdk.WithToken(anon, mintTeamToken(t, h, "shop-dev", identity.DefaultTeamID, identity.RoleMemberID))

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	configs := structurev1.NewConfigsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	routes := proxyv1.NewRoutesServiceClient(h.Conn)
	tasks := automationv1.NewTasksServiceClient(h.Conn)
	runs := automationv1.NewRunsServiceClient(h.Conn)
	schedules := automationv1.NewSchedulesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)
	auditQ := identityv1.NewAuditQueryServiceClient(h.Conn)
	freezes := systemv1.NewGovernanceServiceClient(h.Conn)

	projA, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	projAID := projA.GetProject().GetId()
	appA, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projAID, Name: "web"})
	require.NoError(t, err)
	appAID := appA.GetApp().GetId()
	_, err = secrets.PutSecret(owner, &structurev1.PutSecretRequest{ProjectId: projAID, Name: "db-password", Value: "s3cr3t"})
	require.NoError(t, err)
	_, err = configs.PutConfig(owner, &structurev1.PutConfigRequest{ProjectId: projAID, Name: "env", Content: `{"LOG_LEVEL":"info"}`})
	require.NoError(t, err)
	// default 网络随项目出生（F-C）——跨队读面按名直取的样本行由出生面
	// 提供，无需显式建。
	taskA, err := tasks.CreateTask(owner, &automationv1.CreateTaskRequest{ProjectId: projAID, Name: "migrate", Image: "nginx:1.27"})
	require.NoError(t, err)
	taskAID := taskA.GetTask().GetId()
	depA, err := deployments.Deploy(owner, &deliveryv1.DeployRequest{AppId: appAID, Image: "nginx:1.27"})
	require.NoError(t, err)
	depAID := depA.GetDeployment().GetId()

	// ---- 跨队读面：全局 ID 直取全拒（存在但非本队 → E_FORBIDDEN） ----
	for name, call := range map[string]func() error{
		"GetApp": func() error { _, e := apps.GetApp(bAdmin, &structurev1.GetAppRequest{Id: appAID}); return e },
		"GetConfig": func() error {
			_, e := configs.GetConfig(bAdmin, &structurev1.GetConfigRequest{ProjectId: projAID, Name: "env"})
			return e
		},
		"ListSecrets": func() error {
			_, e := secrets.ListSecrets(bAdmin, &structurev1.ListSecretsRequest{ProjectId: projAID})
			return e
		},
		"ListNetworks": func() error {
			_, e := networks.ListNetworks(bAdmin, &structurev1.ListNetworksRequest{ProjectId: projAID})
			return e
		},
		"GetTask": func() error { _, e := tasks.GetTask(bAdmin, &automationv1.GetTaskRequest{Id: taskAID}); return e },
		"ListTasks": func() error {
			_, e := tasks.ListTasks(bAdmin, &automationv1.ListTasksRequest{ProjectId: projAID})
			return e
		},
		"GetDeployment": func() error {
			_, e := deployments.GetDeployment(bAdmin, &deliveryv1.GetDeploymentRequest{Id: depAID})
			return e
		},
		"ListDeployments": func() error {
			_, e := deployments.ListDeployments(bAdmin, &deliveryv1.ListDeploymentsRequest{AppId: appAID})
			return e
		},
		"ListRevisions": func() error {
			_, e := revisions.ListRevisions(bAdmin, &deliveryv1.ListRevisionsRequest{AppId: appAID})
			return e
		},
		"DiffRevisions": func() error {
			_, e := revisions.DiffRevisions(bAdmin, &deliveryv1.DiffRevisionsRequest{AppId: appAID, FromSeq: 1, ToSeq: 1})
			return e
		}, //nolint:staticcheck // 序号常量在越权拒绝前不被消费
		"ListBuilds": func() error {
			_, e := builds.ListBuilds(bAdmin, &deliveryv1.ListBuildsRequest{AppId: appAID})
			return e
		},
		"GetProject": func() error {
			_, e := projects.GetProject(bAdmin, &structurev1.GetProjectRequest{Id: projAID})
			return e
		},
		"ListSchedules": func() error {
			_, e := schedules.ListSchedules(bAdmin, &automationv1.ListSchedulesRequest{ProjectId: projAID})
			return e
		},
	} {
		assert.Equal(t, codes.PermissionDenied, status.Code(call()), "cross-team %s must be E_FORBIDDEN", name)
	}

	// ---- 跨队写面：受理前置拒绝 ----
	_, err = secrets.PutSecret(bAdmin, &structurev1.PutSecretRequest{ProjectId: projAID, Name: "evil", Value: "x"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team PutSecret")
	_, err = secrets.DeleteSecret(bAdmin, &structurev1.DeleteSecretRequest{ProjectId: projAID, Name: "db-password"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team DeleteSecret")
	_, err = apps.CreateApp(bAdmin, &structurev1.CreateAppRequest{ProjectId: projAID, Name: "implant"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team CreateApp")
	_, err = tasks.CreateTask(bAdmin, &automationv1.CreateTaskRequest{ProjectId: projAID, Image: "nginx:1.27"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team CreateTask")
	_, err = deployments.Deploy(bAdmin, &deliveryv1.DeployRequest{AppId: appAID, Image: "nginx:1.27"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team Deploy")
	_, err = deployments.Rollback(bAdmin, &deliveryv1.RollbackRequest{AppId: appAID, ToRevision: "1"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team Rollback")
	_, err = routes.CreateRoute(bAdmin, &proxyv1.CreateRouteRequest{
		ProjectId: projAID, AppId: appAID, Process: "web", Port: 8080, Host: "hijack.example.com",
	})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team CreateRoute")

	// 上传面：首帧 meta 指向他人 Project → 落盘前拒绝。
	_, err = uploadTar(bAdmin, builds, projAID, buildFixtureTar(), 4096)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team UploadSource")

	// ---- Task 动词与 Run 面 ----
	_, err = tasks.ScaleTask(bAdmin, &automationv1.ScaleTaskRequest{Id: taskAID, DesiredConcurrency: 2})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team ScaleTask")
	_, err = tasks.DeleteTask(bAdmin, &automationv1.DeleteTaskRequest{Id: taskAID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team DeleteTask")
	runID := waitRunUnderTask(t, owner, runs, taskAID)
	_, err = runs.GetRun(bAdmin, &automationv1.GetRunRequest{Id: runID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team GetRun")
	_, err = runs.ListRuns(bAdmin, &automationv1.ListRunsRequest{TaskId: taskAID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team ListRuns(task)")

	// ListRuns 收紧：无过滤 / 双过滤。
	_, err = runs.ListRuns(bAdmin, &automationv1.ListRunsRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "unfiltered ListRuns must be rejected")
	_, err = runs.ListRuns(owner, &automationv1.ListRunsRequest{TaskId: taskAID, ProjectId: projAID})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "task+project ListRuns must be rejected")

	// ---- List 面按队过滤（B 侧只见 B 的项目） ----
	projB, err := projects.CreateProject(bAdmin, &structurev1.CreateProjectRequest{Name: "acme-core"})
	require.NoError(t, err)
	assert.Equal(t, bID, projB.GetProject().GetTeamId(), "omitted team_id defaults to the caller's team")
	seen, err := projects.ListProjects(bAdmin, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)
	assert.Len(t, seen.GetProjects(), 1, "team B sees only its own project")
	assert.Equal(t, projB.GetProject().GetId(), seen.GetProjects()[0].GetId())
	ownerList, err := projects.ListProjects(owner, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)
	assert.Len(t, ownerList.GetProjects(), 2, "platform owner sees every team's projects")

	// owner 跨队豁免通道：owner 读 B 的项目成立。
	_, err = projects.GetProject(owner, &structurev1.GetProjectRequest{Id: projB.GetProject().GetId()})
	assert.NoError(t, err, "platform owner bypasses the row-level check")

	// 同队正向：team A member 读 A 的资源不拦（行级之外无干扰）。
	got, err := apps.GetApp(aMember, &structurev1.GetAppRequest{Id: appAID})
	require.NoError(t, err, "same-team access must pass")
	assert.Equal(t, appAID, got.GetApp().GetId())

	// ---- identity 面：目标 Team 校验与缺省 ----
	_, err = tokens.CreateToken(bAdmin, &identityv1.CreateTokenRequest{Name: "implant", RoleId: identity.RoleMemberID, TeamId: identity.DefaultTeamID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team CreateToken")
	inTeam, err := tokens.CreateToken(bAdmin, &identityv1.CreateTokenRequest{Name: "acme-ci", RoleId: identity.RoleMemberID})
	require.NoError(t, err)
	assert.Equal(t, bID, inTeam.GetToken().GetTeamId(), "CreateToken defaults to the caller's team")
	_, err = projects.CreateProject(bAdmin, &structurev1.CreateProjectRequest{Name: "shop-clone", TeamId: identity.DefaultTeamID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team CreateProject")

	// ---- 审计 Team 轴 ----
	bAudit, err := auditQ.ListAudit(bAdmin, &identityv1.ListAuditRequest{})
	require.NoError(t, err)
	for _, e := range bAudit.GetEntries() {
		assert.NotEqual(t, "project/"+projAID, e.GetResource(),
			"team A's audit rows must not leak to team B (team_id axis)")
	}
	foundB := false
	for _, e := range bAudit.GetEntries() {
		if e.GetResource() == "project/"+projB.GetProject().GetId() {
			foundB = true
		}
	}
	assert.True(t, foundB, "team B's own rows are visible (plus ''-team system rows)")
	ownerAudit, err := auditQ.ListAudit(owner, &identityv1.ListAuditRequest{})
	require.NoError(t, err)
	sawA := false
	for _, e := range ownerAudit.GetEntries() {
		if e.GetResource() == "project/"+projAID {
			sawA = true
		}
	}
	assert.True(t, sawA, "platform owner sees every team's audit rows")

	// ---- freeze 面：全局仅 owner；本队冻结/解除；他队冻结不可碰 ----
	_, err = freezes.SetChangeFreeze(bAdmin, &systemv1.SetChangeFreezeRequest{Reason: "b maintenance"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "global freeze (empty team) is owner-only")
	_, err = freezes.SetChangeFreeze(bAdmin, &systemv1.SetChangeFreezeRequest{Reason: "a hijack", TeamId: identity.DefaultTeamID})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "cross-team freeze is forbidden")
	bFreeze, err := freezes.SetChangeFreeze(bAdmin, &systemv1.SetChangeFreezeRequest{Reason: "b maintenance", TeamId: bID})
	require.NoError(t, err)
	_, err = freezes.LiftChangeFreeze(aMember, &systemv1.LiftChangeFreezeRequest{Id: bFreeze.GetFreeze().GetId()})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "team A cannot lift team B's freeze")
	_, err = freezes.LiftChangeFreeze(bAdmin, &systemv1.LiftChangeFreezeRequest{Id: bFreeze.GetFreeze().GetId()})
	require.NoError(t, err, "own-team freeze lifts")
}
