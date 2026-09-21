package cron

// 调度器 hermetic 测试（真实 SQLite + 假底座/假前哨/假时钟）：到点触发、
// 重叠 skip、节点前哨 skip、完成/失败收口、看门狗超时、错过点不补跑与披
// 露、启动残留收口（在途行按任务实况收口 + 孤儿 job 服务清扫）、手动触发
// 同路径 + 审计。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeClock 是可拨动的时钟。
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// fakeSub 是内存底座：服务集 + 每服务任务态（按服务名给任务终态）。
type fakeSub struct {
	services map[string]engine.ServiceState
	tasks    map[string][]engine.TaskState
	created  []string
	removed  []string
	nets     map[string]bool
}

func newFakeSub() *fakeSub {
	return &fakeSub{
		services: map[string]engine.ServiceState{},
		tasks:    map[string][]engine.TaskState{},
		nets:     map[string]bool{},
	}
}

func (f *fakeSub) SwarmReady(context.Context) error { return nil }
func (f *fakeSub) NetworkEnsure(_ context.Context, name string) error {
	f.nets[name] = true
	return nil
}
func (f *fakeSub) ServiceInspect(_ context.Context, name string) (engine.ServiceState, error) {
	s, ok := f.services[name]
	if !ok {
		return engine.ServiceState{}, fmt.Errorf("%w: %s", engine.ErrServiceNotFound, name)
	}
	return s, nil
}
func (f *fakeSub) ServiceCreate(_ context.Context, spec engine.ServiceSpec) error {
	if _, dup := f.services[spec.Name]; dup {
		return errors.New("conflict")
	}
	f.services[spec.Name] = engine.ServiceState{Name: spec.Name, Labels: spec.ServiceLabels}
	f.created = append(f.created, spec.Name)
	return nil
}
func (f *fakeSub) ServiceUpdate(context.Context, string, engine.ServiceSpec) error { return nil }
func (f *fakeSub) ServiceRemove(_ context.Context, name string) error {
	delete(f.services, name)
	delete(f.tasks, name)
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeSub) ServiceList(_ context.Context, labels map[string]string) ([]engine.ServiceState, error) {
	var out []engine.ServiceState
	for _, s := range f.services {
		match := true
		for k, v := range labels {
			if s.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeSub) TaskList(_ context.Context, service string) ([]engine.TaskState, error) {
	return f.tasks[service], nil
}

// failPref / fakePreflight 是前哨端口假件。
type fakePreflight struct{ err error }

func (p fakePreflight) Preflight(context.Context, string) error { return p.err }

// harness 是 cron 调度器测试环境。
type harness struct {
	t     *testing.T
	store *state.Store
	box   *secrets.Box
	sub   *fakeSub
	clk   *fakeClock
	mgr   *Manager
	app   state.App
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "fleetly.key"))
	if err != nil {
		t.Fatalf("ensure key: %v", err)
	}
	sub := newFakeSub()
	clk := &fakeClock{now: time.Date(2026, 9, 21, 12, 0, 5, 0, time.UTC)}
	mgr := NewManager(Config{}, st, box, sub, fakePreflight{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(clk)
	app, err := st.CreateApp(context.Background(), "", "cronapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return &harness{t: t, store: st, box: box, sub: sub, clk: clk, mgr: mgr, app: app}
}

// arm 预置一个已武装的 schedule：最新 revision 的 compose 快照带 fleetly.cron
// 声明 + 最新 succeeded 部署快照带 Job 模板（真实 BuildPlan 产物形态的直构
// 等价——internal/engine 侧另有规划单测钉住该产物的存在性）。
func (h *harness) arm(expression, service string) {
	h.t.Helper()
	ctx := context.Background()
	spec := compose.Spec{
		Name: "cronapp",
		Services: []compose.Service{{
			Name:  service,
			Image: "busybox",
			Cron:  &compose.CronSchedule{Expression: expression},
		}},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		h.t.Fatalf("marshal compose snapshot: %v", err)
	}
	if err := h.store.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.CreateRevision(ctx, state.RevisionWrite{
			AppID:             h.app.ID,
			ComposeNormalized: string(raw),
			Overlay:           "{}",
			DesiredHash:       "h",
		})
		return err
	}); err != nil {
		h.t.Fatalf("create revision: %v", err)
	}
	template := engine.ServiceSpec{
		Name:     "fleetly-cronapp-" + service,
		Image:    "busybox@sha256:aa",
		Job:      true,
		Replicas: 1,
		Networks: []engine.NetworkAttach{{Name: "fleetly-cronapp-net", Aliases: []string{service}}},
		ServiceLabels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     h.app.Name,
			state.LabelProcess: service,
		},
		ContainerLabels: map[string]string{state.LabelApp: h.app.Name},
	}
	snapshot, err := json.Marshal([]engine.ServiceSpec{template})
	if err != nil {
		h.t.Fatalf("marshal snapshot: %v", err)
	}
	cipher, err := h.box.Encrypt(snapshot)
	if err != nil {
		h.t.Fatalf("encrypt snapshot: %v", err)
	}
	rec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID:       h.app.ID,
		AppName:     h.app.Name,
		Kind:        "deploy",
		SpecHash:    spec.SpecHash,
		DesiredSpec: string(cipher),
	})
	if err != nil {
		h.t.Fatalf("create deployment: %v", err)
	}
	// CreateDeployment 固定落 queued——测试夹具沿合法转移链推进到 succeeded
	//（queued→preparing→releasing→observing→succeeded；UpdateDeployment 的
	// CAS 分支按转移表校验，夹具不可跳步）。
	walk := []state.DeploymentStatus{
		state.DeployPreparing, state.DeployReleasing, state.DeployObserving, state.DeploySucceeded,
	}
	from := state.DeployQueued
	for _, to := range walk {
		if err := h.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{Status: &to, PrevStatus: &from}); err != nil {
			h.t.Fatalf("walk to %s: %v", to, err)
		}
		from = to
	}
	_ = rec
}

