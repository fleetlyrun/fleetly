package apitest_test

// API 面全链冒烟：真 engine + 真 API + bufconn（Deploy 归一化 → 冻结 →
// admission → 状态机驱动一条龙）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestDeployEndToEnd(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token) // owner 全权（bootstrap）
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	// 镜像直投部署。
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), Image: "nginx:1.27",
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", dep.GetDeployment().GetState())

	// 引擎推进到 releasing（假 runtime Ensure 生效）。releasing 态先于
	// Ensure 记录可见（状态迁移与载体下发在同一 step 的两段），Ensure 记
	// 录单独轮询（-race 下调度慢会暴露窗口）。
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateReleasing)
	}, 2e9, 1e7)
	require.Eventually(t, func() bool {
		return len(h.Runtime.Calls()) > 0
	}, 2e9, 1e7)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "nginx:1.27", calls[0].Spec["web"].Image)

	// L1 观测 → succeeded。
	h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateObserving)
	}, 2e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == string(deployment.StateSucceeded)
	}, 2e9, 1e7)
}

// Q-13 回归：Rollback 无成功基线的错误映射走 engine 哨兵
// （errors.Is，非文案 Contains）→ E_NO_BASELINE 稳定可编程分支。
func TestRollbackNoBaselineMapsStable(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "rollback"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	_, err = deployments.Rollback(ctx, &deliveryv1.RollbackRequest{AppId: app.GetApp().GetId()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NO_BASELINE")
	require.Contains(t, err.Error(), "no successful deployment to roll back to")
}

func TestComposeDeployNormalizes(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token) // owner 全权（bootstrap）
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	revisions := deliveryv1.NewRevisionsServiceClient(h.Conn)

	proj, _ := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	app, _ := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "compose"})

	compose := "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080\"\n    deploy:\n      replicas: 2\n"
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: app.GetApp().GetId(), ComposeYaml: compose})
	require.NoError(t, err)

	revs, err := revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{AppId: app.GetApp().GetId()})
	require.NoError(t, err)
	require.Len(t, revs.GetRevisions(), 1)

	// 同内容重复部署：Revision 内容寻址复用。
	dep2, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: app.GetApp().GetId(), ComposeYaml: compose})
	require.NoError(t, err)
	revs2, _ := revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{AppId: app.GetApp().GetId()})
	require.Len(t, revs2.GetRevisions(), 1, "identical spec reuses the frozen revision")
	assert.NotEqual(t, dep.GetDeployment().GetId(), dep2.GetDeployment().GetId(), "but deployments are distinct")
}

func TestComposeRejectedField(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token) // owner 全权（bootstrap）
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, _ := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "p"})
	app, _ := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "a"})
	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId:       app.GetApp().GetId(),
		ComposeYaml: "services:\n  web:\n    image: nginx\n    privileged: true\n",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported field")
}

func TestNodesAndEnroll(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token) // owner 全权（bootstrap）
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	kit, err := nodes.EnrollNode(ctx, &runtimev1.EnrollNodeRequest{})
	require.NoError(t, err)
	assert.Contains(t, kit.GetJoinCommand(), "docker swarm join")

	// 节点对账在 managed 节拍落缓存（启动后一个 tick 内）。
	require.Eventually(t, func() bool {
		list, err := nodes.ListNodes(ctx, &runtimev1.ListNodesRequest{})
		return err == nil && len(list.GetNodes()) > 0
	}, 3e9, 2e7)
}

// TestNodesAdminVerb 覆盖 RuntimeAdmin 三动词的服务面（F0.19）：锚点校验、
// 哨兵错误映射、子面调用记录与审计行（结果即动作本身）。
func TestNodesAdminVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token) // owner 全权（bootstrap）
	nodes := runtimev1.NewNodesServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	// 空锚点在服务面即拒（E_INVALID_ARGUMENT）。
	_, err := nodes.DrainNode(ctx, &runtimev1.DrainNodeRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node_id")

	// Provider 哨兵映射 E_NOT_FOUND（对不上任何载体节点）。
	h.Runtime.SetAdminErr(capability.ErrNodeNotFound)
	missing := "01JMISSING0000000000000000"
	_, err = nodes.CordonNode(ctx, &runtimev1.CordonNodeRequest{NodeId: missing})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// 三动词 happy path：子面记录 + 审计行（bufconn 直连无 CLI 自标识头，
	// source=api；CLI 走 x-fleetly-client=cli 呈现 cli）。
	h.Runtime.SetAdminErr(nil)
	nodeID := "01JD0NODE00000000000000000"
	for _, r := range []struct {
		name string
		call func() error
	}{
		{"drain", func() error {
			_, err := nodes.DrainNode(ctx, &runtimev1.DrainNodeRequest{NodeId: nodeID})
			return err
		}},
		{"cordon", func() error {
			_, err := nodes.CordonNode(ctx, &runtimev1.CordonNodeRequest{NodeId: nodeID})
			return err
		}},
		{"uncordon", func() error {
			_, err := nodes.UncordonNode(ctx, &runtimev1.UncordonNodeRequest{NodeId: nodeID})
			return err
		}},
	} {
		require.NoError(t, r.call(), "%s must succeed", r.name)
	}

	calls := h.Runtime.AdminCalls()
	require.Len(t, calls, 4, "failed attempt is recorded too")
	assert.Equal(t, "cordon", calls[0].Verb)
	assert.Equal(t, missing, calls[0].NodeID)
	assert.Equal(t, []string{"drain", "cordon", "uncordon"},
		[]string{calls[1].Verb, calls[2].Verb, calls[3].Verb})
	assert.Equal(t, nodeID, calls[1].NodeID)

	entries, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: "node.", Limit: 100})
	require.NoError(t, err)
	var actions []string
	for _, e := range entries.GetEntries() {
		assert.Equal(t, "api", e.GetSource(), "bufconn dial carries no cli self-identification header")
		actions = append(actions, e.GetAction())
	}
	// 审计列表新→旧（ORDER BY id DESC），同拍 ULID 次序不作断言。
	assert.ElementsMatch(t, []string{"node.drain", "node.cordon", "node.uncordon"}, actions,
		"exactly the three successful verbs leave audit rows")
}
