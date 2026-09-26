package engine

// DT-4 部署期一次性作业（init job，torchwood 线 IMPL-T1-3）引擎面回归：
// 票面五条守卫逐条钉死——
//
//	① job 失败 → release.job_failed + E_INIT_JOB_FAILED，且迁移先于新代码
//	   （长驻服务未按新 spec 对账）；
//	② job 超时 → release.job_timed_out + E_INIT_JOB_TIMED_OUT（per-job
//	   label 预算与平台缺省预算两条锚定）；
//	③ 多 init job 并行、全过才晋级（全过前长驻服务零 create/update）；
//	④ 成功/失败/超时三路径零残留 + 孤儿清扫一个扫描周期清除 + 对账删除
//	   豁免（在途 job 不被 applyDesired 误删）；
//	⑤ 无 init job 的 release 路径零变化（相位列零写、无 release.job_* 事件、
//	   对账仍在计划拍内执行、看门狗预算 = DeployTimeout）。
//
// 补充面：执行形态快照断言（Job/单副本/restart-condition=none/fleetly.init.run
// label/确定性命名）、env 投影与长驻服务同源抽样、重启续跑幂等（同部署
// 两拍不重复建服务 + 重启后相位续跑不误分类）。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// initJobComposeFixture 构造「长驻 web + init job migrate」的 compose；
// extraJobLabels 追加到 migrate 的 labels 段（如
// "      fleetly.job.timeout: 30s\n"）。
func initJobComposeFixture(webImage, extraJobLabels string) string {
	return `name: demo
services:
  web:
    image: ` + webImage + `
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
  migrate:
    image: alpine:3
    command: ["true"]
    labels:
      fleetly.job: init
` + extraJobLabels
}

// initJobServiceName 推导 init job 服务名（与实现同源公式：team/prj 取
// app 行、app 取入队名、尾缀 = deployment ID 前 8 位）。
func (h *harness) initJobServiceName(rec state.DeployRecord, service string) string {
	h.t.Helper()
	app := h.demoApp()
	name, err := naming.InitJobName(app.TeamSlug, app.ProjectSlug, rec.AppName, service, rec.ID)
	if err != nil {
		h.t.Fatalf("init job name: %v", err)
	}
	return name
}

// startInitJobRelease 入队并推进到 init 相位首拍（job 服务已在位）。
// 返回启动后的部署行与 migrate job 服务名。
func startInitJobRelease(t *testing.T, h *harness, yaml string) (state.DeployRecord, string) {
	t.Helper()
	ctx := context.Background()
	rec := h.enqueue(h.writeCompose(yaml))
	h.eng.Tick(ctx)
	row := mustGet(h, rec.ID)
	if row.Status != state.DeployReleasing || row.Phase != state.PhaseInitJobs {
		t.Fatalf("deployment after start = %s/%q (%s), want releasing/init_jobs",
			row.Status, row.Phase, row.ErrorCode)
	}
	jobName := h.initJobServiceName(row, "migrate")
	if _, ok := h.sub.services[jobName]; !ok {
		t.Fatalf("init job service %s not created on the release start tick", jobName)
	}
	return row, jobName
}

// finishJobTask 把 job 服务的任务实况改为终态（complete/failed；Image 与
// 建服务时的 spec 一致——任务归属判定面）。
func finishJobTask(t *testing.T, h *harness, jobName, taskState, errMsg string) {
	t.Helper()
	svc, ok := h.sub.services[jobName]
	if !ok {
		t.Fatalf("init job service %s not found", jobName)
	}
	h.sub.setExternalTasks(jobName, []TaskState{{
		ID: "t-init-1", State: taskState, DesiredState: "running", Err: errMsg, Image: svc.spec.Image,
	}})
}

// eventPayloadOf 返回某事件名的首条 payload（脱敏 JSON）。
func eventPayloadOf(t *testing.T, h *harness, name string) string {
	t.Helper()
	for _, ev := range mustEvents(t, h) {
		if ev.Name == name {
			return ev.Payload
		}
	}
	t.Fatalf("event %s not found", name)
	return ""
}

