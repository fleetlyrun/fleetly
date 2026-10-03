package apitest_test

// firstBootJobs intake 全链（ADR-0033）：compose 扩展键 → 归一化 → 冻结 →
// releasing 前半铸 job Task（Deployment 消息 first_boot_task_id 可见，
// ADR-0030 开放锚）→ Run 终态 (stopped, completed) → carrier → succeeded。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestComposeFirstBootDeployEndToEnd(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	runs := automationv1.NewRunsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "fbshop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	// job 的 secret_refs 预置（材料面硬失败语义——缺行即 Run 不可铸）。
	const fixtureURL = "postgres://u:p@db-1:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{
		ProjectId: proj.GetProject().GetId(), Name: "db-url", Value: fixtureURL,
	})
	require.NoError(t, err)

	// compose 扩展键（ADR-0033）：command list 形态 + secrets + ttl。
	compose := "services:\n" +
		"  web:\n" +
		"    image: nginx:1.27\n" +
		"x-fleetly-first-boot-jobs:\n" +
		"  - name: migrate\n" +
		"    image: migrate/migrate:v4.18.1\n" +
		"    command: [\"sh\", \"-c\", \"migrate -database \\\"$(cat /run/secrets/db-url)\\\" up\"]\n" +
		"    secrets:\n" +
		"      - db-url\n" +
		"    ttl: 300s\n"
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: app.GetApp().GetId(), ComposeYaml: compose})
	require.NoError(t, err)
	depID := dep.GetDeployment().GetId()

	// 铸造锚可见（ADR-0030 决策 3：API 暴露当前锚定 Task）。
	var taskID string
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		if err != nil {
			return false
		}
		taskID = got.GetDeployment().GetFirstBootTaskId()
		return taskID != ""
	}, 3e9, 1e7)

	// 铸出的 job Task 是无名 one-shot（schedule 先例），Run 材料面带
	// secret 文件注入（与 App 域同一条解析通道）。
	var list *automationv1.ListRunsResponse
	require.Eventually(t, func() bool {
		got, err := runs.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
		if err != nil {
			return false
		}
		list = got
		return len(got.GetRuns()) == 1
	}, 3e9, 1e7, "job run row must land after the task anchor becomes visible")
	runID := list.GetRuns()[0].GetId()
	// Run 行先于 workload Ensure 落账（engine 拍内两步）——扫描包 Eventually
	// 等待 job 域 ensure 出现（裸扫描在行落账与 Ensure 之间的窗口上偶发空手，
	// -race 下确定性复现；下方 carrier ensure 同款先例）。
	var jobEnsure *apitest.EnsureCall
	require.Eventually(t, func() bool {
		for i := range h.Runtime.Calls() {
			c := h.Runtime.Calls()[i]
			if c.NS.Task == taskID {
				jobEnsure = &h.Runtime.Calls()[i]
				return true
			}
		}
		return false
	}, 3e9, 1e7, "job task-domain ensure must be recorded")
	w := jobEnsure.Spec["run"]
	require.NotNil(t, w)
	assert.Equal(t, runID, w.ID)
	assert.Equal(t, []string{"sh", "-c", "migrate -database \"$(cat /run/secrets/db-url)\" up"}, w.Command, "list-form command lands as literal argv")
	assert.Equal(t, capability.RestartNever, w.Restart)
	assert.Contains(t, jobEnsure.Materials.SecretFiles, "db-url", "secret_refs inject as files on the job run")

	// job Run 终态 (stopped, completed) → carrier 子相位 Ensure → L1 → succeeded。
	h.Runtime.ReportCompleted(runID, 1)
	require.Eventually(t, func() bool {
		for _, c := range h.Runtime.Calls() {
			if c.NS.App == app.GetApp().GetId() {
				return true
			}
		}
		return false
	}, 3e9, 1e7, "carrier ensure must follow the job terminal state")
	h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 3e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 3e9, 1e7)

	// done 游标：全部完成后 first_boot_task_id 归空（无 job/已完成为空，
	// ADR-0030 决策 3）。
	got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
	require.NoError(t, err)
	assert.Empty(t, got.GetDeployment().GetFirstBootTaskId())
}
