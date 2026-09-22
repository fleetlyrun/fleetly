package statebackup

// 上传失败当日退避重试的状态机单测（W3-F3 改进票）：首传失败入队与退避
// 时刻、未到期不重试、重试失败按 5m/15m/1h 再排程且事件面静默、重试成功
// recovered 当日闭环、重试耗尽终态 failed。时间源经 nowFn 注入——测试零
// 等待、退避时刻逐点断言。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// retryTestManager 是带注入时钟的上传测试 Manager（首传即失败的场景装配）。
func retryTestManager(t *testing.T, fr *fakeRestic, now time.Time) (*Manager, *state.Store) {
	t.Helper()
	mgr, st := newUploadTestManager(t, fr)
	mgr.nowFn = func() time.Time { return now }
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)
	return mgr, st
}

// uploadEventCount 统计指定上传轨事件的次数（事件流不刷屏的断言材料）。
func uploadEventCount(t *testing.T, st *state.Store, name string) int {
	t.Helper()
	events, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.Name == name {
			n++
		}
	}
	return n
}

// queuedEntry 取某行的重试队列条目（在队 = 第二返回值 true）。
func queuedEntry(mgr *Manager, id string) (uploadRetryEntry, bool) {
	mgr.retryMu.Lock()
	defer mgr.retryMu.Unlock()
	e, ok := mgr.retries[id]
	return e, ok
}

// TestUploadRetryBackoffStateMachine 退避状态机全轨：首传失败（事件 1 次）
// → 5m 未到期零重试 → 5m 重试失败（无新事件，+15m 再排程）→ 15m 重试失败
// （无新事件，+1h 再排程）→ 1h 重试仍失败 → 耗尽终态（队列清、行 failed、
// 事件仍 1 次）。重试执行经 retryUploadOnce 同步驱动——状态机逐点确定性。
func TestUploadRetryBackoffStateMachine(t *testing.T) {
	base := time.Now()
	fr := &fakeRestic{backupErr: errors.New("injected outage (rustfs not ready)")}
	mgr, st := retryTestManager(t, fr, base)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("fresh upload_status = %s, want failed", rec.UploadStatus)
	}
	// 首传失败：事件恰 1 次 + 入队（attempts=0，nextAt=+5m）。
	if n := uploadEventCount(t, st, "backup.upload_failed"); n != 1 {
		t.Fatalf("backup.upload_failed events = %d, want 1 (fresh failure only)", n)
	}
	entry, ok := queuedEntry(mgr, rec.ID)
	if !ok {
		t.Fatal("failed upload not enqueued for retry")
	}
	if entry.attempts != 0 || !entry.nextAt.Equal(base.Add(uploadRetryBackoff[0])) {
		t.Fatalf("queued entry = attempts:%d nextAt:%v, want attempts:0 nextAt:+5m", entry.attempts, entry.nextAt)
	}

	// 未到期（+4m59s）：巡检零出队、零 runner 调用增量。
	mgr.nowFn = func() time.Time { return base.Add(uploadRetryBackoff[0] - time.Second) }
	if due := mgr.dueUploadRetries(); len(due) != 0 {
		t.Fatalf("due entries before backoff = %d, want 0", len(due))
	}

	// 重试 1（+5m）：仍失败 → 无新事件 + attempts=1、nextAt=+15m。
	mgr.nowFn = func() time.Time { return base.Add(uploadRetryBackoff[0]) }
	if due := mgr.dueUploadRetries(); len(due) != 1 {
		t.Fatalf("due entries at +5m = %d, want 1", len(due))
	}
	mgr.retryUploadOnce(entry)
	if n := uploadEventCount(t, st, "backup.upload_failed"); n != 1 {
		t.Fatalf("backup.upload_failed events after retry 1 = %d, want 1 (retries stay silent)", n)
	}
	row, err := st.GetStateBackup(context.Background(), rec.ID)
	if err != nil || row.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("row after retry 1 = %+v (err %v), want failed", row, err)
	}
	if entry, ok = queuedEntry(mgr, rec.ID); !ok || entry.attempts != 1 ||
		!entry.nextAt.Equal(base.Add(uploadRetryBackoff[0]).Add(uploadRetryBackoff[1])) {
		t.Fatalf("queued after retry 1 = ok:%v attempts:%d nextAt:%v, want attempts:1 nextAt:+5m+15m", ok, entry.attempts, entry.nextAt)
	}

	// 重试 2（+5m+15m）：仍失败 → 无新事件 + attempts=2、nextAt=+1h。
	mgr.nowFn = func() time.Time { return entry.nextAt }
	if due := mgr.dueUploadRetries(); len(due) != 1 {
		t.Fatalf("due entries at +20m = %d, want 1", len(due))
	}
	mgr.retryUploadOnce(entry)
	if n := uploadEventCount(t, st, "backup.upload_failed"); n != 1 {
		t.Fatalf("backup.upload_failed events after retry 2 = %d, want 1", n)
	}
	if entry, ok = queuedEntry(mgr, rec.ID); !ok || entry.attempts != 2 ||
		!entry.nextAt.Equal(base.Add(uploadRetryBackoff[0]).Add(uploadRetryBackoff[1]).Add(uploadRetryBackoff[2])) {
		t.Fatalf("queued after retry 2 = ok:%v attempts:%d nextAt:%v, want attempts:2 nextAt:+5m+15m+1h", ok, entry.attempts, entry.nextAt)
	}

	// 重试 3（+5m+15m+1h）：仍失败 → 耗尽终态：队列清空、行 failed、事件
	// 仍 1 次（下一份备份开新轨）。
	mgr.nowFn = func() time.Time { return entry.nextAt }
	mgr.retryUploadOnce(entry)
	if n := uploadEventCount(t, st, "backup.upload_failed"); n != 1 {
		t.Fatalf("backup.upload_failed events after exhaustion = %d, want 1", n)
	}
	if _, ok := queuedEntry(mgr, rec.ID); ok {
		t.Fatal("retry entry still queued after budget exhausted, want dropped")
	}
	row, err = st.GetStateBackup(context.Background(), rec.ID)
	if err != nil || row.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("row after exhaustion = %+v (err %v), want terminal failed", row, err)
	}
	if herr := mgr.CheckHealth(); herr == nil {
		t.Fatal("CheckHealth must stay red while the exhausted row is the latest")
	}
	// 全程重试执行次数 = init+backup 各 4 次（首传 + 3 重试；每轮先 init
	// 后 backup，snapshots 在 backup 失败后不达）。
	backupCalls := 0
	for _, c := range fr.calls {
		if resticCommand(c.Args) == "backup" {
			backupCalls++
		}
	}
	if backupCalls != 1+uploadRetryAttempts {
		t.Fatalf("restic backup calls = %d, want %d (fresh + 3 retries)", backupCalls, 1+uploadRetryAttempts)
	}
}