// stringInList 报告字符串是否在列表中。
func stringInList(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestInitJobFailureFailsReleaseBeforePromotion 守卫①：init job 任务 failed
// → release.job_failed 事件 + 部署终态 failed/E_INIT_JOB_FAILED（走
// failUnswitchedOrSwitched 唯一入口；首发无版本可归位 → scale=0 保留现场）；
// 失败前长驻服务未按新 spec 对账（迁移先于新代码的迁移安全面）。
func TestInitJobFailureFailsReleaseBeforePromotion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row, jobName := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))

	// 执行形态快照（job 服务 = 模板克隆的执行投影）。
	spec := h.sub.services[jobName].spec
	if !spec.Job || spec.Global || spec.Replicas != 1 {
		t.Fatalf("job spec mode wrong: job=%v global=%v replicas=%d", spec.Job, spec.Global, spec.Replicas)
	}
	if spec.RestartPolicy == nil || spec.RestartPolicy.Condition != "none" {
		t.Fatalf("restart policy = %+v, want condition=none (fail once, no retry)", spec.RestartPolicy)
	}
	if !naming.IsInitJobName(spec.Name) || spec.Name != jobName {
		t.Fatalf("job service name = %s, want the deterministic init family form", spec.Name)
	}
	app := h.demoApp()
	for key, want := range map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     app.QualifiedName(),
		state.LabelProcess: "migrate",
		state.LabelTeam:    app.TeamSlug,
		state.LabelProject: app.ProjectSlug,
		state.LabelInitRun: row.ID,
	} {
		if spec.ServiceLabels[key] != want {
			t.Fatalf("job label %s = %q, want %q (labels=%v)", key, spec.ServiceLabels[key], want, spec.ServiceLabels)
		}
	}
	if _, ok := spec.ServiceLabels[state.LabelDeployment]; ok {
		t.Fatal("job service inherited the deployment label (ownership labels are long-running lies)")
	}
	if _, ok := spec.ServiceLabels[state.LabelDesiredHash]; ok {
		t.Fatal("job service inherited the desired-hash label")
	}
	if !h.sub.networks[h.demoNet()] {
		t.Fatal("app network was not ensured before the job service create")
	}

	// 迁移安全：job 在途期间长驻服务不得按新 spec 对账。
	if _, ok := h.sub.services[h.svc("web")]; ok {
		t.Fatal("long-running service reconciled while the init job was still in flight (migration safety broken)")
	}
	if len(h.sub.updates) != 0 {
		t.Fatalf("substrate updates before job completion: %v", h.sub.updates)
	}

	// job 任务失败 → 事件 + 终态（fail-fast，不重试）。
	finishJobTask(t, h, jobName, "failed", "exit code 1 (migration failed)")
	final := h.runToTerminal(row)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_INIT_JOB_FAILED" {
		t.Fatalf("deploy = %s (%s), want failed E_INIT_JOB_FAILED", final.Status, final.ErrorCode)
	}
	if final.Recovery != "" {
		t.Fatalf("recovery = %q, want none (first deploy has no previous revision to replay)", final.Recovery)
	}
	if !final.SubstrateHalted {
		t.Fatal("first-deploy init job failure did not keep the scene at scale=0")
	}
	names := h.events()
	if !hasEvent(names, "release.job_failed") {
		t.Fatalf("missing release.job_failed: %v", names)
	}
	if hasEvent(names, "release.job_timed_out") {
		t.Fatalf("unexpected release.job_timed_out: %v", names)
	}
	if !hasEvent(names, "deployment.failed") {
		t.Fatalf("missing deployment.failed: %v", names)
	}
	payload := eventPayloadOf(t, h, "release.job_failed")
	if !strings.Contains(payload, `"job_service":"`+jobName+`"`) || !strings.Contains(payload, "exit code 1") {
		t.Fatalf("release.job_failed payload = %s, want job_service and the task failure reason", payload)
	}
	// 新代码未晋级：长驻服务从未被创建。
	if _, ok := h.sub.services[h.svc("web")]; ok {
		t.Fatal("long-running service created despite the init job failure (new code must not be promoted)")
	}
	if got, err := h.eng.AppDerivedState(ctx, row.AppID); err != nil || got != DerivedDown {
		t.Fatalf("app state = %s (%v), want down", got, err)
	}
}

