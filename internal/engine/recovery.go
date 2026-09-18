package engine

// 终态写入、cancel 语义与控制面重启恢复（release-semantics §2.3 尾部三条
// + §2.5 场景 11/14；state-model §2.10 app 派生状态）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// failTransition 落 failed 终态（CAS：当前状态 → failed）+ deployment.failed
// 事件 + auto_abort 审计（system + reason=错误码，release-semantics §2.7）。
// 已终态的竞争落败返回 nil（幂等收敛）。
func (e *Engine) failTransition(ctx context.Context, rec state.DeployRecord, code, detail string) error {
	to := state.DeployFailed
	from := rec.Status
	errorCode := code
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
		Status:     &to,
		PrevStatus: &from,
		ErrorCode:  &errorCode,
	}); err != nil {
		if errors.Is(err, state.ErrDeploymentStateTransition) {
			return nil // 已被并发推进（重启恢复/CLI 竞争）：终态不可逆
		}
		return err
	}
	rec.Status = state.DeployFailed
	rec.ErrorCode = code
	return e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "deployment.auto_abort",
			Target:      "deployment:" + rec.ID,
			Result:      "ok",
			ErrorCode:   code,
			DiffSummary: `{"app":"` + rec.AppName + `","detail":"` + jsonEscape(detail) + `"}`,
		}); err != nil {
			return err
		}
		return deploymentEvent(ctx, tx, "deployment.failed", rec.ID,
			"code", code, "detail", detail)
	})
}

// failTransitionErr 是信封错误的失败出口（从 *apperr.Error 取码与文案）。
func (e *Engine) failTransitionErr(ctx context.Context, rec state.DeployRecord, err error) error {
	ae := appErrOf(err, rec.ID)
	return e.failTransition(ctx, rec, ae.Code(), ae.Message())
}

// cancelTerminal 未触底座阶段（queued/preparing/building）的取消：直接落
// cancelled（无归位动作——底座未被改动）。
func (e *Engine) cancelTerminal(ctx context.Context, rec state.DeployRecord) error {
	to := state.DeployCancelled
	from := rec.Status
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
		Status:     &to,
		PrevStatus: &from,
	}); err != nil {
		return err
	}
	rec.Status = state.DeployCancelled
	return e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := deploymentEvent(ctx, tx, "deployment.cancelled", rec.ID); err != nil {
			return err
		}
		return auditDeployment(ctx, tx, "human", "deployment.cancel", rec.ID, "ok", "",
			`{"stage":"`+string(from)+`"}`)
	})
}

// cancelDeployment releasing（未切流）取消：先归位再落 cancelled（§2.3）。
// 无有效版本时按首发语义 scale=0 保留现场（不置 substrate_halted——取消
// 不是失败）。
func (e *Engine) cancelDeployment(ctx context.Context, rec state.DeployRecord) error {
	previous, err := e.lastActiveSnapshot(ctx, rec)
	if err != nil {
		return err
	}
	recovery := state.RecoveryRestore
	patch := state.DeploymentPatch{}
	if previous != nil {
		if err := e.restoreSnapshot(ctx, rec, previous); err != nil {
			return e.failTransitionErr(ctx, rec, errorf("E_ROLLBACK_FAILED",
				"取消的归位重放失败（critical，不再二次自动）：%v", err))
		}
		patch.Recovery = &recovery
	} else if specs, derr := e.decodeSpecs(rec); derr == nil {
		if err := e.scaleToZero(ctx, rec, specs); err != nil {
			return e.failTransitionErr(ctx, rec, err)
		}
	}
	clear := false
	patch.CancelRequested = &clear
	to := state.DeployCancelled
	from := rec.Status
	patch.Status = &to
	patch.PrevStatus = &from
	if err := e.store.UpdateDeployment(ctx, rec.ID, patch); err != nil {
		return err
	}
	rec.Status = state.DeployCancelled
	if err := e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := deploymentEvent(ctx, tx, "deployment.cancelled", rec.ID); err != nil {
			return err
		}
		return auditDeployment(ctx, tx, "human", "deployment.cancel", rec.ID, "ok", "",
			`{"stage":"releasing","restored":true}`)
	}); err != nil {
		return err
	}
	return e.refreshDerivedState(ctx, rec.AppID, rec.AppName)
}

