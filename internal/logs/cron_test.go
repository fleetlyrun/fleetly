package logs

// E5 Cron 整改（架构 §4.3 留存行「日志进现有采集」）：一次性 cron job 服务
// 日志进管线的映射解析（job 名 + 受管 label → (app, compose 服务)）与采集
// 路径（零点全量回读、归属合流、游标当轮回收、不重复）。
//
// v0.3 W2-S3：app 归属标识与 job 服务名都以三段限定形/三段公式消费
// （fleetly-cron-<team>-<prj>-<app>-<svc>-<ulid8>；label 值 team/prj/app）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// errFakeTransient 是发现面底座暂态注入错误。
var errFakeTransient = errors.New("substrate transient")

// cronLine 便捷构造一条底座日志行。
func cronLine(at time.Time, line string) substrate.LogLine {
	return substrate.LogLine{At: at, Line: line}
}

// jobState 构造一个 job 服务的实况投影（app label 值 = 三段限定形）。
func jobState(name, appLabel, process string) engine.ServiceState {
	return engine.ServiceState{
		Name:   name,
		Labels: map[string]string{state.LabelApp: appLabel, state.LabelProcess: process},
	}
}

// testJobName 构造确定性 job 服务名（固定 ULID 词形尾缀；三段公式——
// team/prj 段取播种 app 的 slug）。
func testJobName(t *testing.T, app state.App) string {
	t.Helper()
	name, err := naming.CronJobName(app.TeamSlug, app.ProjectSlug, app.Name, "task", "01JABCDEFGHJKMNPQRSTVWX")
	if err != nil {
		t.Fatalf("CronJobName: %v", err)
	}
	return name
}

// cronTestSetup 预置 active app + 长驻服务面（web；无 cron job）。
func cronTestSetup(t *testing.T) (*Manager, *fakePort, state.App) {
	t.Helper()
	mg, port, st, _ := newTestManager(t)
	app, _ := testsupport.SeedAppE(t, st, "cronapp")
	port.setApp(app.QualifiedName(), "web")
	return mg, port, app
}

// TestCronJobRefOfMapping 映射解析：合法投影 → 归属对；前缀不符 / app label
// 漂移 / process label 缺失 → 拒绝（label 是唯一权威——job 名字符串反解在
// app/service 含 '-' 时有歧义）。
func TestCronJobRefOfMapping(t *testing.T) {
	const appLabel = "acme/prod/cronapp"
	jobName, err := naming.CronJobName("acme", "prod", "cronapp", "task", "01JABCDEFGHJKMNPQRSTVWX")
	if err != nil {
		t.Fatalf("CronJobName: %v", err)
	}
	ref, ok := cronJobRefOf(appLabel, jobState(jobName, appLabel, "task"))
	if !ok {
		t.Fatal("valid job projection rejected")
	}
	if ref.JobService != jobName || ref.Service != "task" {
		t.Fatalf("ref = %+v", ref)
	}
	if _, ok := cronJobRefOf(appLabel, jobState("fleetly-cronapp-task", appLabel, "task")); ok {
		t.Fatal("long-running service name accepted as job")
	}
	if _, ok := cronJobRefOf("other/team/app", jobState(jobName, appLabel, "task")); ok {
		t.Fatal("app label drift accepted (mis-attribution)")
	}
	if _, ok := cronJobRefOf(appLabel, jobState(jobName, appLabel, "")); ok {
		t.Fatal("missing process label accepted")
	}
	if _, ok := cronJobRefOf(appLabel, engine.ServiceState{Name: jobName}); ok {
		t.Fatal("nil labels accepted")
	}
}

