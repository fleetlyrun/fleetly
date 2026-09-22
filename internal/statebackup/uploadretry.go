package statebackup

// 上传失败当日退避重试（W3-F3 改进票，v0.2.x）：docker 重启收敛窗内 daily
// 备份上传撞 rustfs 未就绪（DNS no such host）→ backup.upload_failed 红
// 一次，次日下一份备份才绿。本文件给上传轨补「失败当日短退避重试」：
// 首传失败入重试队列，按 5m/15m/1h 退避至多重试 3 次；任一次成功即 ok +
// backup.upload_recovered（既有事件，红→绿闭环当日完成）；重试耗尽行终
// failed（下一份备份自然再开新轨）。
//
// 诚实面（事件流不刷屏）：backup.upload_failed 只在**首传失败**发一次；
// 重试中间失败只 Debug 日志 + 台账 upload_error 刷新（行保持 failed，组
// 件健康红口径不变），终态（成功/耗尽）才落事件面或静默保持。
//
// 状态载体：进程内 map（W3-F3 改进票的最小实现裁决——不建新表、不扩
// state_backups 列）。重启即清零的边界如实接受：重启后失败行保持 failed
// 可见，下一份备份照常开轨（重启本身常伴随 rustfs 重新收敛，重试价值低）。
// 「当日」边界由退避总时长天然满足（5m+15m+1h ≈ 80min ≪ 一天），无需显
// 式日历判断。

import (
	"context"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

const (
	// uploadRetryAttempts 是首传失败后的最大重试次数（不含首传）。
	uploadRetryAttempts = 3
	// uploadRetryScan 是重试队列的巡检周期（守护循环内的第二拍；粒度
	// 只需支撑分钟级退避，30s 足够且空转成本可忽略）。
	uploadRetryScan = 30 * time.Second
)

// uploadRetryBackoff 是重试退避表：backoff[i] = 已失败 i+1 次后距下一次
// 重试的等待（5m → 15m → 1h）。索引上界 = uploadRetryAttempts-1（第 3 次
// 重试再失败即耗尽，不再排程）。
var uploadRetryBackoff = [uploadRetryAttempts]time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
}

// uploadRetryEntry 是一个待重试的上传（台账行快照 + 退避状态机）。
type uploadRetryEntry struct {
	rec state.StateBackup
	// attempts 是已执行的重试次数（0 = 尚未重试过）。
	attempts int
	// nextAt 是下一次重试的最早时刻（m.now() 时钟）。
	nextAt time.Time
}

// now 是时间源（测试注入点 nowFn；生产 = time.Now）。
func (m *Manager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// enqueueUploadRetry 把上传失败的台账行排入重试队列（runOnce 的首传失败
// 路径；同一 id 重复入队不重置既有退避状态——防御性幂等）。
func (m *Manager) enqueueUploadRetry(rec state.StateBackup) {
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	if m.retries == nil {
		m.retries = make(map[string]uploadRetryEntry)
	}
	if _, dup := m.retries[rec.ID]; dup {
		return
	}
	m.retries[rec.ID] = uploadRetryEntry{
		rec:      rec,
		nextAt:   m.now().Add(uploadRetryBackoff[0]),
		attempts: 0,
	}
	m.log.Debug("backup: upload retry scheduled",
		"id", rec.ID, "attempts_left", uploadRetryAttempts,
		"next_backoff", uploadRetryBackoff[0].String())
}

// dueUploadRetries 取出到期条目并出队（到期判据 nextAt ≤ now；调用方对每
// 条执行一次重试——执行在 goroutine 内，出队先行避免执行期重复扫描）。
func (m *Manager) dueUploadRetries() []uploadRetryEntry {
	now := m.now()
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	var due []uploadRetryEntry
	for id, e := range m.retries {
		if !e.nextAt.After(now) {
			due = append(due, e)
			delete(m.retries, id)
		}
	}
	return due
}

// runUploadRetries 是重试队列的巡检拍（守护循环 uploadRetryScan 周期调
// 用）：到期条目逐条起 goroutine 执行（inflight 计数——Stop 等待在途重试
// 收口，与在途 post-deploy 备份同纪律）。
func (m *Manager) runUploadRetries() {
	for _, e := range m.dueUploadRetries() {
		m.inflight.Add(1)
		go func(entry uploadRetryEntry) {
			defer m.inflight.Done()
			m.retryUploadOnce(entry)
		}(e)
	}
}

// retryUploadOnce 执行一次重试：与备份触发同一 mu 串行（restic 同仓执行
// 不与 Trigger 路径并发），复用上传轨全管线（惰性 init 幂等——W3-F1 同族
// 豁免在管线内）。结论分派：成功 = 终态；仍失败 = 退避表内再排程 / 耗尽
// 即终态（Debug 收口，事件面静默）。终态一律清队（生产路径执行前已出队
// ——此处是对直接调用的防御性清理，保证状态机自洽）。
func (m *Manager) retryUploadOnce(entry uploadRetryEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	m.mu.Lock()
	row := m.uploadSnapshot(ctx, entry.rec, true)
	m.mu.Unlock()
	m.retryMu.Lock()
	delete(m.retries, entry.rec.ID)
	m.retryMu.Unlock()
	if row.UploadStatus != state.BackupUploadFailed {
		return // ok（或 none——目标被拆除的合法态）：重试闭环
	}
	attempts := entry.attempts + 1
	if attempts >= uploadRetryAttempts {
		m.log.Debug("backup: upload retry budget exhausted (row stays failed; next backup opens a new track)",
			"id", entry.rec.ID, "attempts", attempts)
		return
	}
	backoff := uploadRetryBackoff[attempts]
	m.retryMu.Lock()
	if m.retries == nil {
		m.retries = make(map[string]uploadRetryEntry)
	}
	// 同 id 在执行期不会再入队（首传入队只发生在 runOnce 的新行路径），
	// 直接写回即可。
	m.retries[entry.rec.ID] = uploadRetryEntry{
		rec:      entry.rec,
		attempts: attempts,
		nextAt:   m.now().Add(backoff),
	}
	m.retryMu.Unlock()
	m.log.Debug("backup: upload retry failed (backing off)",
		"id", entry.rec.ID, "attempt", attempts, "next_backoff", backoff.String())
}