// TestUploadRetryRecoversSameDay 当日闭环：首传失败 → +5m 重试时底座已
// 愈 → 行 ok + backup.upload_recovered（前行自身 failed 的恢复绿）+ 组件
// 转绿 + 队列清空。W3-F3 场景的钉住形态。
func TestUploadRetryRecoversSameDay(t *testing.T) {
	base := time.Now()
	fr := &fakeRestic{backupErr: errors.New("dns no such host (rustfs converging)")}
	mgr, st := retryTestManager(t, fr, base)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	entry, ok := queuedEntry(mgr, rec.ID)
	if !ok {
		t.Fatal("failed upload not enqueued")
	}

	// 底座恢复：backup 成功 + 回读在列。
	fr.backupErr = nil
	fr.backupOutput = snapLine
	fr.snapshotsOutput = snapsJSON

	mgr.nowFn = func() time.Time { return base.Add(uploadRetryBackoff[0]) }
	mgr.retryUploadOnce(entry)

	row, err := st.GetStateBackup(context.Background(), rec.ID)
	if err != nil || row.UploadStatus != state.BackupUploadOK {
		t.Fatalf("row after successful retry = %+v (err %v), want ok", row, err)
	}
	if _, ok := queuedEntry(mgr, rec.ID); ok {
		t.Fatal("recovered row still queued, want dropped")
	}
	if err := mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth after recovered retry = %v, want green", err)
	}
	events, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	recovered := false
	for _, ev := range events {
		if ev.Name == "backup.upload_recovered" {
			recovered = true
			if !strings.Contains(ev.Payload, rec.ID) {
				t.Errorf("recovered payload %q missing backup id", ev.Payload)
			}
		}
	}
	if !recovered {
		t.Error("backup.upload_recovered event missing (same-day red→green closure)")
	}
	if n := uploadEventCount(t, st, "backup.upload_failed"); n != 1 {
		t.Errorf("backup.upload_failed events = %d, want 1", n)
	}
}

// TestUploadRetriesNotEnqueuedOnSuccessOrUnset 非失败路径零入队：上传 ok
// 与 unset 合法停摆都不得产生重试条目（负面测试）。
func TestUploadRetriesNotEnqueuedOnSuccessOrUnset(t *testing.T) {
	base := time.Now()
	// ok 路径。
	fr := &fakeRestic{backupOutput: snapLine, snapshotsOutput: snapsJSON}
	mgr, st := retryTestManager(t, fr, base)
	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger (ok path): %v", err)
	}
	if rec.UploadStatus != state.BackupUploadOK {
		t.Fatalf("upload_status = %s, want ok", rec.UploadStatus)
	}
	if _, ok := queuedEntry(mgr, rec.ID); ok {
		t.Fatal("successful upload must not be enqueued for retry")
	}

	// unset 路径（重开一套：mode=unset、无 runner 调用）。
	fr2 := &fakeRestic{}
	mgr2, st2 := newUploadTestManager(t, fr2)
	mgr2.nowFn = func() time.Time { return base }
	rec2, err := mgr2.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger (unset path): %v", err)
	}
	if rec2.UploadStatus != state.BackupUploadNone || len(fr2.calls) != 0 {
		t.Fatalf("unset path upload=%s calls=%d, want none/0", rec2.UploadStatus, len(fr2.calls))
	}
	if _, ok := queuedEntry(mgr2, rec2.ID); ok {
		t.Fatal("unset skip must not be enqueued for retry")
	}
	if n := uploadEventCount(t, st2, "backup.upload_failed"); n != 0 {
		t.Errorf("unset path emitted %d upload_failed events, want 0", n)
	}
	_ = st
}