// TestInitJobTimeoutFailsReleaseWithTimedOutEvent 守卫②（per-job label 预算）：
// fleetly.job.timeout 驱动的看门狗 → release.job_timed_out 事件 +
// E_INIT_JOB_TIMED_OUT + failed；预算锚 = release_started_at（相位兜底同值）。
func TestInitJobTimeoutFailsReleaseWithTimedOutEvent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row, jobName := startInitJobRelease(t, h,
		initJobComposeFixture("alpine:3", "      fleetly.job.timeout: 30s\n"))

	if want := row.ReleaseStartedAt.Add(30 * time.Second); !row.WatchdogDeadlineAt.Equal(want) {
		t.Fatalf("watchdog deadline = %v, want release_started_at+30s = %v", row.WatchdogDeadlineAt, want)
	}
	h.clk.Advance(31 * time.Second)
	h.eng.Tick(ctx)
	final := mustGet(h, row.ID)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_INIT_JOB_TIMED_OUT" {
		t.Fatalf("deploy = %s (%s), want failed E_INIT_JOB_TIMED_OUT", final.Status, final.ErrorCode)
	}
	names := h.events()
	if !hasEvent(names, "release.job_timed_out") {
		t.Fatalf("missing release.job_timed_out: %v", names)
	}
	if hasEvent(names, "release.job_failed") {
		t.Fatalf("unexpected release.job_failed on timeout: %v", names)
	}
	payload := eventPayloadOf(t, h, "release.job_timed_out")
	if !strings.Contains(payload, `"job_service":"`+jobName+`"`) || !strings.Contains(payload, `"budget":"30s"`) {
		t.Fatalf("release.job_timed_out payload = %s, want job_service and the per-job budget", payload)
	}
	if _, ok := h.sub.services[jobName]; ok {
		t.Fatal("timed-out init job service left behind")
	}
}

// TestInitJobPlatformDefaultBudgetTimesOut 守卫②（平台缺省预算）：无
// fleetly.job.timeout label 时预算 = Config.InitJobTimeout（DefaultJobTimeout
// 10m），相位兜底 deadline 与之锚定；越过即超时失败。
func TestInitJobPlatformDefaultBudgetTimesOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if h.eng.cfg.InitJobTimeout != DefaultJobTimeout {
		t.Fatalf("platform init job budget = %s, want the shared default %s",
			h.eng.cfg.InitJobTimeout, DefaultJobTimeout)
	}
	row, _ := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))
	if want := row.ReleaseStartedAt.Add(DefaultJobTimeout); !row.WatchdogDeadlineAt.Equal(want) {
		t.Fatalf("watchdog deadline = %v, want release_started_at+%s = %v",
			row.WatchdogDeadlineAt, DefaultJobTimeout, want)
	}
	h.clk.Advance(DefaultJobTimeout + time.Second)
	h.eng.Tick(ctx)
	final := mustGet(h, row.ID)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_INIT_JOB_TIMED_OUT" {
		t.Fatalf("deploy = %s (%s), want failed E_INIT_JOB_TIMED_OUT", final.Status, final.ErrorCode)
	}
	payload := eventPayloadOf(t, h, "release.job_timed_out")
	if !strings.Contains(payload, `"budget":"`+DefaultJobTimeout.String()+`"`) {
		t.Fatalf("release.job_timed_out payload = %s, want the platform default budget", payload)
	}
}

// TestInitJobInjectedPlatformBudgetTimesOut 守卫②（注入位）：engine.Config
// 的 InitJobTimeout 作为平台缺省预算的注入点生效（装配层可调；label 缺省
// 时读它），无 label 的 job 按注入值计时。
func TestInitJobInjectedPlatformBudgetTimesOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.eng = NewEngine(Config{InitJobTimeout: 15 * time.Second},
		h.store, h.sub, h.images, h.resolver, h.box,
		slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(h.clk)

	row, _ := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))
	if want := row.ReleaseStartedAt.Add(15 * time.Second); !row.WatchdogDeadlineAt.Equal(want) {
		t.Fatalf("watchdog deadline = %v, want the injected budget release_started_at+15s = %v",
			row.WatchdogDeadlineAt, want)
	}
	h.clk.Advance(16 * time.Second)
	h.eng.Tick(ctx)
	final := mustGet(h, row.ID)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_INIT_JOB_TIMED_OUT" {
		t.Fatalf("deploy = %s (%s), want failed E_INIT_JOB_TIMED_OUT on the injected budget", final.Status, final.ErrorCode)
	}
	payload := eventPayloadOf(t, h, "release.job_timed_out")
	if !strings.Contains(payload, `"budget":"15s"`) {
		t.Fatalf("release.job_timed_out payload = %s, want the injected budget", payload)
	}
}

