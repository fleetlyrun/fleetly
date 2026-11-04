package apitest_test

// pre-deploy job 序语义全链（ADR-0098）：staging 记录·六十一坑①重播形——
// 双 label 方言 job 字典序自然排 = db-bootstrap 先铸，处女库上等待环等
// migrate 落库的角色、串行下永不到来 = 死锁。fleetly.job.after 把受理序
// 拓扑反转为 migrate 先铸先完成，db-bootstrap 次铸，一次过到 succeeded
// （游标序 = 受理序 = repeated 位次）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestComposePreDeployJobOrderAfterResolvesDeadlock(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	runs := automationv1.NewRunsServiceClient(h.Conn)

	projectID := h.CreateProject(t, "ordshop").GetId()
	appID := h.CreateApp(t, projectID, "web").GetId()

	// 死锁形：db-bootstrap 字典序自然先于 migrate；after 声明反转受理序。
	compose := "services:\n" +
		"  web:\n" +
		"    image: nginx:1.27\n" +
		"  db-bootstrap:\n" +
		"    image: busybox:1.37\n" +
		"    command: [\"db-bootstrap\"]\n" +
		"    labels:\n" +
		"      fleetly.job: \"init\"\n" +
		"      fleetly.job.timeout: \"5m\"\n" +
		"      fleetly.job.after: \"migrate\"\n" +
		"  migrate:\n" +
		"    image: busybox:1.37\n" +
		"    command: [\"migrate\", \"up\"]\n" +
		"    labels:\n" +
		"      fleetly.job: \"init\"\n" +
		"      fleetly.job.timeout: \"5m\"\n"
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, ComposeYaml: compose})
	require.NoError(t, err)
	depID := dep.GetDeployment().GetId()

	// 逐锚推进（串行链：job i 成功才铸 i+1）：首锚必是 migrate（after 反转
	// 字典序），次锚 db-bootstrap。锚的身份经 ensure 记录的 run 命令断言。
	var order [][]string
	seen := ""
	for i := 0; i < 2; i++ {
		taskID := ""
		require.Eventually(t, func() bool {
			got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
			if err != nil {
				return false
			}
			taskID = got.GetDeployment().GetPreDeployTaskId()
			return taskID != "" && taskID != seen
		}, 3e9, 1e7, "next pre-deploy anchor must appear (serial pipeline)")
		seen = taskID

		var cmd []string
		require.Eventually(t, func() bool {
			for j := range h.Runtime.Calls() {
				c := h.Runtime.Calls()[j]
				if c.NS.Task == taskID {
					w, ok := c.Spec["run"]
					if ok {
						cmd = w.Command
					}
					return ok
				}
			}
			return false
		}, 3e9, 1e7, "job task-domain ensure must be recorded")
		order = append(order, cmd)

		// run 行落地 → 完成报文（job 自身无等待：受理序已把依赖编进位次）。
		var list *automationv1.ListRunsResponse
		require.Eventually(t, func() bool {
			got, err := runs.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: taskID})
			if err != nil {
				return false
			}
			list = got
			return len(got.GetRuns()) == 1
		}, 3e9, 1e7, "job run row must land after the anchor becomes visible")
		h.Runtime.ReportCompleted(list.GetRuns()[0].GetId(), 1)
	}

	assert.Equal(t, [][]string{{"migrate", "up"}, {"db-bootstrap"}}, order,
		"the lexicographic deadlock order flips: migrate mints and completes first, db-bootstrap follows")

	// done 游标 + carrier 一次过（尾部与单 job 链路同款）：L1 → succeeded。
	require.Eventually(t, func() bool {
		for _, c := range h.Runtime.Calls() {
			if c.NS.App == appID {
				return true
			}
		}
		return false
	}, 3e9, 1e7, "carrier ensure must follow the final job terminal state")
	h.Runtime.ReportRunning(appID+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 3e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 3e9, 1e7)

	got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
	require.NoError(t, err)
	assert.Empty(t, got.GetDeployment().GetPreDeployTaskId(), "done cursor clears the anchor after all jobs complete")
}
