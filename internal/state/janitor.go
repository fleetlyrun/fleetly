package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 保留期清理 job（state-model §2.2/§2.9 + 架构 §2.3 数据保留默认值）：
// 事件 30 天、审计 1 年（均可经 config 调整）。最小实现 = 独立守护
// goroutine 周期执行（定时器编排随 T2.22 统一调度核决策前保持最简）。
// 清理只删数据、不改 seq 语义——被清理区段的游标查询显式返回
// E_EVENT_CURSOR_EXPIRED（410），不静默跳号。
//
// S18-A7/A10 扩展：compose 持久化目录（<数据根>/deployments/<id>/）按
// 部署终态后 30 天窗清理；builds 终态行 90 天、build artifacts 目录 30 天
// 保留窗（均可经 config 调整）；非终态超龄扫描（deployments 超 2×（发布
// 看门狗+观察窗）预算、builds 非 queued/building 停留超 2×构建超时 → 事件
// engine.stale_nonterminal / build.stale_nonterminal + Error 日志——只告警
// 不自愈：正常恢复路径已有启动复位/看门狗兜底，这层是「未来新状态机漏洞」
// 的显性化）。

// JanitorCleanupInterval 是清理扫描周期。
const JanitorCleanupInterval = time.Hour

// DefaultEventRetentionDays / DefaultAuditRetentionDays 是保留期默认值
// （架构 §2.3：事件 30 天、审计 1 年；config 可调）。
const (
	DefaultEventRetentionDays = 30
	DefaultAuditRetentionDays = 365
)

// S18-A7/A10 新增保留窗默认值。
const (
	// DefaultBuildRetentionDays 是 builds 终态行保留天数（台账窗口）。
	DefaultBuildRetentionDays = 90
	// DefaultArtifactsRetentionDays 是 build artifacts 目录保留天数。
	DefaultArtifactsRetentionDays = 30
	// DefaultDeploymentDirRetentionDays 是部署 compose 持久化目录在部署
	// 终态后的保留天数（终态前行恒不清理）。
	DefaultDeploymentDirRetentionDays = 30
)

// JanitorConfig 是 janitor 的保留窗与预算参数集（S18-A7/A10 装配扩展：
// 构造参数从两个散装天数收口为结构体——新增 duties 的参数不再逐个膨胀
// 构造签名）。零值字段回落默认；目录/根为空时对应 duty 整体跳过。
type JanitorConfig struct {
	// EventRetentionDays / AuditRetentionDays 是事件/审计保留天数。
	EventRetentionDays int
	AuditRetentionDays int
	// BuildRetentionDays 是 builds 终态行保留天数（缺省 90）。
	BuildRetentionDays int
	// ArtifactsDir 是 build 产物归档根目录（build.artifacts_dir；空 = 跳过
	// 目录清理——进程内测试夹具形态）。
	ArtifactsDir string
	// ArtifactsRetentionDays 是产物目录保留天数（缺省 30）。
	ArtifactsRetentionDays int
	// DeploymentsRoot 是部署 compose 持久化根（<数据根>/deployments；
	// 空 = 跳过目录清理）。
	DeploymentsRoot string
	// DeploymentDirRetentionDays 是部署目录在终态后的保留天数（缺省 30）。
	DeploymentDirRetentionDays int
	// StaleDeploymentBudget 是部署非终态超龄判定预算（2×（DeployTimeout+
	// ObserveWindow），装配自 engine 配置；≤0 跳过部署扫描）。
	StaleDeploymentBudget time.Duration
	// StaleBuildBudget 是构建非终态超龄判定预算（2×构建超时，装配自
	// build 配置；≤0 跳过构建扫描）。
	StaleBuildBudget time.Duration
}