// beat 驱动一拍。
func (h *harness) beat() {
	h.t.Helper()
	h.mgr.beat(context.Background())
}

// runs 返回台账行。
func (h *harness) runs(service string) []state.CronRun {
	h.t.Helper()
	rows, err := h.store.ListCronRuns(context.Background(), h.app.ID, service, 100)
	if err != nil {
		h.t.Fatalf("list cron runs: %v", err)
	}
	return rows
}

// eventNames 返回至今全部事件名。
func (h *harness) eventNames() map[string]int {
	h.t.Helper()
	rows, err := h.store.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		h.t.Fatalf("list events: %v", err)
	}
	out := map[string]int{}
	for _, e := range rows {
		out[e.Name]++
	}
	return out
}

// jobServices 提取台账行里的 job 服务名（skipped 行为空）。
func jobServiceOf(rows []state.CronRun) string {
	for _, r := range rows {
		if r.JobService != "" {
			return r.JobService
		}
	}
	return ""
}

// TestBeatFiresDueSchedule 到点触发：窗口内的整点触发一次，job 服务创建
// （fleetly-cron- 前缀 + restart-condition=none + cron.run label）、行 started、
// cron.triggered 事件。
func TestBeatFiresDueSchedule(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	// now=12:00:05，窗口 (11:59:55, 12:00:05] 含 12:00:00。
	h.beat()
	rows := h.runs("task")
	if len(rows) != 1 || rows[0].Status != state.CronRunStarted {
		t.Fatalf("expected one started row, got %+v", rows)
	}
	if !strings.HasPrefix(rows[0].JobService, "fleetly-cron-cronapp-task-") {
		t.Fatalf("job service name = %q", rows[0].JobService)
	}
	if rows[0].ScheduledAt != time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("scheduled_at = %v, want 12:00:00Z", rows[0].ScheduledAt)
	}
	job, ok := h.sub.services[rows[0].JobService]
	if !ok {
		t.Fatalf("job service %q not created", rows[0].JobService)
	}
	if job.Labels[state.LabelCronRun] != rows[0].ID {
		t.Fatalf("cron.run label missing: %+v", job.Labels)
	}
	if got := h.eventNames()["cron.triggered"]; got != 1 {
		t.Fatalf("cron.triggered events = %d, want 1", got)
	}
	if !h.sub.nets["fleetly-cronapp-net"] {
		t.Fatal("per-app network was not ensured before job creation")
	}
}

