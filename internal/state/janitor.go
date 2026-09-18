package state

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// 保留期清理 job（state-model §2.2/§2.9 + 架构 §2.3 数据保留默认值）：
// 事件 30 天、审计 1 年（均可经 config 调整）。最小实现 = 独立守护
// goroutine 周期执行（定时器编排随 T2.22 统一调度核决策前保持最简）。
// 清理只删数据、不改 seq 语义——被清理区段的游标查询显式返回
// E_EVENT_CURSOR_EXPIRED（410），不静默跳号。

// JanitorCleanupInterval 是清理扫描周期。
const JanitorCleanupInterval = time.Hour

// DefaultEventRetentionDays / DefaultAuditRetentionDays 是保留期默认值
// （架构 §2.3：事件 30 天、审计 1 年；config 可调）。
const (
	DefaultEventRetentionDays = 30
	DefaultAuditRetentionDays = 365
)

// Janitor 周期清理过期事件与审计记录。
type Janitor struct {
	store *Store
	log   *slog.Logger

	eventRetention time.Duration
	auditRetention time.Duration

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewJanitor 构造保留期清理守护。retention 取配置天数（非正值回落
// 默认值——保留期是契约默认，不允许被误配成 0 而静默关闭）。
func NewJanitor(store *Store, eventRetentionDays, auditRetentionDays int, log *slog.Logger) *Janitor {
	done := make(chan struct{})
	close(done)
	return &Janitor{
		store:          store,
		log:            log,
		eventRetention: retentionOrDefault(eventRetentionDays, DefaultEventRetentionDays),
		auditRetention: retentionOrDefault(auditRetentionDays, DefaultAuditRetentionDays),
		stop:           make(chan struct{}),
		done:           done,
	}
}

func retentionOrDefault(days, def int) time.Duration {
	if days <= 0 {
		days = def
	}
	return time.Duration(days) * 24 * time.Hour
}

// Start 非阻塞启动清理循环（启动先清一拍，此后每小时一拍）。
func (j *Janitor) Start(ctx context.Context) error {
	j.done = make(chan struct{})
	go j.loop(ctx)
	return nil
}

// Stop 停止清理循环并等待退出。
func (j *Janitor) Stop(_ context.Context) error {
	j.stopOnce.Do(func() { close(j.stop) })
	<-j.done
	return nil
}

// EventRetention / AuditRetention 返回生效保留期（诊断用）。
func (j *Janitor) EventRetention() time.Duration { return j.eventRetention }
func (j *Janitor) AuditRetention() time.Duration { return j.auditRetention }

// PruneOnce 执行一轮清理（now 为基准时刻），返回 (事件条数, 审计条数)。
// 独立导出供测试直接驱动。
func (j *Janitor) PruneOnce(ctx context.Context, now time.Time) (events int64, audits int64, err error) {
	events, err = j.store.PruneExpiredEvents(ctx, now.Add(-j.eventRetention))
	if err != nil {
		return 0, 0, err
	}
	audits, err = j.store.PruneExpiredAudits(ctx, now.Add(-j.auditRetention))
	if err != nil {
		return events, 0, err
	}
	return events, audits, nil
}

func (j *Janitor) loop(ctx context.Context) {
	defer close(j.done)
	if ev, au, err := j.PruneOnce(ctx, time.Now().UTC()); err != nil {
		j.log.Error("retention prune failed", "error", err)
	} else if ev > 0 || au > 0 {
		j.log.Info("retention prune completed", "events_pruned", ev, "audits_pruned", au)
	}
	ticker := time.NewTicker(JanitorCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-j.stop:
			return
		case <-ticker.C:
			ev, au, err := j.PruneOnce(ctx, time.Now().UTC())
			if err != nil {
				j.log.Error("retention prune failed", "error", err)
				continue
			}
			if ev > 0 || au > 0 {
				j.log.Info("retention prune completed", "events_pruned", ev, "audits_pruned", au)
			}
		}
	}
}
