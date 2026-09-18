package engine

// observing 阶段与运行期语义（release-semantics §2.2 L3/L4、§2.5 场景
// 7/8/9/10、architecture §2.5 默认参数）：观察窗 60s（v0.1 平台默认），
// 信号 = 崩溃循环（≥2 次退出）/ 窗末未恢复 / 副本水位不足 ≥10s；默认只告警
// （verdict=unstable + app degraded，不建新 deployment）；单次退出且自愈 →
// W_DEPLOY_INSTABILITY 警告通过；窗后只告警一次（L4）。成功终态固化版本
// 快照（revisions verified）+ pending env promote（随部署生效）。

import (
	"context"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// evaluateObserving 推进一个观察窗周期。
func (e *Engine) evaluateObserving(ctx context.Context, rec state.DeployRecord) error {
	// 观察窗内曾健康不可 cancel（409 语义）——请求位拒绝并清除。
	if rec.CancelRequested {
		return e.rejectCancel(ctx, rec)
	}

	specs, err := e.decodeSpecs(rec)
	if err != nil {
		return e.failSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
			"期望态快照不可读：人工处置")
	}

	now := e.now()
	windowEnd := rec.ObserveStartedAt.Add(e.cfg.ObserveWindow)
	type serviceObs struct {
		spec       ServiceSpec
		running    int
		desired    int
		crashCount int
	}
	obs := make([]serviceObs, 0, len(specs))
	for i := range specs {
		spec := specs[i]
		tasks, err := e.sub.TaskList(ctx, spec.Name)
		if err != nil {
			return e.transientOr(ctx, rec, err)
		}
		obs = append(obs, serviceObs{
			spec:       spec,
			desired:    desiredReplicasOf(spec),
			running:    countNewRunning(tasks, spec.Image),
			crashCount: countNewCrashes(tasks, spec.Image),
		})
	}

	// 崩溃循环（≥2 次退出）→ 观察窗不合格（场景 7）。
	for _, o := range obs {
		if o.crashCount >= 2 {
			return e.failSwitched(ctx, rec, "E_OBSERVE_CRASH_LOOP",
				fmt.Sprintf("服务 %s 观察窗内崩溃循环（退出 %d 次）", o.spec.Name, o.crashCount))
		}
	}

	// 副本水位不足 ≥10s（L3 信号）→ 观察窗不合格。
	below := false
	for _, o := range obs {
		key := rec.ID + "/" + o.spec.Name
		if o.running < o.desired {
			start, ok := e.waterMarks[key]
			if !ok {
				e.waterMarks[key] = now
				continue
			}
			if now.Sub(start) >= e.cfg.ReplicasBelowFor {
				below = true
			}
		} else {
			delete(e.waterMarks, key)
		}
	}
	if below {
		return e.failSwitched(ctx, rec, "E_OBSERVE_UNHEALTHY",
			fmt.Sprintf("观察窗内副本水位持续不足（≥%ds）", int64(e.cfg.ReplicasBelowFor/time.Second)))
	}

	if now.Before(windowEnd) {
		return nil // 窗口未满：继续观察
	}

	// 窗末判定。
	atWater := true
	singleCrash := false
	for _, o := range obs {
		if o.running < o.desired {
			atWater = false
		}
		if o.crashCount == 1 {
			singleCrash = true
		}
	}
	if !atWater {
		return e.failSwitched(ctx, rec, "E_OBSERVE_UNHEALTHY",
			"观察窗结束副本水位未恢复（窗末 unhealthy，场景 8）")
	}
	// 单次退出且窗末自愈 → 警告通过（场景 9，W_DEPLOY_INSTABILITY）。
	flags := rec.Flags
	if singleCrash && flags&state.DeployFlagInstabilityWarning == 0 {
		flags |= state.DeployFlagInstabilityWarning
		if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{Flags: &flags}); err != nil {
			return err
		}
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			return deploymentEvent(ctx, tx, "deployment.warning", rec.ID,
				"code", "W_DEPLOY_INSTABILITY")
		}); err != nil {
			return err
		}
	}
	return e.succeedDeployment(ctx, rec, flags)
}

// countNewCrashes 统计目标版本任务的退出次数（failed/rejected/complete）。
func countNewCrashes(tasks []TaskState, targetImage string) int {
	n := 0
	for _, t := range tasks {
		if !isNewVersionTask(t, targetImage) {
			continue
		}
		switch t.State {
		case "failed", "rejected", "complete":
			n++
		}
	}
	return n
}

