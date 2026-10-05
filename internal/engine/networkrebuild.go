package engine

// 网络重建动词（ADR-0046，N2 评审批 P1-4 根修）：平台中介的受监督重建。
// staging 四个 pre-F2.2 项目网无 Attachable 位挡住 Database Backup 的
// utility 附着；手工拆网会被平台 reconcile 回滚——orchestration 在平台
// 手里（本文件）才不会互踩。
//
// 执行序：受理（project 存活 + network 行在场）→ 载体枚举与归属裁决
// （外来/失锚附着 E_CONFLICT 拒绝并列出——平台对不认识的载体零动作）→
// 平台归属载体逐个 detach → 删网络（带界排水）→ ensureNetworks 同源复建
// → 载体逐个 re-attach（附件形状由 Provider 快照还原，与 spec 一致故无
// drift）→ 终态核验（attachable + 平台标签在位）。
//
// 串行化：全序持 maintenanceMu 写锁；全部 Ensure 族调用点（materialize/
// databaseStep/managedStep/driveEnsure/backup 环的 utility 附着）与
// TeardownDatabase（拆载体，2026-10-05 级联批入册）持读锁——读锁间照旧
// 并发，只有重建排他（ADR-0046 决策 3 的取舍记录）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// NetworkRebuildResult 是一次重建执行的载体计数（响应面直读）。
type NetworkRebuildResult struct {
	Detached   int
	Reattached int
}

// ForeignAttachmentError 列出平台无法归属的网络附着载体（受理拒绝面：
// 无 fleetly 域标签的外来 service，或域锚不在期望集的残留载体）。API 层
// 映射 E_CONFLICT——诚实拒绝，不碰不认识的载体（WorkloadOrphaned 只登记
// 原则的网络面同款）。
type ForeignAttachmentError struct {
	Carriers []string
}

func (e *ForeignAttachmentError) Error() string {
	return "network has attachments the platform cannot attribute: " + strings.Join(e.Carriers, ", ")
}

// RebuildNetwork 执行一次网络重建（API 层同步调用；全序 NetworkRebuildTimeout
// 硬界）。幂等：载体网已 attachable 且平台标签在位时走快速路径零扰动返回；
// 任意中间态失败后重跑收敛——中途被摘的载体由各域的强制重放节拍
// （ReconcileReplayInterval，缺省 60s）Ensure 兜底重挂，重跑则加速收口。
func (e *Engine) RebuildNetwork(ctx context.Context, projectID, name string) (NetworkRebuildResult, error) {
	// 受理：Project 存活（同族受理检查）+ network 行在场。
	p, err := e.projects.Get(ctx, e.db.Runner(), projectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return NetworkRebuildResult{}, fmt.Errorf("rebuild network: project %s not found: %w", projectID, state.ErrNotFound)
		}
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: load project %s: %w", projectID, err)
	}
	if p.Deleted() {
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: project %s not found: %w", projectID, state.ErrNotFound)
	}
	if _, err := e.networks.GetByName(ctx, e.db.Runner(), projectID, name); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return NetworkRebuildResult{}, fmt.Errorf("rebuild network: network %q not found in project %s: %w", name, projectID, state.ErrNotFound)
		}
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: load network: %w", err)
	}
	ops := capability.FacesOf(e.runtime).NetworkMaintenance
	if ops == nil {
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: the runtime provider does not implement the network maintenance face (ADR-0046)")
	}
	ns := capability.NamespaceRef{Team: p.TeamID, Project: projectID}

	// 串行化：写锁贯穿 detach→rm→create→attach 全序（读锁在各 Ensure 族
	// 调用点；锁等待不占各步预算——排队语义）。
	e.maintenanceMu.Lock()
	defer e.maintenanceMu.Unlock()

	bctx, cancel := context.WithTimeout(ctx, e.opts.NetworkRebuildTimeout)
	defer cancel()

	state0, err := ops.InspectNetwork(bctx, ns, name)
	if err != nil {
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: inspect carrier: %w", err)
	}
	if state0.Exists && state0.Attachable && state0.Managed {
		// 快速路径：已复建形态（幂等重跑/出生网误重建的零扰动返回）。
		return NetworkRebuildResult{}, nil
	}
	// 归属裁决（诚实拒绝面）。
	if err := e.checkNetworkAttachments(bctx, state0.Attachments); err != nil {
		return NetworkRebuildResult{}, err
	}

	// detach 全部当前附着（排序由 InspectNetwork 保证——确定性执行序）。
	for _, att := range state0.Attachments {
		if err := ops.DetachNetwork(bctx, ns, name, att.Carrier); err != nil {
			return NetworkRebuildResult{}, fmt.Errorf("rebuild network: detach %s (network kept; retry the rebuild to converge): %w", att.Carrier, err)
		}
	}
	detached := len(state0.Attachments)
	// 失败回滚：detach 之后的任何一步失败，把已 detach 的载体尽力挂回
	// （AttachNetwork 幂等）。数据库/受管域本有下一拍 ensure 重放自愈，
	// app 域没有周期重放面——不回滚会把 app 服务留在网外直到下次部署
	// （2026-10-05 staging 实录：失败重试间隙的半 detach 态）。bctx 此刻
	// 可能已耗尽，回滚用独立带界 ctx（utility remove 的 WithoutCancel 同款）。
	rollbackDetach := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), time.Minute)
		defer cancel()
		for _, att := range state0.Attachments {
			if err := ops.AttachNetwork(rctx, ns, name, att.Carrier); err != nil {
				e.log.Error("network rebuild: rollback re-attach", "carrier", att.Carrier, "network", name, "err", err)
			}
		}
	}

	if state0.Exists {
		if err := ops.RemoveNetwork(bctx, ns, name); err != nil {
			rollbackDetach()
			return NetworkRebuildResult{}, fmt.Errorf("rebuild network: remove carrier (detach rolled back; retry the rebuild to converge): %w", err)
		}
	}
	if err := ops.EnsureNetwork(bctx, ns, name); err != nil {
		rollbackDetach()
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: recreate carrier (network removed; retry the rebuild to recreate and re-attach): %w", err)
	}
	// 终态核验：复建撞名/竞态（重建窗内他方建了同名非 attachable 网）即
	// 诚实失败——不接受"删了旧网换来一个同样不可附着的新网"。
	after, err := ops.InspectNetwork(bctx, ns, name)
	if err != nil {
		rollbackDetach()
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: verify carrier: %w", err)
	}
	if !after.Exists || !after.Attachable || !after.Managed {
		rollbackDetach()
		return NetworkRebuildResult{}, fmt.Errorf("rebuild network: carrier for %q exists but is not the managed attachable form (exists=%t attachable=%t managed=%t); remove the foreign carrier network and retry", name, after.Exists, after.Attachable, after.Managed)
	}

	// re-attach（本次 detach 过的载体；已消失的载体在 Provider 侧幂等跳过）。
	for _, att := range state0.Attachments {
		if err := ops.AttachNetwork(bctx, ns, name, att.Carrier); err != nil {
			return NetworkRebuildResult{Detached: detached}, fmt.Errorf("rebuild network: attach %s (network rebuilt; retry the rebuild to re-attach the remaining carriers): %w", att.Carrier, err)
		}
	}
	return NetworkRebuildResult{Detached: detached, Reattached: detached}, nil
}

