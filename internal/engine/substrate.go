package engine

// 运行期 DB↔Swarm 存在性对账（T0-V2.2，调研 R2）：zane-ops 在每次部署
// 收尾做 cleanup_previous_unclean_deployments 对账；本平台推广为周期任务。
// 现状缺口：外部 docker service rm 后，平台 DB 仍声称 running，视图长期
// 说谎——只有控制面重启分类恢复会纠正，运行期无人对账。
//
// 定位与判据：
//   - 判据是 service 存在性（ServiceInspect），不是副本数——漂移 = 服务
//     存在但 spec 被改（drift.go 管）；本对账 = 服务整个不存在。
//     replicas=0 的 paused/保留现场应用 service 仍在，不误报；
//   - 候选集：派生态声称 running 的 active app（无在途部署——发布过程
//     本身就是期望态迁移，服务可能尚未创建），期望态 = 最新 succeeded
//     deployment 的 desired_spec 快照（与漂移检测同源，lastSucceededSpecs）；
//   - 处置只披露与修正视图：发 app.substrate_missing 事件 + 派生态
//     running → down（诚实修正：服务整体缺失 = 没有任何期望实例），
//     不做自动重建/重部署/删除——重建是部署链路（用户/恢复器）的职责；
//   - 节流与幂等：持续缺失只报一次（substrateMissingSeen 进程内记忆，
//     服务恢复后清零可再报；重启清零 = 缺失存续时重报一次——重复优于
//     漏报，与 driftSeen 同语义）；substrate API 错误（超时/不可达）≠
//     服务缺失，只进日志不发事件——不得把平台自身故障当成 substrate
//     丢失（那会把平台故障放大成假披露）。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// substrateReconInterval 是存在性对账扫描的频控间隔（与漂移检测默认周期
// 同量级：外部 docker service rm 的发现延迟以 30s 计可接受；扫描只读
// 底座 + 单事务落库，幂等——重复执行只做确定性收敛）。
const substrateReconInterval = 30 * time.Second

// SubstrateRecon 单步执行存在性对账扫描（测试与诊断显式入口——直通频控
// 闸；生产由 tick 周期驱动）。
func (e *Engine) SubstrateRecon(ctx context.Context) { e.substrateRecon(ctx, true) }

// substrateRecon 是 tick 的存在性对账 duty（T0-V2.2/R2）。频控：非 force
// 形态受 substrateNextAt 时间闸（tick goroutine 专用字段，与
// deleteScanNextAt 同模式；重启即清零 = 重启后立即扫一拍）。
func (e *Engine) substrateRecon(ctx context.Context, force bool) {
	now := e.now()
	if !force && now.Before(e.substrateNextAt) {
		return
	}
	e.substrateNextAt = now.Add(substrateReconInterval)
	apps, err := e.store.ListActiveApps(ctx)
	if err != nil {
		e.log.Warn("engine: substrate recon list apps", "error", err)
		return
	}
	inFlight := map[string]bool{}
	if rows, err := e.store.ListNonTerminalDeployments(ctx); err != nil {
		e.log.Warn("engine: substrate recon in-flight check", "error", err)
		return
	} else {
		for _, r := range rows {
			inFlight[r.AppID] = true
		}
	}
	for _, app := range apps {
		if inFlight[app.ID] {
			continue // 发布过程本身就是期望态迁移：服务可能尚未创建，不判缺失
		}
		derived, err := e.store.GetAppDerivedState(ctx, app.ID)
		if err != nil {
			if errors.Is(err, state.ErrAppNotFound) {
				continue // 已删：无视图可修
			}
			e.log.Warn("engine: substrate recon read derived state", "app", app.Name, "error", err)
			continue
		}
		if derived != DerivedRunning {
			continue // 只对声称 running 的 app 对账（down/blocked/degraded 各有归属路径）
		}
		e.reconAppSubstrate(ctx, app.ID, app.Name)
	}
}

// reconAppSubstrate 核实单个 running 应用的服务存在性：任一期望服务缺失
// → 事件 + 派生态修正（running → down）；全部存在 → 清缺失记忆（服务
// 恢复后可再报）；底座读错误 → 只记日志（瞬态，下一拍重核）。
func (e *Engine) reconAppSubstrate(ctx context.Context, appID, appName string) {
	source, specs, err := e.lastSucceededSpecs(ctx, appID)
	if err != nil {
		e.log.Warn("engine: substrate recon read expectations", "app", appName, "error", err)
		return
	}
	if source == nil {
		return // 无成功部署：无期望态（判据无从建立，不在此扩权）
	}
	var missing []string
	for i := range specs {
		if _, err := e.sub.ServiceInspect(ctx, specs[i].Name); err != nil {
			if errors.Is(err, ErrServiceNotFound) {
				missing = append(missing, specs[i].Name)
				continue
			}
			// substrate API 错误（超时/不可达）≠ 服务缺失：不判缺失、不发
			// 事件、不改派生态，只进日志，下一拍重核。整应用放弃本拍——
			// 部分服务未核实就下缺失结论同样构成假披露。
			e.log.Warn("engine: substrate recon inspect failed (transient, not counted as missing)",
				"app", appName, "service", specs[i].Name, "error", err)
			return
		}
	}
	if len(missing) == 0 {
		delete(e.substrateMissingSeen, appID) // 服务恢复：记忆清零，下次缺失可再报（非永久静音）
		return
	}
	if e.substrateMissingSeen[appID] {
		return // 持续缺失已报过：不重复发事件（节流；派生态已修，扫描候选过滤同样挡住重入）
	}
	if err := e.reportSubstrateMissing(ctx, appID, appName, *source, missing); err != nil {
		if errors.Is(err, state.ErrAppDerivedStateConflict) {
			return // 并发翻转已离开 running：幂等跳过，下一拍候选过滤生效
		}
		e.log.Warn("engine: substrate recon report", "app", appName, "error", err)
		return // 未记缺失记忆：下一拍重试（事件与修正同事务，失败即整体未生效）
	}
	e.substrateMissingSeen[appID] = true
	e.log.Warn("engine: running app's service(s) absent from the substrate (view corrected to down)",
		"app", appName, "deployment", source.ID, "missing", strings.Join(missing, ","))
}

// reportSubstrateMissing 落缺失事件与审计，并把派生态修正为如实值
// （running → down）。事件、审计与派生 CAS 同事务（fail-closed：任一失败
// 整体回滚，不产生只有事件没有修正的半程披露，也不产生只有修正没有披露
// 的静默改写）。
func (e *Engine) reportSubstrateMissing(ctx context.Context, appID, appName string,
	source state.DeployRecord, missing []string) error {
	return e.store.InTx(ctx, func(tx *state.Tx) error {
		cur, err := tx.GetAppDerivedState(ctx, appID)
		if err != nil {
			if errors.Is(err, state.ErrAppNotFound) {
				return nil // app 已删：无视图可修
			}
			return err
		}
		if cur != DerivedRunning {
			return nil // 并发翻转已离开 running：离开本 duty 范围（扫描时与写入时双重校验）
		}
		if err := tx.SetAppDerivedState(ctx, appID, cur, DerivedDown); err != nil {
			return err
		}
		if err := appendEvents(ctx, tx, appEvent("app.substrate_missing", appName,
			"app_id", appID,
			"desired_deployment", source.ID,
			"missing_services", strings.Join(missing, ","))); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:  "system",
			Action: "app.substrate_missing",
			Target: "app:" + appName,
			Result: "ok",
			DiffSummary: state.DiffSummary("app", appName, "deployment", source.ID,
				"missing_services", strings.Join(missing, ",")),
		})
	})
}