// Janitor 周期清理过期事件与审计记录（S18-A7/A10：外加部署目录/构建
// 台账/产物目录保留窗与非终态超龄扫描）。
type Janitor struct {
	store *Store
	log   *slog.Logger
	cfg   JanitorConfig

	eventRetention time.Duration
	auditRetention time.Duration

	// staleSeen 是非终态超龄告警的进程内记忆（每行告警一次；重启清零 =
	// 超龄存续时重报——漏报劣于重复，与引擎 driftSeen 同语义）。
	staleSeen map[string]bool

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewJanitor 构造保留期清理守护。cfg 天数取非正值时回落默认（保留期是
// 契约默认，不允许被误配成 0 而静默关闭）。
func NewJanitor(store *Store, cfg JanitorConfig, log *slog.Logger) *Janitor {
	done := make(chan struct{})
	close(done)
	return &Janitor{
		store: store,
		log:   log,
		cfg: JanitorConfig{
			EventRetentionDays:         retentionDaysOr(cfg.EventRetentionDays, DefaultEventRetentionDays),
			AuditRetentionDays:         retentionDaysOr(cfg.AuditRetentionDays, DefaultAuditRetentionDays),
			BuildRetentionDays:         retentionDaysOr(cfg.BuildRetentionDays, DefaultBuildRetentionDays),
			ArtifactsDir:               cfg.ArtifactsDir,
			ArtifactsRetentionDays:     retentionDaysOr(cfg.ArtifactsRetentionDays, DefaultArtifactsRetentionDays),
			DeploymentsRoot:            cfg.DeploymentsRoot,
			DeploymentDirRetentionDays: retentionDaysOr(cfg.DeploymentDirRetentionDays, DefaultDeploymentDirRetentionDays),
			StaleDeploymentBudget:      cfg.StaleDeploymentBudget,
			StaleBuildBudget:           cfg.StaleBuildBudget,
		},
		eventRetention: retentionOrDefault(cfg.EventRetentionDays, DefaultEventRetentionDays),
		auditRetention: retentionOrDefault(cfg.AuditRetentionDays, DefaultAuditRetentionDays),
		staleSeen:      map[string]bool{},
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

// retentionDaysOr 是 JanitorConfig 字段的缺省回落（天数形态，非时长）。
func retentionDaysOr(days, def int) int {
	if days <= 0 {
		return def
	}
	return days
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
// 独立导出供测试直接驱动。S18-A7/A10：一轮内顺次执行全部 duties——
// 事件/审计分批删除 → builds 终态行 → 产物目录 → 部署 compose 目录 →
// 非终态超龄扫描；各 duty 失败独立记日志不中断后续（单 duty 失败不该
// 瘫痪整轮保留期治理），事件/审计错误仍向上返回（既有契约）。
func (j *Janitor) PruneOnce(ctx context.Context, now time.Time) (events int64, audits int64, err error) {
	events, err = j.store.PruneExpiredEvents(ctx, now.Add(-j.eventRetention))
	if err != nil {
		return 0, 0, err
	}
	audits, err = j.store.PruneExpiredAudits(ctx, now.Add(-j.auditRetention))
	if err != nil {
		return events, 0, err
	}
	if n, err := j.store.PruneTerminalBuildsOlderThan(ctx,
		now.Add(-time.Duration(j.cfg.BuildRetentionDays)*24*time.Hour)); err != nil {
		j.log.Error("janitor: prune terminal builds failed", "error", err)
	} else if n > 0 {
		j.log.Info("janitor: pruned terminal builds", "builds_pruned", n)
	}
	j.pruneArtifacts(now)
	j.pruneDeploymentDirs(ctx, now)
	j.scanStaleNonTerminal(ctx, now)
	return events, audits, nil
}

// pruneArtifacts 清理 build artifacts 目录（S18-A10：plan JSON / 构建日志
// 归档按 mtime 30 天窗——顶层过期文件删除、子目录内全部条目过期才整目录
// 回收（归档目录是同次构建的产物组，任一新鲜条目即保留））。
func (j *Janitor) pruneArtifacts(now time.Time) {
	if j.cfg.ArtifactsDir == "" {
		return
	}
	cutoff := now.Add(-time.Duration(j.cfg.ArtifactsRetentionDays) * 24 * time.Hour)
	entries, err := os.ReadDir(j.cfg.ArtifactsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			j.log.Error("janitor: scan artifacts dir failed", "dir", j.cfg.ArtifactsDir, "error", err)
		}
		return
	}
	for _, e := range entries {
		path := filepath.Join(j.cfg.ArtifactsDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.IsDir() {
			j.pruneDirByMtime(path, cutoff)
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

// pruneDirByMtime 删除 mtime 全部早于 cutoff 的目录（artifacts 子目录形态；
// 任一新鲜条目存在即保留整目录——归档目录是同次构建的产物组）。
func (j *Janitor) pruneDirByMtime(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return // 有新鲜条目：整目录保留
		}
	}
	_ = os.RemoveAll(dir)
}

// pruneDeploymentDirs 清理部署 compose 持久化目录（S18-A7：
// <DeploymentsRoot>/<id>/ 按部署终态后 30 天窗；非终态行恒不清理——
// 引擎仍要重载复核；无部署行对应的孤儿目录按目录 mtime 同窗回收——
// 入队建行失败/历史残留的兜底）。
//
// MG-3（B6）资源台账兜底对账说明：PersistDeploymentCompose 先写目录后建
// 行（store.go——部署行不指向缺失文件），「行建失败/进程在两步间崩溃」
// 留下的目录即本函数的孤儿分支对象——目录存在 + GetDeployment 返回
// ErrDeploymentNotFound → 按 mtime 过窗回收（该形态理论已覆盖，
// janitor_test.go 的 d_orphan_old 用例钉死）；台账外资源不再静默累积。
func (j *Janitor) pruneDeploymentDirs(ctx context.Context, now time.Time) {
	if j.cfg.DeploymentsRoot == "" {
		return
	}
	entries, err := os.ReadDir(j.cfg.DeploymentsRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			j.log.Error("janitor: scan deployments root failed", "dir", j.cfg.DeploymentsRoot, "error", err)
		}
		return
	}
	cutoff := now.Add(-time.Duration(j.cfg.DeploymentDirRetentionDays) * 24 * time.Hour)
	removable, err := j.store.TerminalDeploymentIDsOlderThan(ctx, cutoff)
	if err != nil {
		j.log.Error("janitor: query terminal deployments failed", "error", err)
		return
	}
	for _, e := range entries {
		path := filepath.Join(j.cfg.DeploymentsRoot, e.Name())
		if removable[e.Name()] {
			_ = os.RemoveAll(path)
			continue
		}
		// 孤儿目录（无部署行 / 行非终态）：非终态行不清理；无行对应的
		// 目录按 mtime 同窗回收。M3-1：仅 ErrDeploymentNotFound 走孤儿
		// 回收分支——GetDeployment 的其他错误（库故障/ctx 取消等）对
		// 「该目录是否有部署行」不构成证据，误当孤儿会连带删除在役部署
		// 的 compose 持久化目录（引擎 preparing 重载即失败）；记 Warn
		// 跳过，下一轮扫描再判。
		if _, err := j.store.GetDeployment(ctx, e.Name()); err != nil {
			if !errors.Is(err, ErrDeploymentNotFound) {
				j.log.Warn("janitor: probe deployment failed, skip dir this round",
					"dir", path, "error", err.Error())
				continue
			}
			if info, ierr := e.Info(); ierr == nil && info.ModTime().Before(cutoff) {
				_ = os.RemoveAll(path)
			}
		}
	}
}

// scanStaleNonTerminal 非终态超龄扫描（S18-A10，§9 裁决并入 janitor）：
// deployments 停留非终态超 2×（DeployTimeout+ObserveWindow）、builds
// 停留 queued/building 超 2×构建超时 → 事件 + Error 日志。只告警不自愈
// （恢复路径已有 S8/S9 兜底；这层是未来新状态机漏洞的显性化）。基线取
// 行内最新阶段时间（phase_started_at / release_started_at /
// observe_started_at / created_at 最大值）——正常推进的行基线随阶段刷新，
// 恒不误报。
func (j *Janitor) scanStaleNonTerminal(ctx context.Context, now time.Time) {
	if j.cfg.StaleDeploymentBudget > 0 {
		rows, err := j.store.ListNonTerminalDeployments(ctx)
		if err != nil {
			j.log.Error("janitor: stale scan deployments failed", "error", err)
		} else {
			for _, rec := range rows {
				anchor := rec.CreatedAt
				for _, t := range []time.Time{rec.PhaseStartedAt, rec.ReleaseStartedAt, rec.ObserveStartedAt} {
					if t.After(anchor) {
						anchor = t
					}
				}
				if now.Sub(anchor) <= j.cfg.StaleDeploymentBudget {
					continue
				}
				j.reportStale(ctx, now, "engine.stale_nonterminal",
					"deployment:"+rec.ID,
					"deployment", rec.ID,
					"status", string(rec.Status),
					"age_seconds", fmt.Sprintf("%d", int64(now.Sub(anchor)/time.Second)))
			}
		}
	}
	if j.cfg.StaleBuildBudget > 0 {
		rows, err := j.store.ListNonTerminalBuilds(ctx)
		if err != nil {
			j.log.Error("janitor: stale scan builds failed", "error", err)
		} else {
			for _, rec := range rows {
				anchor := rec.CreatedAt
				if !rec.StartedAt.IsZero() && rec.StartedAt.After(anchor) {
					anchor = rec.StartedAt
				}
				if now.Sub(anchor) <= j.cfg.StaleBuildBudget {
					continue
				}
				j.reportStale(ctx, now, "build.stale_nonterminal",
					"build:"+rec.ID,
					"build", rec.ID,
					"status", string(rec.Status),
					"age_seconds", fmt.Sprintf("%d", int64(now.Sub(anchor)/time.Second)))
			}
		}
	}
}

// reportStale 落一条非终态超龄事件 + Error 日志（每行每 janitor 生命周期
// 一次；重启重报——漏报劣于重复）。
func (j *Janitor) reportStale(ctx context.Context, now time.Time, name, subject string, kv ...string) {
	if j.staleSeen[subject] {
		return
	}
	j.staleSeen[subject] = true
	j.log.Error("janitor: 非终态记录超龄停留（状态机漏洞显性化，不自愈）",
		"event", name, "subject", subject)
	// DiffSummary 构造器复用为事件 payload 的安全 JSON 化（B4 同源）。
	kvs := make([]any, 0, len(kv))
	for _, v := range kv {
		kvs = append(kvs, v)
	}
	p := DiffSummary(kvs...)
	_ = j.store.InTx(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent(ctx, Event{Name: name, Subject: subject, Payload: p, At: now})
		return err
	})
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