// checkNetworkAttachments 裁决附着载体清单的归属（ADR-0046 决策 2.2）：
// 期望集 = 域锚可解析到活跃行（App 轴 apps 表 / Task 轴 active·draining
// 行 / Database 轴活跃行）或在册受管 Provider 命名空间（Proxy 挂全部活跃
// 项目网）。载体标记值是 sanitizeNamePart 产物（平台 ID 的小写形），比对
// 大小写折叠。任一无法归属即 ForeignAttachmentError（E_CONFLICT 面）。
func (e *Engine) checkNetworkAttachments(ctx context.Context, attachments []capability.NetworkAttachment) error {
	if len(attachments) == 0 {
		return nil
	}
	apps, err := e.apps.List(ctx, e.db.Runner())
	if err != nil {
		return fmt.Errorf("rebuild network: list apps: %w", err)
	}
	tasks, err := e.tasks.ListDriving(ctx, e.db.Runner())
	if err != nil {
		return fmt.Errorf("rebuild network: list tasks: %w", err)
	}
	dbs, err := e.databases.List(ctx, e.db.Runner())
	if err != nil {
		return fmt.Errorf("rebuild network: list databases: %w", err)
	}
	appIDs, taskIDs, dbIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range apps {
		appIDs[strings.ToLower(apps[i].ID)] = true
	}
	for i := range tasks {
		taskIDs[strings.ToLower(tasks[i].ID)] = true
	}
	for i := range dbs {
		dbIDs[strings.ToLower(dbs[i].ID)] = true
	}
	managedNS := map[string]bool{}
	for _, decl := range e.managedProviders() {
		ns := decl.m.ManagedNamespace()
		managedNS[strings.ToLower(ns.Team+"/"+ns.Project)] = true
	}

	var foreign []string
	for _, att := range attachments {
		d := att.Domain
		zero := d.Team == "" && d.Project == "" && d.App == "" && d.Task == "" && d.Database == ""
		if att.Workload == "" && zero {
			foreign = append(foreign, att.Carrier+" (no fleetly labels)")
			continue
		}
		if managedNS[strings.ToLower(d.Team+"/"+d.Project)] {
			continue // 受管域载体（Proxy 挂项目网是部署形态的一部分）
		}
		switch {
		case d.App != "":
			if !appIDs[strings.ToLower(d.App)] {
				foreign = append(foreign, att.Carrier+" (app "+d.App+" is not an active app)")
			}
		case d.Task != "":
			if !taskIDs[strings.ToLower(d.Task)] {
				foreign = append(foreign, att.Carrier+" (task "+d.Task+" is not a driving task)")
			}
		case d.Database != "":
			if !dbIDs[strings.ToLower(d.Database)] {
				foreign = append(foreign, att.Carrier+" (database "+d.Database+" is not an active database)")
			}
		default:
			foreign = append(foreign, att.Carrier+" (fleetly labels carry no domain axis)")
		}
	}
	if len(foreign) > 0 {
		return &ForeignAttachmentError{Carriers: foreign}
	}
	return nil
}

// lockMaintenance 取维护互斥的读半边（Ensure 族调用点与网络重建的串行化
// 锚，ADR-0046）。约定：读锁在收敛步的带界 ctx 派生**之前**获取——锁等待
// 是排队语义，不占各步的超时预算（否则重建窗内的部署会以超时失败而非
// 排队）。返回解锁函数（defer 消费）。
func (e *Engine) lockMaintenance() func() {
	e.maintenanceMu.RLock()
	return e.maintenanceMu.RUnlock
}