// TestBeatSkipsOverlappingRun 重叠 skip：在途行存在时不创建 job、行记
// skipped(overlap)。
func TestBeatSkipsOverlappingRun(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.beat()
	first := h.runs("task")
	h.clk.Advance(time.Minute)
	h.beat() // 12:01:05：上一 run 仍在途（假底座无任务终态）→ skip
	rows := h.runs("task")
	if len(rows) != 2 {
		t.Fatalf("expected started+skipped rows, got %+v", rows)
	}
	if rows[0].Status != state.CronRunSkipped || rows[0].SkipReason != state.CronSkipOverlap {
		t.Fatalf("second row = %+v, want skipped(overlap)", rows[0])
	}
	if len(h.sub.created) != 1 {
		t.Fatalf("job creations = %v, want exactly one", h.sub.created)
	}
	_ = first
}

// TestBeatSkipsNodeUnavailable 节点前哨：绑定节点不可用 →
// skipped(node_unavailable)、不创建 job。
func TestBeatSkipsNodeUnavailable(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.mgr.preflight = fakePreflight{err: apperr.New("E_PLACEMENT_NODE_UNAVAILABLE", "bound node not ready")}
	h.beat()
	rows := h.runs("task")
	if len(rows) != 1 || rows[0].Status != state.CronRunSkipped || rows[0].SkipReason != state.CronSkipNodeUnavailable {
		t.Fatalf("expected skipped(node_unavailable), got %+v", rows)
	}
	if len(h.sub.created) != 0 {
		t.Fatalf("no job must be created on node_unavailable, got %v", h.sub.created)
	}
	if got := h.eventNames()["cron.skipped"]; got != 1 {
		t.Fatalf("cron.skipped events = %d, want 1", got)
	}
}

// TestBeatClosesSucceededAndFailed 完成检测：任务 complete → succeeded、
// failed → failed（错误单行化入行），终态拍删除 job 服务并发对应事件。
func TestBeatClosesSucceededAndFailed(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.beat()
	jobSvc := jobServiceOf(h.runs("task"))
	h.sub.tasks[jobSvc] = []engine.TaskState{{ID: "t1", State: "complete"}}
	h.beat()
	rows := h.runs("task")
	if rows[0].Status != state.CronRunSucceeded {
		t.Fatalf("status = %s, want succeeded", rows[0].Status)
	}
	if rows[0].FinishedAt.IsZero() {
		t.Fatal("finished_at not stamped")
	}
	for _, s := range h.sub.removed {
		if s == jobSvc {
			goto removed
		}
	}
	t.Fatalf("job service %q not removed", jobSvc)
removed:
	if got := h.eventNames()["cron.succeeded"]; got != 1 {
		t.Fatalf("cron.succeeded events = %d, want 1", got)
	}

	// 失败路径：新触发一个 run，任务 failed（Err 含换行 → 单行化）。
	h.clk.Advance(time.Minute)
	h.beat()
	jobSvc2 := jobServiceOf(h.runs("task"))
	h.sub.tasks[jobSvc2] = []engine.TaskState{{ID: "t2", State: "failed", Err: "exit code 1\nfrom container"}}
	h.beat()
	rows = h.runs("task")
	if rows[0].Status != state.CronRunFailed {
		t.Fatalf("status = %s, want failed", rows[0].Status)
	}
	if rows[0].Error != "exit code 1 from container" {
		t.Fatalf("error = %q, want single-lined", rows[0].Error)
	}
	if got := h.eventNames()["cron.failed"]; got != 1 {
		t.Fatalf("cron.failed events = %d, want 1", got)
	}
}

// TestBeatTimesOutRun 看门狗（fake 时钟）：任务一直 running、started_at 超
// 预算 → 删 job 服务 + 行 timeout + cron.timed_out（FZ-4）。
func TestBeatTimesOutRun(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.beat()
	jobSvc := jobServiceOf(h.runs("task"))
	h.clk.Advance(DefaultJobTimeout + 5*time.Second)
	h.beat()
	rows := h.runs("task")
	if rows[0].Status != state.CronRunTimeout {
		t.Fatalf("status = %s, want timeout", rows[0].Status)
	}
	if _, ok := h.sub.services[jobSvc]; ok {
		t.Fatal("timed-out job service not removed")
	}
	if got := h.eventNames()["cron.timed_out"]; got != 1 {
		t.Fatalf("cron.timed_out events = %d, want 1", got)
	}
}