// succeedDeployment 成功终态：版本快照固化（revisions verified + 部署行
// revision_id 回填，同事务；保留窗裁剪由 CreateRevision 同事务执行）→ 终态
// CAS + 事件 → pending env promote（随部署生效；kind=rollback 不 promote，
// 见下）→ app 派生状态。
func (e *Engine) succeedDeployment(ctx context.Context, rec state.DeployRecord, flags int64) error {
	specs, err := e.decodeSpecs(rec)
	if err != nil {
		return e.failSwitched(ctx, rec, "E_DEPLOY_INTERRUPTED",
			"期望态快照不可读：人工处置")
	}
	composeNormalized := e.composeNormalizedSnapshot(ctx, rec)
	var revisionID string
	err = e.store.InTx(ctx, func(tx *state.Tx) error {
		rev, err := tx.CreateRevision(ctx, state.RevisionWrite{
			AppID:             rec.AppID,
			ComposeNormalized: composeNormalized,
			Overlay:           overlayOf(specs),
			DesiredHash:       rec.DesiredHash,
		})
		if err != nil {
			return err
		}
		revisionID = rev.ID
		if err := tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "deployment.succeeded",
			Target:      "deployment:" + rec.ID,
			Result:      "ok",
			DiffSummary: `{"revision":"` + rev.ID + `","desired_hash":"` + rec.DesiredHash + `"}`,
		}); err != nil {
			return err
		}
		if err := deploymentEvent(ctx, tx, "deployment.succeeded", rec.ID, "revision", rev.ID); err != nil {
			return err
		}
		if rec.Kind == kindRollback {
			// 回滚成功：快照重放通过完整健康门 + 观察窗，回退版本固化为
			// 新 revision（版本历史里回滚 = 一次成功部署，§2.4）。
			return deploymentEvent(ctx, tx, "deployment.rollback_finished", rec.ID,
				"revision", rev.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	to := state.DeploySucceeded
	from := state.DeployObserving
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
		Status:     &to,
		PrevStatus: &from,
		Flags:      &flags,
		RevisionID: &revisionID,
	}); err != nil {
		return err
	}
	rec.Status = state.DeploySucceeded

	// pending env 随部署生效（architecture §2.4 变量合并行；
	// MarkAppEnvEffective 自带 app.env_applied 审计与幂等）。kind=rollback
	// 跳过：回滚重放的 env 随快照（D-REL-9「非密钥 env 随快照回滚」），
	// pending 平台层未被本次部署消费——留待下次显式部署生效，不虚报生效。
	if rec.Kind != kindRollback {
		if _, err := e.store.MarkAppEnvEffective(ctx, rec.AppID); err != nil {
			return err
		}
	}
	return e.refreshDerivedState(ctx, rec.AppID, rec.AppName)
}

// composeNormalizedSnapshot 生成版本快照的归一化 compose（§2.4：受控子集
// 内的服务与卷定义，canonical JSON）。优先重载 compose 文件（spec_hash 复核
// ——漂移则回退合成摘要，成功不该被文件丢失阻断）。
func (e *Engine) composeNormalizedSnapshot(ctx context.Context, rec state.DeployRecord) string {
	if spec, _, err := compose.Load(ctx, rec.ComposePath); err == nil && spec.SpecHash == rec.SpecHash {
		if raw, err := spec.CanonicalJSON(); err == nil {
			return string(raw)
		}
	}
	type fallback struct {
		App             string `json:"app"`
		SpecHash        string `json:"spec_hash"`
		EnvSnapshotHash string `json:"env_snapshot_hash"`
		Note            string `json:"note"`
	}
	raw, err := canonicalJSON(fallback{
		App:             rec.AppName,
		SpecHash:        rec.SpecHash,
		EnvSnapshotHash: rec.EnvSnapshotHash,
		Note:            "compose 文件在成功时不可重载，快照退化为哈希摘要（详见遗留记录）",
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// watchPostWindow 是 L4 运行期稳定性巡检（只告警一次）：对最近一次成功
// 部署、已过观察窗、未告警过的应用，检查其服务在窗后是否出现任务退出——
// 出现即 deployment.warning（E_DEPLOY_POST_WINDOW_UNSTABLE）+
// app.instability_detected + app degraded（场景 10；不计数升级、不自动回滚）。
func (e *Engine) watchPostWindow(ctx context.Context) {
	apps, err := e.store.ListActiveApps(ctx)
	if err != nil {
		e.log.Warn("engine: list apps for post-window watch", "error", err)
		return
	}
	for _, app := range apps {
		rows, err := e.store.ListAppDeployments(ctx, app.ID, 1)
		if err != nil || len(rows) == 0 {
			continue
		}
		rec := rows[0]
		if rec.Status != state.DeploySucceeded ||
			rec.Flags&state.DeployFlagPostWindowAlerted != 0 ||
			rec.ObserveStartedAt.IsZero() {
			continue
		}
		windowEnd := rec.ObserveStartedAt.Add(e.cfg.ObserveWindow)
		if e.now().Before(windowEnd) {
			continue
		}
		specs, err := e.decodeSpecs(rec)
		if err != nil {
			continue
		}
		unstable := false
		for i := range specs {
			tasks, err := e.sub.TaskList(ctx, specs[i].Name)
			if err != nil {
				continue
			}
			for _, t := range tasks {
				if isNewVersionTask(t, specs[i].Image) &&
					(t.State == "failed" || t.State == "complete" || t.State == "rejected") &&
					t.Timestamp.After(windowEnd) {
					unstable = true
					break
				}
			}
			if unstable {
				break
			}
		}
		if !unstable {
			continue
		}
		flags := rec.Flags | state.DeployFlagPostWindowAlerted
		if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{Flags: &flags}); err != nil {
			return
		}
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			if err := deploymentEvent(ctx, tx, "deployment.warning", rec.ID,
				"code", "E_DEPLOY_POST_WINDOW_UNSTABLE"); err != nil {
				return err
			}
			return appEvent(ctx, tx, "app.instability_detected", rec.AppName)
		}); err != nil {
			e.log.Warn("engine: emit post-window warning", "deployment", rec.ID, "error", err)
			return
		}
		if err := e.refreshDerivedState(ctx, rec.AppID, rec.AppName); err != nil {
			e.log.Warn("engine: refresh derived state", "app", rec.AppName, "error", err)
		}
	}
}

// overlayOf 生成平台覆盖层 JSON（镜像 digest 集合；secret 引用/路由/节点
// 绑定随后续票扩充）。
func overlayOf(specs []ServiceSpec) string {
	type overlay struct {
		Images map[string]string `json:"images"`
	}
	o := overlay{Images: map[string]string{}}
	for i := range specs {
		o.Images[specs[i].Name] = specs[i].Image
	}
	raw, err := canonicalJSON(o)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