// TestInitJobTimeoutIsPerJobBudget 守卫②（per-job 独立计时，冻结裁决第 3 条）：
// 预算不同的两个 job——小预算 job 在自身预算处超时，不等待相位级兜底
// deadline（= max(各预算)）；事件点名该 job 与其预算，不误归因另一只。
func TestInitJobTimeoutIsPerJobBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec := h.enqueue(h.writeCompose(`name: demo
services:
  web:
    image: alpine:3
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
  migrate-a:
    image: alpine:3
    command: ["true"]
    labels:
      fleetly.job: init
      fleetly.job.timeout: 30s
  migrate-b:
    image: alpine:3
    command: ["true"]
    labels:
      fleetly.job: init
`))
	h.eng.Tick(ctx)
	row := mustGet(h, rec.ID)
	if row.Status != state.DeployReleasing || row.Phase != state.PhaseInitJobs {
		t.Fatalf("deployment after start = %s/%q (%s), want releasing/init_jobs", row.Status, row.Phase, row.ErrorCode)
	}
	// 相位兜底 = max(各预算) = 10m（migrate-b 无 label 取平台缺省）；migrate-a
	// 的 30s per-job 预算必须独立生效（否则本拍不判超时、部署滞留）。
	if want := row.ReleaseStartedAt.Add(DefaultJobTimeout); !row.WatchdogDeadlineAt.Equal(want) {
		t.Fatalf("phase deadline = %v, want max budget release_started_at+%s = %v",
			row.WatchdogDeadlineAt, DefaultJobTimeout, want)
	}
	jobA := h.initJobServiceName(row, "migrate-a")
	h.clk.Advance(31 * time.Second)
	h.eng.Tick(ctx)
	final := mustGet(h, row.ID)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_INIT_JOB_TIMED_OUT" {
		t.Fatalf("deploy = %s (%s), want failed E_INIT_JOB_TIMED_OUT (per-job budget must fire before the phase fallback)",
			final.Status, final.ErrorCode)
	}
	payload := eventPayloadOf(t, h, "release.job_timed_out")
	if !strings.Contains(payload, `"job_service":"`+jobA+`"`) || !strings.Contains(payload, `"budget":"30s"`) {
		t.Fatalf("release.job_timed_out payload = %s, want the per-job budget and %s", payload, jobA)
	}
}