// rejectCancel 曾健康（已切流）不可取消：409 语义（建议改用 rollback）。
// 清除请求位并写 error 审计；deployment 保持在途。
func (e *Engine) rejectCancel(ctx context.Context, rec state.DeployRecord) error {
	clear := false
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{CancelRequested: &clear}); err != nil {
		return err
	}
	return e.store.InTx(ctx, func(tx *state.Tx) error {
		return auditDeployment(ctx, tx, "human", "deployment.cancel", rec.ID,
			"error", "E_STATE_VERSION_CONFLICT",
			`{"reason":"already_switched","message":"deployment 曾健康（已切流），不可 cancel；建议改用 rollback"}`)
	})
}

// CancelRequest 是 CLI 的取消入口：准入预检（曾健康 → 409 信封）+ 请求位
// 置位（引擎消费执行归位）。
func (e *Engine) CancelRequest(ctx context.Context, rec state.DeployRecord) error {
	if !rec.FirstHealthyAt.IsZero() || rec.Status == state.DeployObserving {
		return apperr.New("E_STATE_VERSION_CONFLICT",
			"deployment %s 已切流（曾健康），不可 cancel（409）：建议改用 rollback", rec.ID).
			WithContext("deployment", rec.ID).
			WithContext("reason", "already_switched")
	}
	if rec.Status.Terminal() {
		return apperr.New("E_STATE_VERSION_CONFLICT",
			"deployment %s 已是终态（%s），不可 cancel", rec.ID, rec.Status).
			WithContext("deployment", rec.ID).
			WithContext("reason", "terminal")
	}
	set := true
	return e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{CancelRequested: &set})
}

// recoverInterrupted 控制面重启的启动扫描（§2.3）：非终态 deployment 分类
// 恢复——健康 → 重开完整观察窗；paused/failed → 分类 + 归位；无法判定 →
// 失败（E_DEPLOY_INTERRUPTED）+ 人工。底座未就绪时静默（下一 tick 重试，
// 由准备预算看门）。
func (e *Engine) recoverInterrupted(ctx context.Context) {
	if err := e.sub.SwarmReady(ctx); err != nil {
		return
	}
	rows, err := e.store.ListNonTerminalDeployments(ctx)
	if err != nil {
		e.log.Warn("engine: recovery scan", "error", err)
		return
	}
	for _, rec := range rows {
		switch rec.Status {
		case state.DeployQueued, state.DeployPreparing, state.DeployBuilding:
			// 底座未被改动：tick 幂等重入。
		case state.DeployReleasing:
			if err := e.classifyRecovering(ctx, rec); err != nil {
				e.log.Warn("engine: recovery classification", "deployment", rec.ID, "error", err)
			}
		case state.DeployObserving:
			if err := e.reopenObserveWindow(ctx, rec); err != nil {
				e.log.Warn("engine: reopen observe window", "deployment", rec.ID, "error", err)
			}
		}
	}
}