// TestCronJobLogsCollectedFromStart 采集路径：job 服务的输出行进入管线并
// 按 (app, compose 服务) 归属——行产生早于首拍发现仍可读到（零点全量回读，
// 长驻的「发现时刻起采」口径对一次性作业不适用），ring 与落盘同面合流。
func TestCronJobLogsCollectedFromStart(t *testing.T) {
	mg, port, app := cronTestSetup(t)
	ctx := context.Background()
	jobName := testJobName(t, app)
	// 行时间早于任何发现时刻（发现晚于输出的最坏形态）。
	at := time.Now().UTC().Add(-time.Hour)
	port.emit(jobName, cronLine(at, "cron-e2e-log-marker"))
	port.setCronJobs(app.QualifiedName(), jobState(jobName, app.QualifiedName(), "task"))

	mg.scanOnce(ctx)

	// ring 面（Follow 同源）：行归属 (cronapp 限定形, task)。cancel 先于
	// 排空——订阅 channel 由 cancel 关闭（回放已在容量充足的缓冲内，锁内
	// 投递不丢）。
	ch, cancel := mg.hub.subscribe(app.QualifiedName(), "task")
	cancel()
	var snap []Entry
	for e := range ch {
		snap = append(snap, e)
	}
	if len(snap) != 1 {
		t.Fatalf("ring entries = %d, want 1: %+v", len(snap), snap)
	}
	if snap[0].App != app.QualifiedName() || snap[0].Service != "task" || snap[0].Line != "cron-e2e-log-marker" {
		t.Fatalf("entry attribution wrong: %+v", snap[0])
	}
	if snap[0].Source != SourceContainer {
		t.Fatalf("source = %s, want container", snap[0].Source)
	}

	// 落盘面（History 同源）。
	rows, err := mg.History(ctx, HistoryQuery{App: app.Name, Service: "task"})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 1 || rows[0].Line != "cron-e2e-log-marker" {
		t.Fatalf("history rows = %+v", rows)
	}

	// 游标独立记账：job 游标按 job 服务名建立，与长驻 (app, service) 键隔离。
	mg.mu.Lock()
	_, hasJobCursor := mg.streams[cronCursorKey(app.QualifiedName(), jobName)]
	_, hasTaskLongRunningCursor := mg.streams[streamKey(app.Name, "task")]
	mg.mu.Unlock()
	if !hasJobCursor {
		t.Fatal("job cursor not recorded")
	}
	if hasTaskLongRunningCursor {
		t.Fatal("task long-running cursor exists although web is the only long-running service (job keys must not collide with long-running keys)")
	}
}

// TestCronJobLogsNoDupAndCursorReclaimed 采集路径续拍：job 存活期续拍不重
// 复入账（游标推进、增量只取新行）；job 从发现面消失（完成删除）后游标当
// 轮回收。
func TestCronJobLogsNoDupAndCursorReclaimed(t *testing.T) {
	mg, port, app := cronTestSetup(t)
	ctx := context.Background()
	jobName := testJobName(t, app)
	at := time.Now().UTC().Add(-time.Hour)
	port.emit(jobName, cronLine(at, "line-one"))
	port.setCronJobs(app.QualifiedName(), jobState(jobName, app.QualifiedName(), "task"))

	mg.scanOnce(ctx)
	// job 仍在途并产出了新行：续拍只增量（不重复 line-one）。
	port.emit(jobName, cronLine(at.Add(time.Second), "line-two"))
	mg.scanOnce(ctx)

	rows, err := mg.History(ctx, HistoryQuery{App: app.Name, Service: "task"})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows = %d, want exactly 2 (no duplication): %+v", len(rows), rows)
	}

	// job 完成删除（发现面清空）：游标当轮回收，无悬挂。
	port.setCronJobs(app.QualifiedName())
	mg.scanOnce(ctx)
	mg.mu.Lock()
	_, stillThere := mg.streams[cronCursorKey(app.QualifiedName(), jobName)]
	mg.mu.Unlock()
	if stillThere {
		t.Fatal("job cursor not reclaimed after the job service disappeared")
	}
}

// TestCronJobDiscoveryTransientKeepsCursor 发现面底座暂态：本轮跳过且游标
// 保留（不误回收仍存活 job 的游标），下一轮恢复后照常增量。
func TestCronJobDiscoveryTransientKeepsCursor(t *testing.T) {
	mg, port, app := cronTestSetup(t)
	ctx := context.Background()
	jobName := testJobName(t, app)
	base := time.Now().UTC().Add(-time.Hour)
	port.emit(jobName, cronLine(base, "line-one"))
	port.setCronJobs(app.QualifiedName(), jobState(jobName, app.QualifiedName(), "task"))
	mg.scanOnce(ctx)

	port.failCronDiscovery(errFakeTransient)
	mg.scanOnce(ctx) // 暂态轮：跳过，游标必须保留

	mg.mu.Lock()
	_, kept := mg.streams[cronCursorKey(app.QualifiedName(), jobName)]
	mg.mu.Unlock()
	if !kept {
		t.Fatal("transient discovery failure evicted a live job cursor")
	}

	// 恢复后续拍照常增量。
	port.failCronDiscovery(nil)
	port.emit(jobName, cronLine(base.Add(time.Second), "line-two"))
	mg.scanOnce(ctx)
	rows, err := mg.History(ctx, HistoryQuery{App: app.Name, Service: "task"})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows after recovery = %d, want 2: %+v", len(rows), rows)
	}
}