// TestMultipleInitJobsPromoteOnlyAfterAllPass 守卫③：两个 init job 并行、
// 交错完成——job 全过前不得对账长驻服务（无 create/update、相位不清、
// 无 healthy）；全过后相位清位、job 清场、长驻对账执行、健康门/观察窗
// 照旧走完。
func TestMultipleInitJobsPromoteOnlyAfterAllPass(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// v1：只有长驻 web（有效版本在位）。
	if final := h.runToTerminal(h.enqueue(h.writeCompose(composeV1))); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	webName := h.svc("web")
	v1Image := h.sub.services[webName].spec.Image
	updatesBefore := len(h.sub.updates)
	healthyBefore := countEventsByName(t, h, "deployment.healthy")

	// v2：web 新镜像 + 两个 init job。
	rec := h.enqueue(h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
  migrate-a:
    image: alpine:3
    command: ["true"]
    labels:
      fleetly.job: init
  migrate-b:
    image: alpine:3
    command: ["true"]
    labels:
      fleetly.job: init
`))
	h.eng.Tick(ctx)
	row := mustGet(h, rec.ID)
	if row.Status != state.DeployReleasing || row.Phase != state.PhaseInitJobs {
		t.Fatalf("deployment after start = %s/%q (%s), want releasing/init_jobs", row.Status, row.Phase, row.ErrorCode)
	}
	jobA, jobB := h.initJobServiceName(rec, "migrate-a"), h.initJobServiceName(rec, "migrate-b")
	if _, ok := h.sub.services[jobA]; !ok {
		t.Fatal("migrate-a job service not created")
	}
	if _, ok := h.sub.services[jobB]; !ok {
		t.Fatal("migrate-b job service not created")
	}

	// A 先过、B 仍在途：不得晋级。
	finishJobTask(t, h, jobA, "complete", "")
	h.clk.Advance(2 * time.Second)
	h.eng.Tick(ctx)
	row = mustGet(h, rec.ID)
	if row.Status != state.DeployReleasing || row.Phase != state.PhaseInitJobs {
		t.Fatalf("promoted with one job still in flight: %s/%q", row.Status, row.Phase)
	}
	if h.sub.services[webName].spec.Image != v1Image {
		t.Fatal("long-running service updated to the new spec before all init jobs passed")
	}
	if len(h.sub.updates) != updatesBefore {
		t.Fatalf("applyDesired ran before all init jobs passed: %v", h.sub.updates)
	}
	if got := countEventsByName(t, h, "deployment.healthy"); got != healthyBefore {
		t.Fatal("healthy switch emitted before all init jobs passed")
	}
	if _, ok := h.sub.services[jobA]; !ok {
		t.Fatal("completed job service removed before the phase finished (must wait for the whole set)")
	}

	// B 完成：全过 → 相位清位 + job 清场 + 长驻对账执行。
	finishJobTask(t, h, jobB, "complete", "")
	h.clk.Advance(2 * time.Second)
	h.eng.Tick(ctx)
	row = mustGet(h, rec.ID)
	if row.Status != state.DeployReleasing || row.Phase != "" {
		t.Fatalf("after all jobs passed = %s/%q, want releasing with a cleared phase", row.Status, row.Phase)
	}
	if len(h.sub.updates) != updatesBefore+1 || !strings.Contains(h.sub.updates[len(h.sub.updates)-1][1], "alpine:4") {
		t.Fatalf("long-running reconcile after promotion = %v, want one update to the new image", h.sub.updates)
	}
	for _, name := range []string{jobA, jobB} {
		if _, ok := h.sub.services[name]; ok {
			t.Fatalf("job service %s left behind after promotion", name)
		}
	}

	// 晋级后健康门/观察窗照旧：终态 succeeded + healthy 事件。
	final := h.runToTerminal(rec)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("v2 = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	if got := countEventsByName(t, h, "deployment.healthy"); got != healthyBefore+1 {
		t.Fatalf("deployment.healthy count = %d, want %d (exactly one switch after promotion)", got, healthyBefore+1)
	}
}

// TestInitJobServicesLeaveNoResidue 守卫④（三路径零残留）：成功/失败/超时
// 路径后 fleetly-init- 服务被移除（服务表 + ServiceRemove 调用面双断言）。
func TestInitJobServicesLeaveNoResidue(t *testing.T) {
	cases := []struct {
		name     string
		wantCode string
		run      func(t *testing.T, h *harness) (state.DeployRecord, string)
	}{
		{
			name: "success",
			run: func(t *testing.T, h *harness) (state.DeployRecord, string) {
				row, jobName := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))
				finishJobTask(t, h, jobName, "complete", "")
				return h.runToTerminal(row), jobName
			},
		},
		{
			name:     "failed",
			wantCode: "E_INIT_JOB_FAILED",
			run: func(t *testing.T, h *harness) (state.DeployRecord, string) {
				row, jobName := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))
				finishJobTask(t, h, jobName, "failed", "exit code 1")
				return h.runToTerminal(row), jobName
			},
		},
		{
			name:     "timed_out",
			wantCode: "E_INIT_JOB_TIMED_OUT",
			run: func(t *testing.T, h *harness) (state.DeployRecord, string) {
				row, jobName := startInitJobRelease(t, h,
					initJobComposeFixture("alpine:3", "      fleetly.job.timeout: 1s\n"))
				h.clk.Advance(2 * time.Second)
				h.eng.Tick(context.Background())
				return mustGet(h, row.ID), jobName
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			final, jobName := tc.run(t, h)
			if tc.wantCode == "" {
				if final.Status != state.DeploySucceeded {
					t.Fatalf("deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
				}
			} else if final.Status != state.DeployFailed || final.ErrorCode != tc.wantCode {
				t.Fatalf("deploy = %s (%s), want failed %s", final.Status, final.ErrorCode, tc.wantCode)
			}
			if _, ok := h.sub.services[jobName]; ok {
				t.Fatalf("init job service %s left behind after the release finished", jobName)
			}
			if !stringInList(h.sub.removed, jobName) {
				t.Fatalf("ServiceRemove was not called for %s: %v", jobName, h.sub.removed)
			}
			for name := range h.sub.services {
				if naming.IsInitJobName(name) {
					t.Fatalf("residual init job service after %s: %s", tc.name, name)
				}
			}
		})
	}
}

// TestSweepInitJobsClearsOrphansWithinOneScan 守卫④（孤儿清扫兜底）：一个
// 扫描周期内清除全部无管线所有者的 fleetly-init- 服务；非终态且仍在
// init_jobs 相位的在途 job 与普通长驻服务不得误伤。
func TestSweepInitJobsClearsOrphansWithinOneScan(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app := h.demoApp()

	seed := func(name string, labels map[string]string) {
		h.sub.services[name] = &fakeService{
			spec: ServiceSpec{Name: name, Replicas: 1, ServiceLabels: labels},
		}
	}
	initLabels := func(deployID string) map[string]string {
		return map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     app.QualifiedName(),
			state.LabelProcess: "migrate",
			state.LabelInitRun: deployID,
		}
	}

	// ① 归属部署行缺失。
	missingName := "fleetly-init-acme-prod-demo-migrate-missing1"
	seed(missingName, initLabels("dep-missing"))

	// ② 归属部署行已终态。
	terminalRec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy", SpecHash: "h-terminal",
	})
	if err != nil {
		t.Fatalf("create terminal deployment: %v", err)
	}
	if err := h.store.EnterPhase(ctx, terminalRec.ID, state.DeployQueued, state.DeployFailed, state.DeploymentPatch{}); err != nil {
		t.Fatalf("terminalize deployment: %v", err)
	}
	terminalName := "fleetly-init-acme-prod-demo-migrate-terminal1"
	seed(terminalName, initLabels(terminalRec.ID))

	// ③ 归属部署行在途但相位已离开 init_jobs。
	clearedRec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy", SpecHash: "h-cleared",
	})
	if err != nil {
		t.Fatalf("create cleared-phase deployment: %v", err)
	}
	clearedName := "fleetly-init-acme-prod-demo-migrate-cleared1"
	seed(clearedName, initLabels(clearedRec.ID))

	// ④ 归属部署行在途且相位 = init_jobs：管线自有对象，不得误清。
	liveRec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy", SpecHash: "h-live",
	})
	if err != nil {
		t.Fatalf("create live-phase deployment: %v", err)
	}
	phase := state.PhaseInitJobs
	if err := h.store.UpdateDeployment(ctx, liveRec.ID, state.DeploymentPatch{Phase: &phase}); err != nil {
		t.Fatalf("set live phase: %v", err)
	}
	liveName := "fleetly-init-acme-prod-demo-migrate-live1"
	seed(liveName, initLabels(liveRec.ID))

	// ⑤ 非 init 前缀的受管服务不在清扫域。
	longRunning := h.svc("web")
	seed(longRunning, map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     app.QualifiedName(),
		state.LabelProcess: "web",
	})

	h.eng.SweepInitJobs(ctx)

	for _, name := range []string{missingName, terminalName, clearedName} {
		if _, ok := h.sub.services[name]; ok {
			t.Fatalf("orphan init job service %s survived one sweep", name)
		}
	}
	if _, ok := h.sub.services[liveName]; !ok {
		t.Fatal("in-flight init job service (deployment still in the init phase) was removed by the orphan sweep")
	}
	if _, ok := h.sub.services[longRunning]; !ok {
		t.Fatal("non-init managed service removed by the init orphan sweep")
	}
}

// TestApplyDesiredSparesInitJobServices 守卫④配套（对账删除豁免）：
// applyDesired 的省略=删除扫描不得删除 fleetly-init- 前缀的在途 job 服务；
// 普通多余服务照删（cron 豁免同款纪律）。
func TestApplyDesiredSparesInitJobServices(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	initLabels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     "app1",
		state.LabelProcess: "migrate",
		state.LabelInitRun: "dep1",
	}
	staleLabels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     "app1",
		state.LabelProcess: "stale",
	}
	h.sub.services["fleetly-init-app1-migrate-abc"] = &fakeService{
		spec: ServiceSpec{Name: "fleetly-init-app1-migrate-abc", Replicas: 1, ServiceLabels: initLabels},
	}
	h.sub.services["fleetly-app1-stale"] = &fakeService{
		spec: ServiceSpec{Name: "fleetly-app1-stale", Replicas: 1, ServiceLabels: staleLabels},
	}
	desired := []ServiceSpec{{
		Name:     "fleetly-app1-web",
		Image:    "repo/app:1@sha256:abc",
		Replicas: 1,
		ServiceLabels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     "app1",
			state.LabelProcess: "web",
		},
		UpdateOrder: "start-first",
	}}
	rec := state.DeployRecord{ID: "dep1", AppID: "id1", AppName: "app1"}
	if err := h.eng.applyDesired(ctx, rec, desired, false); err != nil {
		t.Fatalf("applyDesired: %v", err)
	}
	if _, ok := h.sub.services["fleetly-init-app1-migrate-abc"]; !ok {
		t.Fatal("in-flight init job service was removed by the deploy reconcile (must be spared)")
	}
	if _, ok := h.sub.services["fleetly-app1-stale"]; ok {
		t.Fatal("stale long-running service was not removed (omit=delete broken)")
	}
	if _, ok := h.sub.services["fleetly-app1-web"]; !ok {
		t.Fatal("desired service not created")
	}
}

// TestReleaseWithoutInitJobsIsUnchanged 守卫⑤：无 init job 的 release 路径
// 行为零变化——相位列零写（全程无 init_jobs）、无 release.job_* 事件、
// applyDesired 仍在计划拍内执行（首拍即切流 observing）、看门狗预算 =
// DeployTimeout。
func TestReleaseWithoutInitJobsIsUnchanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec := h.enqueue(h.writeCompose(composeV1))

	// 首拍：计划拍内对账 + 健康切流（与既有语义逐字一致）。
	h.eng.Tick(ctx)
	first := mustGet(h, rec.ID)
	if first.Status != state.DeployObserving {
		t.Fatalf("first tick status = %s (%s), want observing (applyDesired still runs inside the planning tick)",
			first.Status, first.ErrorCode)
	}
	if first.Phase != "" {
		t.Fatalf("phase = %q on an init-less release, want zero write", first.Phase)
	}
	if !first.WatchdogDeadlineAt.Equal(first.ReleaseStartedAt.Add(h.eng.cfg.DeployTimeout)) {
		t.Fatalf("watchdog deadline = %v, want release_started_at+DeployTimeout = %v",
			first.WatchdogDeadlineAt, first.ReleaseStartedAt.Add(h.eng.cfg.DeployTimeout))
	}
	if len(h.sub.services) != 1 {
		t.Fatalf("services after the planning tick = %d, want only the long-running web", len(h.sub.services))
	}
	if _, err := h.sub.ServiceInspect(ctx, h.svc("web")); err != nil {
		t.Fatalf("long-running service not created in the planning tick: %v", err)
	}
	if len(h.sub.updates) != 0 {
		t.Fatalf("unexpected updates on an init-less first deploy: %v", h.sub.updates)
	}

	// 余下逐拍至终态：相位列恒空、无 job 事件、终态 succeeded。
	for i := 0; i < 64; i++ {
		row := mustGet(h, rec.ID)
		if row.Phase != "" {
			t.Fatalf("phase written on the init-less release path: %q", row.Phase)
		}
		if row.Status.Terminal() {
			break
		}
		h.clk.Advance(2 * time.Second)
		h.eng.Tick(ctx)
	}
	final := mustGet(h, rec.ID)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	for _, name := range h.events() {
		if strings.HasPrefix(name, "release.job_") {
			t.Fatalf("init job event %s leaked into an init-less release", name)
		}
	}
}

// TestInitJobRestartRecoveryResumesIdempotently 补充（重启续跑幂等）：
// 同一部署连续两拍不重建 job 服务（ServiceInspect→在位即跳过）；控制面
// 重启（phase=init_jobs）不被误分类为 E_DEPLOY_INTERRUPTED，新引擎实例
// 续跑同一确定性命名的服务直至成功。
func TestInitJobRestartRecoveryResumesIdempotently(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row, jobName := startInitJobRelease(t, h, initJobComposeFixture("alpine:3", ""))
	created := h.sub.services[jobName]

	// 同部署连续两拍：job 服务不得重建（指针身份 + 版本不变）。
	h.clk.Advance(2 * time.Second)
	h.eng.Tick(ctx)
	if h.sub.services[jobName] != created || created.version != 1 {
		t.Fatalf("init job service recreated within one deployment (version=%d)", created.version)
	}

	// 控制面重启：phase=init_jobs 直接放行回 tick 续跑（不走立即失败分类）。
	eng2 := NewEngine(Config{}, h.store, h.sub, h.images, h.resolver, h.box,
		slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(h.clk)
	eng2.recoverInterrupted(ctx)
	after := mustGet(h, row.ID)
	if after.Status != state.DeployReleasing || after.Phase != state.PhaseInitJobs {
		t.Fatalf("restart rewrote the init phase row: status=%s phase=%s", after.Status, after.Phase)
	}
	if after.ErrorCode != "" {
		t.Fatalf("restart wrote error_code %q on an in-flight init row", after.ErrorCode)
	}
	h.clk.Advance(2 * time.Second)
	eng2.Tick(ctx)
	if h.sub.services[jobName] != created || created.version != 1 {
		t.Fatal("restart recreated the in-flight init job service (must resume idempotently by deterministic name)")
	}

	// 任务完成 → 新实例续跑晋级至成功。
	finishJobTask(t, h, jobName, "complete", "")
	final := h.runToTerminalWith(row, eng2)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("resumed deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	if _, ok := h.sub.services[jobName]; ok {
		t.Fatal("job service left behind after the resumed release finished")
	}
}

// TestBuildPlanInitJobTemplateSnapshot 补充（规划/快照面）：init job 服务
// 不进长驻对账集（plan.Services），进 plan.InitJobs 与快照（Job+InitJob
// 双标记、digest 钉定镜像、label 预算解析、警告披露）——重启续跑与在途
// 评估的统一执行形态来源。
func TestBuildPlanInitJobTemplateSnapshot(t *testing.T) {
	spec, _ := planFixture(t, `name: app1
services:
  web:
    image: repo/app:1
  migrate:
    image: repo/migrate:1
    labels:
      fleetly.job: init
      fleetly.job.timeout: 45m
`)
	plan, err := BuildPlan(PlanInput{
		AppID:        "app1id",
		AppName:      "app1",
		TeamSlug:     "acme",
		PrjSlug:      "prod",
		DeploymentID: "dep1",
		Spec:         spec,
		FileEnv:      map[string]map[string]string{},
		ComposeEnv:   map[string]map[string]string{},
		PlatformEnv:  []envlayer.PlatformVar{},
		Images: map[string]string{
			"web":     "repo/app:1@sha256:abc",
			"migrate": "repo/migrate:1@sha256:def",
		},
		Decision: placement.Decision{},
		Volumes:  nil,
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	webName, _ := naming.ServiceName("acme", "prod", "app1", "web")
	if len(plan.Services) != 1 || plan.Services[0].Name != webName {
		t.Fatalf("long-running set = %+v, want only %s", plan.Services, webName)
	}
	migrateName, _ := naming.ServiceName("acme", "prod", "app1", "migrate")
	if len(plan.InitJobs) != 1 {
		t.Fatalf("init jobs = %d, want 1", len(plan.InitJobs))
	}
	job := plan.InitJobs[0]
	if job.Name != migrateName || !job.Job || !job.InitJob {
		t.Fatalf("init job template wrong: %+v", job)
	}
	if job.Image != "repo/migrate:1@sha256:def" {
		t.Fatalf("init job image = %s, want the digest-pinned reference", job.Image)
	}
	if job.InitJobTimeout != 45*time.Minute {
		t.Fatalf("init job timeout = %s, want 45m (fleetly.job.timeout normalized)", job.InitJobTimeout)
	}
	// 快照保留模板：同一 JSON 即重启续跑的执行形态来源。
	var all []ServiceSpec
	if err := json.Unmarshal(plan.DesiredSpecJSON, &all); err != nil {
		t.Fatalf("decode desired-spec snapshot: %v", err)
	}
	jobs := filterInitJobs(all)
	if len(jobs) != 1 || jobs[0].Name != migrateName || !jobs[0].InitJob {
		t.Fatalf("snapshot init jobs = %+v, want the migrate template", jobs)
	}
	found := false
	for _, w := range plan.Warnings {
		if w.Service == "migrate" && w.Kind == compose.WarningKindInitJobDeclared {
			found = true
		}
	}
	if !found {
		t.Fatalf("init job plan warning missing: %+v", plan.Warnings)
	}
}

// TestInitJobEnvironmentProjectionSharedSource 补充（投影同源抽样）：init
// job 服务的 env 三层合并结果与同 compose 的长驻服务逐字一致（buildServiceSpec
// 共用同一装配点；平台层 pending env 同样注入）。
func TestInitJobEnvironmentProjectionSharedSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app, err := ensureAppForTest(t, ctx, h.store, "demo")
	if err != nil {
		t.Fatalf("ensure app: %v", err)
	}
	cipher, err := h.box.Encrypt([]byte("shared-value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.store.SetAppEnv(ctx, app.ID, "DEMO_TOKEN", string(cipher), "platform", "human"); err != nil {
		t.Fatalf("set app env: %v", err)
	}

	row, jobName := startInitJobRelease(t, h, `name: demo
services:
  web:
    image: alpine:3
    command: ["sleep", "infinity"]
    environment:
      SHARED_FLAG: "on"
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
  migrate:
    image: alpine:3
    command: ["true"]
    environment:
      SHARED_FLAG: "on"
    labels:
      fleetly.job: init
`)
	jobEnv := h.sub.services[jobName].spec.Env
	finishJobTask(t, h, jobName, "complete", "")
	if final := h.runToTerminal(row); final.Status != state.DeploySucceeded {
		t.Fatalf("deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	webEnv := h.sub.services[h.svc("web")].spec.Env

	if len(jobEnv) != len(webEnv) {
		t.Fatalf("env projection length diverged: job=%v web=%v", envKeysOf(jobEnv), envKeysOf(webEnv))
	}
	webValues := map[string]string{}
	for _, kv := range webEnv {
		key, value, _ := strings.Cut(kv, "=")
		webValues[key] = value
	}
	for _, kv := range jobEnv {
		key, value, _ := strings.Cut(kv, "=")
		if webValues[key] != value {
			t.Fatalf("env projection diverged at key %s (job and web must share the same merge source)", key)
		}
	}
	if webValues["SHARED_FLAG"] != "on" || webValues["DEMO_TOKEN"] != "shared-value" {
		t.Fatalf("merged env keys = %v, want compose env + platform pending env", envKeysOf(jobEnv))
	}
}

// envKeysOf 返回 env 列表的 key 集（失败信息用——值不进输出）。
func envKeysOf(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		out = append(out, key)
	}
	return out
}
