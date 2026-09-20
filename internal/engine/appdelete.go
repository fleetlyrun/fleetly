package engine

// app 删除生命周期第二拍的执行者（H10/MG-3，B6 横切结构修复）。
// 背景：api DeleteApp 只落 tombstone 第一拍（active → deleting + 路由撤销），
// 第二拍（deleting → deleted）此前无执行者——MarkAppDeleted 零生产调用方，
// ServiceRemove 唯一调用点是发布对账的「省略=删除」。删除后无新部署 →
// Swarm 服务永久运行、名字不释放，且 ListActiveApps 过滤后日志采集/漂移
// 监控对残留服务失明。裁决：收敛 duty 放引擎——它已持有 store + substrate，
// 关停底座（受管服务移除）本就是期望态收敛的一部分。

import (
	"context"
	"errors"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// appDeleteScanInterval 是 deleting 回收扫描的频控间隔（H10/MG-3）：删除
// 收敛不是热路径（API 第一拍后的异步第二拍），挂 tick 但按时间闸降频——
// 不占每 tick 的底座预算，10s 粒度兼顾「删除延迟可感知」；扫描幂等（服务
// 移除幂等 + 生命周期 CAS），重复执行只做确定性收敛。
const appDeleteScanInterval = 10 * time.Second

// ReapDeletingApps 单步执行 deleting 应用回收扫描（测试与诊断显式入口——
// 直通频控闸；生产由 tick 周期驱动）。
func (e *Engine) ReapDeletingApps(ctx context.Context) { e.reapDeletingApps(ctx, true) }

// reapDeletingApps 是 tick 的 deleting 回收 duty（H10/MG-3）：扫描全部
// deleting 应用并逐个收敛（受管服务移除 → deleting → deleted + 终局事件
// 与审计）。频控：非 force 形态受 deleteScanNextAt 时间闸（tick goroutine
// 专用字段，与 recoveryNextAt 同模式）。
func (e *Engine) reapDeletingApps(ctx context.Context, force bool) {
	now := e.now()
	if !force && now.Before(e.deleteScanNextAt) {
		return
	}
	e.deleteScanNextAt = now.Add(appDeleteScanInterval)
	apps, err := e.store.ListAppsByLifecycle(ctx, state.LifecycleDeleting)
	if err != nil {
		e.log.Warn("engine: list deleting apps", "error", err)
		return
	}
	for _, app := range apps {
		e.reapDeletingApp(ctx, app)
	}
}

// reapDeletingApp 收敛单个 deleting 应用：在途部署让位（发布对账会重建
// 服务，与删除互为反作用）→ 受管服务逐个移除 → 全部移除后 tombstone
// 第二拍（deleting → deleted）与终局事件/审计同事务落定。任何底座/库的
// 瞬态错误都保持 deleting 不落终态——下一拍重扫重试（服务移除幂等：
// NotFound 视成功）。
func (e *Engine) reapDeletingApp(ctx context.Context, app state.App) {
	// 在途部署检查：applyDesired 是期望态收敛，会持续重建被删服务——
	// 等部署终态后再收（下一拍重扫；部署终态后不会再有平台写路径）。
	if has, err := e.store.AppHasNonTerminalDeployment(ctx, app.ID); err != nil {
		e.log.Warn("engine: deleting-app in-flight check", "app", app.Name, "error", err)
		return
	} else if has {
		return
	}
	// 受管服务发现（与 applyDesired/漂移检测同一 label 约定：managed + app 名）。
	existing, err := e.sub.ServiceList(ctx, map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     app.Name,
	})
	if err != nil {
		// 底座瞬态（不可达/超时）：duty 内消化，不落 app 终态——下拍重试。
		e.log.Warn("engine: deleting-app service scan", "app", app.Name, "error", err)
		return
	}
	for _, s := range existing {
		if err := e.sub.ServiceRemove(ctx, s.Name); err != nil {
			// 单服务移除失败：保持 deleting 返回，下一拍对剩余服务重试。
			e.log.Warn("engine: deleting-app service remove", "app", app.Name,
				"service", s.Name, "error", err)
			return
		}
	}
	// 全部受管服务已移除 → tombstone 第二拍 + 终局事件（app.deleted，注册
	// 表词）与审计同事务（fail-closed；CAS 失败 = 并发已推进，幂等跳过）。
	err = e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.MarkAppDeleted(ctx, app.ID); err != nil {
			return err
		}
		if err := appEvent(ctx, tx, "app.deleted", app.Name); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "app.deleted",
			Target:      "app:" + app.Name,
			Result:      "ok",
			DiffSummary: state.DiffSummary("app", app.Name, "lifecycle", "deleted", "services", len(existing)),
		})
	})
	if err != nil {
		if errors.Is(err, state.ErrInvalidLifecycleTransition) {
			return // 并发推进（重复扫描/人工）：终态已落，幂等收敛
		}
		e.log.Warn("engine: mark app deleted", "app", app.Name, "error", err)
		return
	}
	e.log.Info("engine: app deleted (tombstone second beat)", "app", app.Name,
		"services_removed", len(existing))
}