// classifyRecovering 对重启时处于 releasing 的部署分类（场景 11）：
// Swarm pause → 失败分流；全部服务已切换 → 观察窗；无法判定 →
// E_DEPLOY_INTERRUPTED 失败 + 归位。
func (e *Engine) classifyRecovering(ctx context.Context, rec state.DeployRecord) error {
	specs, err := e.decodeSpecs(rec)
	if err != nil {
		return e.failUnswitchedOrSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
			"控制面重启后期望态快照不可读：人工处置")
	}
	anyPaused := false
	allSwitched := len(specs) > 0
	for i := range specs {
		spec := specs[i]
		svc, err := e.sub.ServiceInspect(ctx, spec.Name)
		if err != nil {
			if errors.Is(err, ErrServiceNotFound) {
				anyPaused = true
				allSwitched = false
				break
			}
			return nil // 底座暂态：下一 tick 由看门狗兜底
		}
		tasks, err := e.sub.TaskList(ctx, spec.Name)
		if err != nil {
			return nil
		}
		if svc.UpdateState == "paused" {
			anyPaused = true
		}
		if countNewRunning(tasks, spec.Image) >= desiredReplicasOf(spec) &&
			(svc.UpdateState == "" || svc.UpdateState == "completed") {
			continue
		}
		allSwitched = false
	}
	switch {
	case anyPaused:
		code, detail := e.classifyUpdateFailure(ctx, specs)
		return e.failUnswitchedOrSwitched(ctx, rec, code, "控制面重启后分类恢复："+detail)
	case allSwitched:
		return e.enterObserving(ctx, rec)
	default:
		// 无法判定（更新中/无进展）→ 失败 + 人工（§2.3；E_DEPLOY_INTERRUPTED）。
		return e.failUnswitchedOrSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
			"控制面重启后部署现场无法判定（更新中或无进展）：人工确认后重新发起部署")
	}
}

// reopenObserveWindow 观察窗重启恢复：健康 → 重开完整观察窗（§2.3）；
// 水位不齐 → E_DEPLOY_INTERRUPTED 失败。
func (e *Engine) reopenObserveWindow(ctx context.Context, rec state.DeployRecord) error {
	specs, err := e.decodeSpecs(rec)
	if err != nil {
		return e.failSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
			"控制面重启后期望态快照不可读：人工处置")
	}
	for i := range specs {
		tasks, err := e.sub.TaskList(ctx, specs[i].Name)
		if err != nil {
			return nil // 底座暂态
		}
		if countNewRunning(tasks, specs[i].Image) < desiredReplicasOf(specs[i]) {
			return e.failSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
				"控制面重启后观察窗现场不健康（副本水位不齐）：人工确认后重新发起部署")
		}
	}
	observeStart := e.now()
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{ObserveStartedAt: &observeStart}); err != nil {
		return err
	}
	rec.ObserveStartedAt = observeStart
	return e.store.InTx(ctx, func(tx *state.Tx) error {
		return deploymentEvent(ctx, tx, "deployment.observe_started", rec.ID,
			"window_seconds", fmt.Sprintf("%d", int64(e.cfg.ObserveWindow/time.Second)),
			"reason", "control_plane_restart")
	})
}

// ── app 派生状态（state-model §2.10）────────────────────────────────────────

// AppFacts 是派生裁决的输入事实。
type AppFacts struct {
	// PlacementState 是绑定状态位（'' = 无绑定记录——自由调度非 blocked）。
	PlacementState string
	// Latest 是最近一条部署记录（零值 = 无部署）。
	Latest state.DeployRecord
	// LatestSucceeded 是最近一条 succeeded 部署（零值 = 无有效版本）。
	LatestSucceeded state.DeployRecord
}

// 派生状态词表（state 词表对齐：running/degraded/blocked/down）。
const (
	DerivedRunning  = "running"
	DerivedDegraded = "degraded"
	DerivedBlocked  = "blocked"
	DerivedDown     = "down"
)

