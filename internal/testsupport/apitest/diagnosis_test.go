package apitest_test

// Diagnosis 面 apitest（B2a W2，zeabur 借鉴设计 §2.5）：DiagnoseApp 全链
//（造 crash-loop 容器 → 诊断条目 + unexplained 兜底腿 + unreadable 腿 +
// Fix 深链占位符填充 + REST 路径段）；ListProjectFindings 聚合与分页/预算
//（先行筛未命中的健康 App 不占响应、firing-only 告警 App 先行筛命中但深
// 诊断为空不占响应、after_app_seq 游标翻页）；crash-loop 通知通道投递
//（进入/退出各一次——沿纪律端到端，cipher 面在 apitest 夹具内）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// crashExit 注入一次容器终态观测（新实例逐次——终态事实逐次入账）。
func crashExit(h *apitest.Harness, workloadID string, gen uint64, instance string, exitCode int) {
	h.Runtime.ReportEvent(capability.WorkloadEvent{
		WorkloadID: workloadID, Generation: capability.Generation(gen),
		State: capability.WorkloadFailed, ExitCode: &exitCode,
		Reason: "task: non-zero exit", Instance: instance,
	})
}

// diagnoseEventually 等到诊断面出现期望条目（观测消费是异步的——Watch 流
// 经 consumeWatch；单次终态即产出 unexplained，等特指条目防竞态抢跑）。
// 返回当时的响应。
func diagnoseEventually(t *testing.T, h *apitest.Harness, ctx context.Context, client runtimev1.DiagnosisServiceClient, appID, wantFinding string) *runtimev1.DiagnoseAppResponse {
	t.Helper()
	var last *runtimev1.DiagnoseAppResponse
	require.Eventually(t, func() bool {
		got, err := client.DiagnoseApp(ctx, &runtimev1.DiagnoseAppRequest{AppId: appID})
		if err != nil {
			return false
		}
		last = got
		for _, f := range got.GetFindings() {
			if f.GetFindingId() == wantFinding {
				return true
			}
		}
		return false
	}, 5e9, 2e7)
	return last
}

