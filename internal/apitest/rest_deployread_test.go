package apitest_test

// F3.1 API 可见面读面契约（Console 消费面）：Deployment.from_generation
// 与 Revision.process_strategies 经 REST 的 protojson 形态——双代窗叙事
// 的数据源钉死（uint64 = 字符串、枚举 = 规范名、零值省略三口径）。
// 断言走结构化解析：protojson 的空白形态是故意非确定的（防字节依赖），
// 子串匹配会在空格注入轮随机器红。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestRESTDeploymentReadSurface：蓝绿 R1 驱动到 succeeded（代次化载体
// L1）后，第二笔部署的 from_generation 经 list/get 可见；revisions 读面
// 携带 process_strategies（blue-green 规范枚举名 + rolling 零值省略）。
func TestRESTDeploymentReadSurface(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	handler, err := assembly.NewGatewayHandler(slog.New(slog.DiscardHandler), h.Conn, nil, nil, assembly.NewBrowseGate(h.Services))
	require.NoError(t, err)

	get := func(path string) map[string]any {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+h.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "deploy-read"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// R1 = blue-green compose（首代无基线：from_generation 零值省略）。
	first, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, ComposeYaml: "services:\n  web:\n    image: nginx:1.27\n    deploy:\n      strategy: blue-green\n",
	})
	require.NoError(t, err)

	// 蓝绿 L1：代次化载体 app-web-g1 的 running 观测 → observing → 观察
	// 窗过钟 → succeeded（基线 gen 落位——from_generation 的前置）。
	require.Eventually(t, func() bool {
		h.Runtime.ReportRunning(appID+"-web-g1", 1)
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: first.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 3e9, 5e6)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: first.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 3e9, 5e6)

	// 第二笔（rolling 直投）：from_generation = 基线 gen 1。
	second, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.26"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, second.GetDeployment().GetFromGeneration())

	// list：第二行 from_generation = "1"（uint64 字符串口径），首代行
	// 零值省略（键缺席——不可见性同样钉死）。
	body := get("/v1/deployments?app_id=" + appID)
	rows, _ := body["deployments"].([]any)
	require.Len(t, rows, 2)
	firstRow, _ := rows[1].(map[string]any)
	secondRow, _ := rows[0].(map[string]any)
	assert.Equal(t, first.GetDeployment().GetId(), firstRow["id"])
	assert.Equal(t, "1", secondRow["from_generation"], "REST list carries the baseline generation (string form)")
	_, hasFrom := firstRow["from_generation"]
	assert.False(t, hasFrom, "the baseline (first) deployment row omits the zero-value from_generation")
	_, hasFromRev := firstRow["from_revision"]
	assert.False(t, hasFromRev, "the baseline (first) deployment row omits the zero-value from_revision")

	// get：单行同口径（响应面是 deployment 信封）。
	body = get("/v1/deployments/" + second.GetDeployment().GetId())
	got, _ := body["deployment"].(map[string]any)
	require.NotNil(t, got)
	assert.Equal(t, second.GetDeployment().GetId(), got["id"])
	assert.Equal(t, "1", got["from_generation"], "REST get carries the baseline generation")

	// revisions 读面：R1 的 blue-green 策略目录（规范枚举名）+ R2 的
	// rolling 零值省略（strategy 键缺席 = rolling——与 ProcessSpec 缺省
	// 语义同源）。int64 seq 同字符串口径。
	body = get("/v1/revisions?app_id=" + appID)
	revisions, _ := body["revisions"].([]any)
	require.Len(t, revisions, 2)
	r1, _ := revisions[0].(map[string]any)
	r2, _ := revisions[1].(map[string]any)
	assert.Equal(t, "1", r1["seq"])
	ps1, _ := r1["process_strategies"].([]any)
	require.Len(t, ps1, 1)
	entry1, _ := ps1[0].(map[string]any)
	assert.Equal(t, "web", entry1["process"])
	assert.Equal(t, "DEPLOY_STRATEGY_BLUE_GREEN", entry1["strategy"], "the blue-green revision carries the canonical enum name")
	ps2, _ := r2["process_strategies"].([]any)
	require.Len(t, ps2, 1)
	entry2, _ := ps2[0].(map[string]any)
	assert.Equal(t, "web", entry2["process"])
	_, hasStrategy := entry2["strategy"]
	assert.False(t, hasStrategy, "the rolling revision omits the zero-value strategy field")
}