// DeriveAppState 是纯函数裁决：down > blocked > degraded > running。
//   - down：首发失败 scale=0（substrate_halted），或无有效版本且最近一次
//     失败未切流（没有任何期望实例——归位为零动作/scale 0）；
//   - blocked：placement.state ∈ {blocked, unresolved}（绑定不可用/已移除）；
//   - degraded：观察窗失败（verdict=unstable——含首发已切流后失败的形态，
//     新版本仍在服务）/ 窗后不稳定 / 警告通过（W_DEPLOY_INSTABILITY）；
//   - running：以上皆否。
func DeriveAppState(f AppFacts) string {
	latestFailed := f.Latest.ID != "" && f.Latest.Status == state.DeployFailed
	// down（最高优先级）。
	if latestFailed && f.Latest.SubstrateHalted {
		return DerivedDown
	}
	if f.LatestSucceeded.ID == "" {
		if f.Latest.ID == "" {
			return DerivedDown // 无任何部署记录（无期望实例）
		}
		if latestFailed && f.Latest.FirstHealthyAt.IsZero() {
			return DerivedDown // 首发未切流失败：归位无对象，无期望实例
		}
		// 首发已切流（观察窗失败 unstable）：新版本仍服务 → degraded。
	}
	// blocked。
	switch state.PlacementState(f.PlacementState) {
	case state.PlacementBlocked, state.PlacementUnresolved:
		return DerivedBlocked
	}
	// degraded。
	if latestFailed && f.Latest.Verdict == state.VerdictUnstable {
		return DerivedDegraded
	}
	if f.LatestSucceeded.ID != "" &&
		f.LatestSucceeded.Flags&(state.DeployFlagPostWindowAlerted|state.DeployFlagInstabilityWarning) != 0 {
		return DerivedDegraded
	}
	return DerivedRunning
}

// appFactsOf 读取派生输入事实。
func (e *Engine) appFactsOf(ctx context.Context, appID string) (AppFacts, error) {
	f := AppFacts{}
	if p, err := e.store.GetPlacement(ctx, appID); err == nil {
		f.PlacementState = string(p.State)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return f, err
	}
	if rows, err := e.store.ListAppDeployments(ctx, appID, 1); err == nil && len(rows) > 0 {
		f.Latest = rows[0]
	} else if err != nil {
		return f, err
	}
	rows, err := e.store.ListAppDeployments(ctx, appID, 25)
	if err != nil {
		return f, err
	}
	for _, r := range rows {
		if r.Status == state.DeploySucceeded {
			f.LatestSucceeded = r
			break
		}
	}
	return f, nil
}

// refreshDerivedState 推导并落库 app 派生状态；翻转时发映射事件
// （进入 degraded → app.degraded；消除 → app.recovered；blocked 由
// placement.* 驱动的事件承载——不重复发 app 事件）。
func (e *Engine) refreshDerivedState(ctx context.Context, appID, appName string) error {
	facts, err := e.appFactsOf(ctx, appID)
	if err != nil {
		return err
	}
	next := DeriveAppState(facts)
	var cur string
	err = e.store.InTx(ctx, func(tx *state.Tx) error {
		var err error
		cur, err = tx.GetAppDerivedState(ctx, appID)
		if err != nil {
			if errors.Is(err, state.ErrAppNotFound) {
				return nil
			}
			return err
		}
		if cur == next {
			return nil
		}
		if err := tx.SetAppDerivedState(ctx, appID, cur, next); err != nil {
			return err
		}
		// 事件映射（state-model §2.10）：degraded 进入/退出。
		if next == DerivedDegraded {
			return appEvent(ctx, tx, "app.degraded", appName)
		}
		if cur == DerivedDegraded && next == DerivedRunning {
			return appEvent(ctx, tx, "app.recovered", appName)
		}
		return nil
	})
	if err != nil && !errors.Is(err, state.ErrAppDerivedStateConflict) {
		return err
	}
	return nil
}

// AppDerivedState 是读面出口（CLI deploy 结果输出用）。
func (e *Engine) AppDerivedState(ctx context.Context, appID string) (string, error) {
	facts, err := e.appFactsOf(ctx, appID)
	if err != nil {
		return "", err
	}
	return DeriveAppState(facts), nil
}

// jsonEscape 把任意串安全嵌入 JSON 字符串值（审计 diff 摘要用）。
func jsonEscape(s string) string {
	raw, err := canonicalJSON(map[string]string{"v": s})
	if err != nil {
		return ""
	}
	// 去掉 {"v": 与收尾 }：canonical JSON 保证值内引号已转义。
	const prefix = `{"v":`
	out := string(raw)
	out = trimPrefix(out, prefix)
	return trimSuffix(out, "}")
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

func trimSuffix(s, p string) string {
	if len(s) >= len(p) && s[len(s)-len(p):] == p {
		return s[:len(s)-len(p)]
	}
	return s
}