// TestDiagnoseAppCrashLoopAndUnexplained：全链——crash-loop 容器 → 条目
// （evidence/fix/深链）+ unreadable 腿（组件健康面 B2b 前恒在列）；
// 单发失败（无信号）→ unexplained 兜底（recent_event 证据回指）。
func TestDiagnoseAppCrashLoopAndUnexplained(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	diag := runtimev1.NewDiagnosisServiceClient(h.Conn)

	project := h.CreateProject(t, "diag")
	crashApp := h.CreateApp(t, project.GetId(), "crash").GetId()
	mysteryApp := h.CreateApp(t, project.GetId(), "mystery").GetId()

	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1}]}`
	crashDep := deployAndWaitSucceeded(t, h, ctx, deployments, crashApp, body)
	mysteryDep := deployAndWaitSucceeded(t, h, ctx, deployments, mysteryApp, body)

	// crash-loop：三次终态（逐实例）→ 崩溃循环条目。
	gen := crashDep.GetGeneration()
	for _, inst := range []string{"task-a", "task-b", "task-c"} {
		crashExit(h, crashApp+"-web", gen, inst, 1)
	}
	got := diagnoseEventually(t, h, ctx, diag, crashApp, "F_WORKLOAD_CRASH_LOOP")
	require.Len(t, got.GetFindings(), 1, "a covered failure yields exactly the crash-loop finding (no unexplained double-count)")
	f := got.GetFindings()[0]
	assert.Equal(t, "F_WORKLOAD_CRASH_LOOP", f.GetFindingId())
	assert.Equal(t, "warning", f.GetSeverity())
	assert.Equal(t, "app", f.GetFaultClass())
	assert.Equal(t, crashApp+"-web", f.GetWorkloadId())
	assert.Equal(t, "web", f.GetProcess())
	require.NotNil(t, f.GetEvidence())
	assert.Equal(t, "failed", f.GetEvidence().GetState())
	assert.EqualValues(t, 3, f.GetEvidence().GetRestartCount(), "restart count derives from the event ledger")
	require.NotNil(t, f.GetEvidence().GetExitCode())
	assert.EqualValues(t, 1, f.GetEvidence().GetExitCode())
	require.NotNil(t, f.GetFix())
	assert.Equal(t, "restart", f.GetFix().GetVerb())
	assert.Equal(t, "web", f.GetFix().GetParams()["process"])
	// restart/scale 动词深链直达进程面（修复批 F4：注册表 /processes 子页）。
	assert.Equal(t, "/p/"+project.GetId()+"/apps/"+crashApp+"/processes", f.GetFix().GetConsolePath(),
		"console deep-link placeholders are filled by the assembly layer")
	// unreadable 腿：组件健康面 B2b 前恒 None → 诚实展示。
	assert.Equal(t, []string{"components"}, got.GetUnreadableFaces())

	// unexplained 兜底腿：单发失败、无规则信号 → 最小陈述 + 最近事件证据。
	h.Runtime.ReportEvent(capability.WorkloadEvent{
		WorkloadID: mysteryApp + "-web", Generation: capability.Generation(mysteryDep.GetGeneration()),
		State: capability.WorkloadFailed, Instance: "task-x",
	})
	got = diagnoseEventually(t, h, ctx, diag, mysteryApp, "F_DIAGNOSIS_UNEXPLAINED")
	require.Len(t, got.GetFindings(), 1)
	f = got.GetFindings()[0]
	assert.Equal(t, "F_DIAGNOSIS_UNEXPLAINED", f.GetFindingId(), "uncovered failures always carry the fallback entry")
	assert.Equal(t, "failed", f.GetEvidence().GetState())
	assert.EqualValues(t, 1, f.GetEvidence().GetRestartCount())
	assert.Equal(t, "workload.container_exited", f.GetEvidence().GetRecentEvent(), "recent event rides the evidence face")

	// 未知 App 精确 E_NOT_FOUND（行级授权沿 project 链的 404 腿）。
	_, err := diag.DiagnoseApp(ctx, &runtimev1.DiagnoseAppRequest{AppId: "01JD0APP0000000000000MISS"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")

	// REST 路径段形态（gateway 注解面）：/v1/apps/{app_id}/diagnosis。
	rec := h.REST().Get("/v1/apps/" + crashApp + "/diagnosis")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rest runtimev1.DiagnoseAppResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rest))
	assert.Len(t, rest.GetFindings(), 1)
}

// TestListProjectFindingsPaginationBudget：聚合与分页/扇出预算——先行筛
// 命中（非健康观测）才有条目；健康 App 与 firing-only 告警 App（先行筛
// 命中但深诊断为空）都不占响应；after_app_seq 游标翻页沿 ListApps 惯例。
func TestListProjectFindingsPaginationBudget(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	diag := runtimev1.NewDiagnosisServiceClient(h.Conn)

	project := h.CreateProject(t, "needyou")
	crashApp := h.CreateApp(t, project.GetId(), "acrash").GetId()
	healthyApp := h.CreateApp(t, project.GetId(), "bhealthy").GetId()
	firingApp := h.CreateApp(t, project.GetId(), "cfiring").GetId()

	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1}]}`
	crashDep := deployAndWaitSucceeded(t, h, ctx, deployments, crashApp, body)
	deployAndWaitSucceeded(t, h, ctx, deployments, healthyApp, body)
	deployAndWaitSucceeded(t, h, ctx, deployments, firingApp, body)

	// crashApp：崩溃循环；healthyApp：稳态 running；firingApp：稳态 running
	// + 一条 firing 告警行（先行筛告警腿命中——但 v1 规则不消费告警态，深
	// 诊断为空：先行筛假阳性不占响应）。
	gen := crashDep.GetGeneration()
	for _, inst := range []string{"task-a", "task-b", "task-c"} {
		crashExit(h, crashApp+"-web", gen, inst, 1)
	}
	rules := telemetryv1.NewAlertingServiceClient(h.Conn)
	_, err := rules.CreateAlertRule(ctx, &telemetryv1.CreateAlertRuleRequest{
		AppId: firingApp, Metric: "cpu_percent", Threshold: 90, ForSeconds: 60,
	})
	require.NoError(t, err)
	all, err := h.Services.Engine.AlertRuleRepo().ListAll(ctx)
	require.NoError(t, err)
	for _, r := range all {
		if r.AppID == firingApp {
			require.NoError(t, h.Services.Engine.AlertRuleRepo().Transition(ctx, r.ID, "firing", state.FormatTime(h.Clock.Now())))
		}
	}

	var page *runtimev1.ListProjectFindingsResponse
	require.Eventually(t, func() bool {
		page, err = diag.ListProjectFindings(ctx, &runtimev1.ListProjectFindingsRequest{ProjectId: project.GetId()})
		return err == nil && len(page.GetApps()) > 0
	}, 5e9, 2e7)
	require.Len(t, page.GetApps(), 1, "only the app with findings occupies the page (budget: healthy and firing-only apps are absent)")
	entry := page.GetApps()[0]
	assert.Equal(t, crashApp, entry.GetAppId())
	assert.Equal(t, "acrash", entry.GetAppName())
	assert.EqualValues(t, 1, entry.GetAppSeq(), "app seq echoes the pagination cursor axis")
	require.Len(t, entry.GetFindings(), 1)
	assert.Equal(t, "F_WORKLOAD_CRASH_LOOP", entry.GetFindings()[0].GetFindingId())

	// 分页预算：limit=1 页只覆盖首个 App——翻页经 after_app_seq 游标。
	p1, err := diag.ListProjectFindings(ctx, &runtimev1.ListProjectFindingsRequest{ProjectId: project.GetId(), Limit: 1})
	require.NoError(t, err)
	require.Len(t, p1.GetApps(), 1)
	assert.Equal(t, crashApp, p1.GetApps()[0].GetAppId())
	p2, err := diag.ListProjectFindings(ctx, &runtimev1.ListProjectFindingsRequest{ProjectId: project.GetId(), AfterAppSeq: p1.GetApps()[0].GetAppSeq(), Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, p2.GetApps(), "the healthy app advances the cursor but occupies no page slot")

	// 未知 Project 精确 E_NOT_FOUND。
	_, err = diag.ListProjectFindings(ctx, &runtimev1.ListProjectFindingsRequest{ProjectId: "01JD0PRJ0000000000000MISS"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}

// TestCrashLoopChannelDeliveryRidesEdges：通道投递沿纪律端到端——进入/
// 退出各一次（webhook 收到恰两条 finding 载荷：entered → cleared），其余
// Finding（单发 OOM）零投递。
func TestCrashLoopChannelDeliveryRidesEdges(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	var mu atomic.Int64
	var entered, cleared atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n struct {
			Type      string `json:"type"`
			FindingID string `json:"finding_id"`
			State     string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Add(1)
		switch n.State {
		case "entered":
			entered.Add(1)
		case "cleared":
			cleared.Add(1)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	rules := telemetryv1.NewAlertingServiceClient(h.Conn)
	_, err := rules.CreateNotificationChannel(ctx, &telemetryv1.CreateNotificationChannelRequest{
		Name: "ops", Kind: "webhook", Url: srv.URL,
	})
	require.NoError(t, err)

	project := h.CreateProject(t, "notify").GetId()
	loopApp := h.CreateApp(t, project, "loop").GetId()
	oomApp := h.CreateApp(t, project, "oom").GetId()
	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1}]}`
	loopDep := deployAndWaitSucceeded(t, h, ctx, deployments, loopApp, body)
	oomDep := deployAndWaitSucceeded(t, h, ctx, deployments, oomApp, body)

	// crash-loop：三次终态 → entered 一次。
	gen := loopDep.GetGeneration()
	for _, inst := range []string{"task-a", "task-b", "task-c"} {
		crashExit(h, loopApp+"-web", gen, inst, 1)
	}
	// 更多终态（循环持续）：不重轰。
	crashExit(h, loopApp+"-web", gen, "task-d", 1)
	// 单发 OOM（137）：诊断面有条目、投递面零载荷（其余 Finding 不投递）。
	oom := 137
	h.Runtime.ReportEvent(capability.WorkloadEvent{
		WorkloadID: oomApp + "-web", Generation: capability.Generation(oomDep.GetGeneration()),
		State: capability.WorkloadFailed, ExitCode: &oom, Instance: "task-oom",
	})
	require.Eventually(t, func() bool {
		return entered.Load() == 1
	}, 5e9, 2e7, "exactly one entered delivery rides the transition edge")

	// 恢复（带滞回，修复批 F3）：failed→running 在窗内计数未回落时不清沿
	// （重启间隙的健康观测不是循环结束的证据）。过诊断窗（engine
	// DiagnoseWindow = 10min——终态计数窗内证据老化）后，下一个终态观测
	// 收口 cleared 一次（计数已回落：单次崩溃不再构成循环）；此后稳态
	// running 零重轰。
	h.Runtime.ReportRunning(loopApp+"-web", capability.Generation(gen))
	time.Sleep(300 * time.Millisecond)
	assert.EqualValues(t, 0, cleared.Load(),
		"a recovery observation inside the evidence window must not clear the edge (hysteresis)")
	h.Clock.Advance(11 * time.Minute)
	crashExit(h, loopApp+"-web", gen, "task-e", 1)
	require.Eventually(t, func() bool {
		return cleared.Load() == 1
	}, 5e9, 2e7, "exactly one cleared delivery lands once the in-window count has fallen back")
	h.Runtime.ReportRunning(loopApp+"-web", capability.Generation(gen))
	h.Runtime.ReportRunning(loopApp+"-web", capability.Generation(gen))
	time.Sleep(200 * time.Millisecond)

	assert.EqualValues(t, 1, entered.Load(), "sustained loops and steady polls must not re-deliver")
	assert.EqualValues(t, 2, mu.Load(), "exactly two webhook posts total (entered + cleared); the OOM finding rides no channel")
}