// TestNoCatchUpPastPoints 错过点不补跑：每拍以 now 为锚取下一触发点——
// 上一拍窗口之外的过去点丢弃、绝不回灌（第二拍不再补 12:01:00 之外的历史点）。
func TestNoCatchUpPastPoints(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	// 把时钟拨到 12:47:05（47 个过去点），只应触发 12:47:00 一个点。
	h.clk.Advance(47 * time.Minute)
	h.beat()
	rows := h.runs("task")
	started := 0
	for _, r := range rows {
		if r.Status == state.CronRunStarted {
			started++
			if r.ScheduledAt.UTC() != time.Date(2026, 9, 21, 12, 47, 0, 0, time.UTC) {
				t.Fatalf("fired point = %v, want 12:47:00Z (the only point in the current scan window)", r.ScheduledAt)
			}
		}
	}
	if started != 1 {
		t.Fatalf("started runs = %d, want exactly 1 (no catch-up)", started)
	}
}

// TestMissedDuringDowntimeDisclosedOnce 启动首拍披露停机错过点（每缺口一
// 行 skipped(missed_downtime)，scheduled_at = 首个错过点），后续拍不重复。
func TestMissedDuringDowntimeDisclosedOnce(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	// 预置上一 handled 点 = 12:00:00（历史台账），现在 12:05:05——12:01~12:05
	// 共 5 个点在停机期间错过。
	h.clk.now = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	h.beat() // 触发 12:00:00
	startedRow := jobServiceOf(h.runs("task"))
	if startedRow == "" {
		t.Fatal("setup: baseline trigger missing")
	}
	// 模拟停机：时钟直接跳 5 分钟（控制面重启，内存态清零 = 新 Manager）。
	h.clk.now = time.Date(2026, 9, 21, 12, 5, 5, 0, time.UTC)
	h.mgr = NewManager(Config{}, h.store, h.box, h.sub, fakePreflight{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(h.clk)
	h.beat()
	skips := 0
	var firstMissed time.Time
	for _, r := range h.runs("task") {
		if r.Status == state.CronRunSkipped && r.SkipReason == state.CronSkipMissedDowntime {
			skips++
			if firstMissed.IsZero() || r.ScheduledAt.Before(firstMissed) {
				firstMissed = r.ScheduledAt
			}
		}
	}
	if skips != 1 {
		t.Fatalf("missed_downtime rows = %d, want 1 (one row per gap)", skips)
	}
	if want := time.Date(2026, 9, 21, 12, 1, 0, 0, time.UTC); !firstMissed.Equal(want) {
		t.Fatalf("first missed point = %v, want %v", firstMissed, want)
	}
	// 第二拍：不再重复披露。
	h.beat()
	for _, r := range h.runs("task") {
		if r.Status == state.CronRunSkipped && r.SkipReason == state.CronSkipMissedDowntime {
			continue
		}
	}
	if count := h.countSkips(state.CronSkipMissedDowntime); count != 1 {
		t.Fatalf("missed_downtime rows after second beat = %d, want 1", count)
	}
}

// TestSamePointDeduplicatedAcrossBeats 同点去重：上一 run 在同一点位窗口内
// 收口后，同窗口的二次触发判定不得重复触发同一 cron 点（无新行、无新 job）。
func TestSamePointDeduplicatedAcrossBeats(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.beat() // 12:00:00 点触发
	jobSvc := jobServiceOf(h.runs("task"))
	h.sub.tasks[jobSvc] = []engine.TaskState{{ID: "t1", State: "complete"}}
	h.beat() // 同窗口（still 12:00:05）：上一拍收口 + 二次触发判定 → 去重
	rows := h.runs("task")
	if len(rows) != 1 || rows[0].Status != state.CronRunSucceeded {
		t.Fatalf("rows = %+v, want exactly the succeeded run", rows)
	}
	if len(h.sub.created) != 1 {
		t.Fatalf("job creations = %v, want one (same point must not refire)", h.sub.created)
	}
}

func (h *harness) countSkips(reason string) int {
	n := 0
	for _, r := range h.runs("task") {
		if r.Status == state.CronRunSkipped && r.SkipReason == reason {
			n++
		}
	}
	return n
}

// TestStartupReconcilesInterruptedRuns 启动残留收口：重启后在途行的 job
// 服务已按任务实况终态（complete）→ 按实况收口 succeeded（行+事件），不误
// 记 interrupted。
func TestStartupReconcilesInterruptedRuns(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	h.beat()
	jobSvc := jobServiceOf(h.runs("task"))
	// 假底座里任务已完成（重启期间任务跑完），行仍在途。时钟只推 5s（窗
	// 口 (12:00:00, 12:00:10] 不含新的 cron 点——隔离「收口」与「新触发」）。
	h.sub.tasks[jobSvc] = []engine.TaskState{{ID: "t1", State: "complete"}}
	h.clk.Advance(5 * time.Second)
	h.mgr = NewManager(Config{}, h.store, h.box, h.sub, fakePreflight{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(h.clk)
	h.beat()
	rows := h.runs("task")
	if rows[0].Status != state.CronRunSucceeded {
		t.Fatalf("status = %s, want succeeded (task-truth reconciliation)", rows[0].Status)
	}
	if got := h.eventNames()["cron.succeeded"]; got != 1 {
		t.Fatalf("cron.succeeded events = %d, want 1", got)
	}
}

// TestStartupSweepsOrphanJobServices 孤儿 job 服务清扫：服务在、台账无在
// 途行（行写失败/进程崩溃半程）→ 删服务。
func TestStartupSweepsOrphanJobServices(t *testing.T) {
	h := newHarness(t)
	h.sub.services["fleetly-cron-cronapp-task-deadbeef"] = engine.ServiceState{
		Name:   "fleetly-cron-cronapp-task-deadbeef",
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}
	h.arm("* * * * *", "task")
	h.beat()
	if _, ok := h.sub.services["fleetly-cron-cronapp-task-deadbeef"]; ok {
		t.Fatal("orphan job service not swept")
	}
}

// TestManualTriggerSamePathAndAudit 手动触发：与到点触发同路径（started 行
// + cron.triggered source=manual + job 服务），审计 cron.manual_triggered；
// 非 cron 服务 → ErrNoSchedule；重叠时返回 skipped 行。
func TestManualTriggerSamePathAndAudit(t *testing.T) {
	h := newHarness(t)
	h.arm("* * * * *", "task")
	run, err := h.mgr.TriggerRun(context.Background(), h.app.ID, h.app.Name, "task", "tok01")
	if err != nil {
		t.Fatalf("TriggerRun: %v", err)
	}
	if run.Status != state.CronRunStarted || run.JobService == "" {
		t.Fatalf("manual run = %+v", run)
	}
	audits, err := h.store.RecentAudits(context.Background(), 10)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "cron.manual_triggered" {
			found = true
		}
	}
	if !found {
		t.Fatal("cron.manual_triggered audit missing")
	}
	// 重叠：第二次手动触发返回 skipped(overlap) 行而非错误。
	run2, err := h.mgr.TriggerRun(context.Background(), h.app.ID, h.app.Name, "task", "tok01")
	if err != nil {
		t.Fatalf("second TriggerRun: %v", err)
	}
	if run2.Status != state.CronRunSkipped || run2.SkipReason != state.CronSkipOverlap {
		t.Fatalf("second manual run = %+v, want skipped(overlap)", run2)
	}
	if len(h.sub.created) != 1 {
		t.Fatalf("job creations = %v, want one", h.sub.created)
	}
	// 非 cron 服务 → ErrNoSchedule。
	if _, err := h.mgr.TriggerRun(context.Background(), h.app.ID, h.app.Name, "web", ""); !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("err = %v, want ErrNoSchedule", err)
	}
}

// TestTriggerRunUnknownAppIsAssembleGap assembleSchedules 收窄到不存在的
// app 时返回空集 → TriggerRun 报 ErrNoSchedule（不 panic、不跨 app 触发）。
func TestTriggerRunUnknownAppIsAssembleGap(t *testing.T) {
	h := newHarness(t)
	if _, err := h.mgr.TriggerRun(context.Background(), "nonsense", "nonsense", "task", ""); !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("err = %v, want ErrNoSchedule", err)
	}
}
