package dataservices

// Update 动词的双车道执行面（B2d，ADR-0095 决策 4/5/6；design §3.2/§3.3）：
// settings/resources 的期望态变更按旋钮 class 分流——
//
//	dynamic 子集：同步热应用先行（ApplyDynamicSettings → 一次性工具容器，
//	             RotatePassword 同序：先数据面后持久层）→ 成功经 ensure cache 端口
//	             写 AppliedDigest → 落行；失败 = E_SETTINGS_APPLY_FAILED
//	             （行不动 + 强制 Forget AppliedDigest——半应用黑洞修）。
//	             期望态移除的 dynamic 键以 Knob.Default 渲染进同一次热应用
//	             （复位到引擎原生缺省——引擎自持久面〔auto.conf/auto.cnf〕
//	             不随行删除自动回退，不复位即行与引擎永久分叉）。
//	static 子集：落行 + KickDatabases（既有受监督滚动机械；材料字节变 =
//	             载体滚动替换；ensure cache 签名第四段保证 static 变更即时重下发）。
//
// 单写边界（ADR-0095 决策 5「utilityMu」，红队 B-击一）：dynamic apply 的
// 全部驱动者（Update API 热应用、循环重放）共持 per-entity 互斥；Update 全程
// 持锁使 受理钩子（确认票内容绑定）→apply→ensure cache→行→烧票 原子化（堵并发
// Update 的 engine=B/行=A 倒挂与同票双兑）。
//
// 锁序（全部驱动者同序，防 ABBA）：utilityMu → maintenance 读半边（后者由
// 工具容器执行链 runUtilityContainer 内自取）。循环重放因此在 reconcile 取
// maintenance 读锁**之前**执行——锁内调用会构成 RWMutex 读锁递归（网络
// 重建写者排队时自死锁）且与 Update 倒挂锁序。
//
// 与 rotateMu 的分界：utilityMu 锁「utility 数据面动作族」的 dynamic 旋钮
// 应用面（写引擎自持久配置）；rotateMu 锁凭证轮换面（写认证面）——两族
// 引擎侧持久面分立（auto.conf/auto.cnf vs pg_authid/密码材料），交错执行
// 各自引擎语句原子、认证失败即整体失败可重试，无共享写面故不共锁。
//
// 重放门（ADR-0095 决策 6，红队 A7）：环 reconcile 对 dynamic 子集做幂等
// 重放——触发判据 = 行 settings 的 dynamic 摘要 ≠ ensure cache AppliedDigest；仅
// status=running 且非恢复挂起行执行（新行首拍/StartPeriod 窗/restorePass
// 在途不重放——否则穿窗失败工具容器 churn）；失败退避；重放检查在 ensure cache
// 短路判定之前（dynamic 半边是事件触发收敛，不随 Ensure 短路停摆）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 数据服务族共享值类型/哨兵随契约面下沉 engine/contract（engine 去应用
// 模块 import 第九批，2026-11-10）：包内既名经别名零漂移，定义真源在
// contract/dataservice.go。
var ErrSettingsApplyRejected = contract.ErrSettingsApplyRejected

type (
	ResourceUpdate = contract.ResourceUpdate
	UpdateRequest  = contract.UpdateRequest
	UpdateResult   = contract.UpdateResult
)

// dynamicApplyBackoff 是重放失败退避窗（对齐 ReconcileReplayInterval 缺省
// 节拍——失败后一窗内不重试，防持续故障面的工具容器 churn）。
const dynamicApplyBackoff = 60 * time.Second

// utilityLocks 是 per-entity 互斥锁族（utilityMu 的实现面：按 databaseID
// 取锁；锁对象惰性建且不回收——库行数有界（配额 16/项目），泄漏面为零）。
type utilityLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// acquire 取 key 上的锁并返回释放函数（rotateMu 先例的 per-entity 形态；
// 等待不占步预算——排队语义）。
func (u *utilityLocks) acquire(key string) func() {
	u.mu.Lock()
	if u.locks == nil {
		u.locks = map[string]*sync.Mutex{}
	}
	l, ok := u.locks[key]
	if !ok {
		l = &sync.Mutex{}
		u.locks[key] = l
	}
	u.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// settingsDigest 是 settings 子集的稳定摘要（sha256 截断 8 字节——
// credentialFingerprint 同款口径；键序排序，确定性序列化）。三个消费面：
// ensure cache 签名第四段（static 子集——复合代理，ADR-0095 决策 4 红队 A5）、
// AppliedDigest（dynamic 子集——重放失配判据，决策 6）、Update 车道
// 分流（同源单公式，防三处各拼一份漂移）。空子集 = 空串。
func settingsDigest(settings map[string]string) string {
	if len(settings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "=" + settings[k] + "\n"))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// UpdateDatabase 执行双车道分流（ADR-0095 决策 5）：utilityMu 全程持锁
// （Admit→apply→ensure cache→行→烧票 原子化）→ dynamic 子集同步热应用（未变
// 重放 = 摘要命中即跳过，零扰动 no-op；移除键复位缺省）→ static 子集 +
// 资源面落行 → Kick。行不存在返回 state.ErrNotFound（API 404 口径）；
// 热应用被拒返回 ErrSettingsApplyRejected（行不动 + ensure cache 强制 Forget——
// 循环重放以行值拉回收敛，半应用态不滞留）。值域校验（ValidateSettings/
// 资源上限/引擎版本不可变）由 API 受理位前置，status 门与确认票内容
// 绑定经 Admit 在锁内复核；本面是执行真源。
func (d *DataServices) UpdateDatabase(ctx context.Context, databaseID string, req UpdateRequest) (UpdateResult, error) {
	// 单写边界：per-entity 互斥全程持有（与循环重放共持——同一驱动族）。
	unlock := d.utilityMu.acquire(databaseID)
	defer unlock()

	row, err := d.repos.Databases.Get(ctx, databaseID)
	if err != nil {
		return UpdateResult{}, err
	}
	var redeem func()
	if req.Admit != nil {
		if redeem, err = req.Admit(row); err != nil {
			return UpdateResult{}, err
		}
	}
	tpl, err := databasetemplate.For(row.Engine, row.Version)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	// 期望态合并（全量替换语义）：settings/resources 的 nil = 不变面。
	nextSettings := row.Settings
	if req.Settings != nil {
		nextSettings = req.Settings
	}
	nextCPU, nextMemory := row.CPUMillis, row.MemoryMB
	if req.Resources != nil {
		nextCPU, nextMemory = req.Resources.CPUMillis, req.Resources.MemoryMB
	}
	_, oldDynamic := databasetemplate.SplitSettings(tpl.Knobs(), row.Settings)
	_, dynamicOnly := databasetemplate.SplitSettings(tpl.Knobs(), nextSettings)
	resets, err := dynamicResets(tpl.Knobs(), oldDynamic, dynamicOnly)
	if err != nil {
		return UpdateResult{}, err
	}

	// dynamic 车道：同步热应用先行（先数据面后持久层，RotatePassword 同序）。
	// 未变重放 = 行 dynamic 摘要与 ensure cache AppliedDigest 命中且无移除复位 →
	// 跳过工具容器（声明式幂等；进程重启后 ensure cache 失踪 → 摘要失配 → 幂等
	// 重放拉回，引擎语句同值自持久即 no-op）。
	res := UpdateResult{}
	wantDigest := settingsDigest(dynamicOnly)
	haveDigest, have := d.appliedDigestOf(d.applied, row.ID)
	switch {
	case len(resets) > 0 || (len(dynamicOnly) > 0 && (!have || haveDigest != wantDigest)):
		applySet := make(map[string]string, len(dynamicOnly)+len(resets))
		for k, v := range dynamicOnly {
			applySet[k] = v
		}
		for k, v := range resets {
			applySet[k] = v
		}
		if err := d.applyDynamicLocked(ctx, row, tpl, applySet, wantDigest); err != nil {
			return UpdateResult{}, err
		}
		res.DynamicApplied = sortedKeys(applySet)
	case len(dynamicOnly) == 0 && have:
		// 期望态已无 dynamic 面且无待复位键（ensure cache 残留）：Forget 归位——
		// 重放门对空子集不触发，残留 ensure cache 只会让下一次失配判据失真。
		d.appliedForget(d.applied, row.ID)
	}

	// static 车道 + 资源面：落行（dynamic 已成功才到达——失败路径行不动）。
	if err := d.repos.Databases.UpdateDesired(ctx, row.ID, nextSettings, nextCPU, nextMemory); err != nil {
		return UpdateResult{}, err
	}
	res.StaticRolled = changedStaticKeys(tpl, row.Settings, nextSettings)
	// 烧票在执行成功后、仍持锁时（Deploy discardConfirmTicket 先例）：同库
	// 并发同票的第二请求在锁外排队，入锁后 Admit 的 peek 即见 tombstone。
	if redeem != nil {
		redeem()
	}
	// static 变更即时送达：Kick 收敛循环（ensure cache 签名第四段已含 static 子集
	// 摘要——ensureFresh 短路失效，Ensure 真实重发且材料含新值，不等
	// 60s 兜底节拍）。未变重放同样 Kick（幂等，Ensure 短路照常命中）。
	d.kickDatabases()
	return res, nil
}

// dynamicResets 报告旧→新期望态间被移除的 dynamic 键 → 复位值（Knob.
// Default = 目录回显面的引擎原生缺省）。引擎自持久面（ALTER SYSTEM →
// postgresql.auto.conf / SET PERSIST → mysqld-auto.cnf）不随行删除回退：
// 移除键不复位 = 行宣称缺省、引擎停留旧值，且无 ensure cache 失配可触发收敛
// （重放门只对在册键失配）。复位以缺省值显式写入引擎自持久面（与引擎
// 原生 RESET 等效于该版本缺省）。在册 dynamic 旋钮缺 Default 即拒（注册
// 表不变量——databasetemplate 契约测试同批钉死，正常不可达）。
func dynamicResets(knobs []dataservicetemplate.Knob, oldDynamic, newDynamic map[string]string) (map[string]string, error) {
	if len(oldDynamic) == 0 {
		return nil, nil
	}
	byKey := make(map[string]dataservicetemplate.Knob, len(knobs))
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	var out map[string]string
	for key := range oldDynamic {
		if _, kept := newDynamic[key]; kept {
			continue
		}
		knob, ok := byKey[key]
		if !ok || knob.Default == "" {
			return nil, fmt.Errorf("dynamic knob %q carries no registered default, so removing it cannot be rendered as an engine reset", key)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = knob.Default
	}
	return out, nil
}

// applyDynamicLocked 执行一次 dynamic 热应用（utilityMu 已持有——「Locked」
// 后缀即此约定）：解密现凭证 → 模板渲染 → 工具容器执行 → 成功记 ensure cache /
// 失败强制 Forget（半应用黑洞修，ADR-0095 决策 6：多旋钮部分失败时
// 「行不动+AppliedDigest 不动」会让 ensure cache 永不失配、引擎停半应用态——
// Forget 打开失配口，循环重放以行值拉回收敛）。applySet 是本次渲染的键值
// 全集（期望态 dynamic 子集 ∪ 移除复位键）；wantDigest 是期望态 dynamic
// 子集摘要（复位键不入摘要——空摘要 = 期望态无 dynamic 面，成功即 Forget）。
func (d *DataServices) applyDynamicLocked(ctx context.Context, row *database.Database, tpl databasetemplate.Template, applySet map[string]string, wantDigest string) error {
	password, err := d.databasePassword(ctx, row)
	if err != nil {
		return err
	}
	spec, err := tpl.ApplyDynamicSettings(contract.DatabaseDNSName(row.ID), password, applySet)
	if err != nil {
		return err
	}
	if err := d.runSettingsUtility(ctx, row, tpl.Image(), tpl.ToolUser(), spec.Argv, spec.Env, spec.SecretFiles); err != nil {
		// 失败强制 Forget：无论部分还是全部语句失败，ensure cache 失配让环以行值
		// 拉回（行未动——期望态仍是旧值，重放即收敛回旧值）。
		d.appliedForget(d.applied, row.ID)
		return fmt.Errorf("%w: %s", ErrSettingsApplyRejected, err)
	}
	// 成功后、行落库同窗口写 ensure cache（apply 成功后崩溃 = ensure cache 已记、行未落
	//——ensure cache 随进程丢失，恢复后重放以**行值**（旧值）拉回，崩溃窗回退
	// 语义见 ADR-0095 决策 6：行单真源的正确代价，如实记录）。
	if wantDigest == "" {
		d.appliedForget(d.applied, row.ID)
	} else {
		d.appliedRemember(d.applied, row.ID, wantDigest)
	}
	return nil
}

// runSettingsUtility 执行热应用工具容器（与 rotate/备份/恢复同纪律：维护
// 互斥读半边 + 带界 + stderr/stdout 尾部入错误报文；runRotateUtility 同款
// 形态的 dynamic 应用专用包装——ID 前缀区分取证面）。
func (d *DataServices) runSettingsUtility(ctx context.Context, row *database.Database, image, user string, argv []string, env map[string]string, secretFiles map[string][]byte) error {
	req := capability.UtilityRequest{
		ID:          "dbsettings-" + row.ID,
		Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, Database: row.ID, DatabaseName: row.Name},
		Image:       image,
		User:        user, // 模板 ToolUser 声明（B8-fix 通道；数据库五引擎缺省空——零行为变化）
		Argv:        argv,
		Env:         env,
		Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
		SecretFiles: secretFiles,
	}
	return d.runUtilityContainer(ctx, req)
}

// runUtilityContainer 是一次性工具容器的共用执行链（rotate 的
// runRotateUtility 与 dynamic 热应用的 runSettingsUtility 同底座——B2d
// 自 rotate.go 收敛单源）：维护互斥读半边（ADR-0046：utility 附着项目网，
// 与网络重建串行化；锁等待不占执行预算）+ 带界（ManagedStepTimeout 硬
// 上限）+ stderr/stdout 尾部入错误报文（k3s 侧工具日志合流走 stdout，
// restore 面同款双通道取证）。调用方不得已持 maintenance 读锁（RWMutex
// 读锁不可递归——见包注锁序）。
func (d *DataServices) runUtilityContainer(ctx context.Context, req capability.UtilityRequest) error {
	if d.utility == nil {
		return fmt.Errorf("the utility face is not assembled (data service tools unavailable)")
	}
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	execCtx, cancel := d.boundedStep(ctx)
	defer cancel()
	stdout := &limitedBuffer{max: 2048}
	stderr := &limitedBuffer{max: 2048}
	if err := d.utility.RunUtility(execCtx, req, stdout, stderr); err != nil {
		msg := err.Error()
		if tail := contract.StderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		} else if tail := contract.StderrTail(stdout.String()); tail != "" {
			msg += ": " + tail
		}
		return errors.New(msg)
	}
	return nil
}

// replayDynamicSettings 是循环侧重放门（ADR-0095 决策 6）：失配触发幂等重放。
// 门四件（B2d 验收锚）：①失配触发（行 dynamic 摘要 ≠ ensure cache AppliedDigest）；
// ②仅 status=running 且非恢复挂起行执行；③失败退避（一窗内不重试）；
// ④重放检查在 ensure cache 短路判定之前执行（调用点在 reconcileDatabase 取
// maintenance 读锁与 ensureFresh 之前——dynamic 半边不随 Ensure 短路停摆，
// 锁序同 Update：utilityMu → maintenance）。经 utilityMu 与 Update 热应用
// 共持互斥（全部驱动者共持，决策 5）；锁内以重读行为准（List 快照可能
// 早于并发 Update 的落行——旧快照重放会把引擎拉回旧值）。
func (d *DataServices) replayDynamicSettings(ctx context.Context, row *database.Database) {
	if !replayEligible(row) {
		return
	}
	tpl, err := databasetemplate.For(row.Engine, row.Version)
	if err != nil {
		return // 模板离册的存量行：reconcile 主路径同判记日志（诊断面单源）。
	}
	_, dynamicOnly := databasetemplate.SplitSettings(tpl.Knobs(), row.Settings)
	if len(dynamicOnly) == 0 {
		return
	}
	if have, ok := d.appliedDigestOf(d.applied, row.ID); ok && have == settingsDigest(dynamicOnly) {
		return // 门①未失配：已应用态，零扰动。
	}
	// 门③：失败退避（窗内不重试；成功即清锚）。
	if d.replayInBackoff(row.ID) {
		return
	}
	// 经 utilityMu 与 Update 共持（per-entity 互斥；锁等待不占步预算）。
	unlock := d.utilityMu.acquire(row.ID)
	defer unlock()
	// 锁内重读：行 = 期望态单真源（等锁期间 Update 可能已改行/记 ensure cache）。
	fresh, err := d.repos.Databases.Get(ctx, row.ID)
	if err != nil || !replayEligible(fresh) {
		return
	}
	_, dynamicOnly = databasetemplate.SplitSettings(tpl.Knobs(), fresh.Settings)
	wantDigest := settingsDigest(dynamicOnly)
	if have, ok := d.appliedDigestOf(d.applied, fresh.ID); len(dynamicOnly) == 0 || (ok && have == wantDigest) {
		return
	}
	if err := d.applyDynamicLocked(ctx, fresh, tpl, dynamicOnly, wantDigest); err != nil {
		d.markReplayFailure(fresh.ID)
		d.log.Error("database replay: dynamic settings", "database", fresh.ID, "err", err)
		return
	}
	d.clearReplayFailure(fresh.ID)
	d.log.Info("database replay: dynamic settings converged", "database", fresh.ID, "keys", len(dynamicOnly))
}

// replayEligible 是重放门②（红队 A7）：仅 running 且恢复不在途（新行首拍
// pending/StartPeriod 窗与 restorePass 在途不重放——否则穿窗 60–90 次失败
// 工具容器 churn；ADR-0039 决策 6 修订：failed 终态不再算在途，恢复源历史
// 保留不影响判定）。
func replayEligible(row *database.Database) bool {
	return row.Status == database.StatusRunning && !database.RestoreActive(row.RestoreState)
}

// replayInBackoff 报告该行的重放是否仍处失败退避窗内（注入时钟口径——
// 与 markReplayFailure 同源，测试可控）。
func (d *DataServices) replayInBackoff(databaseID string) bool {
	d.replayMu.Lock()
	defer d.replayMu.Unlock()
	failAt, ok := d.replayFails[databaseID]
	return ok && d.clock.Now().Sub(failAt) < dynamicApplyBackoff
}

// markReplayFailure 落重放失败时刻（退避窗锚点）。
func (d *DataServices) markReplayFailure(databaseID string) {
	d.replayMu.Lock()
	defer d.replayMu.Unlock()
	if d.replayFails == nil {
		d.replayFails = map[string]time.Time{}
	}
	d.replayFails[databaseID] = d.clock.Now()
}

// clearReplayFailure 清退避锚（重放成功/域收口）。
func (d *DataServices) clearReplayFailure(databaseID string) {
	d.replayMu.Lock()
	defer d.replayMu.Unlock()
	delete(d.replayFails, databaseID)
}

// sortedKeys 报告 map 键集（排序稳定）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// changedStaticKeys 报告旧→新期望态间实际变更的 static 键集（UpdateResult.
// StaticRolled 的数据源；未变键不计——声明式重放 no-op 的注记面）。
func changedStaticKeys(tpl databasetemplate.Template, oldSettings, newSettings map[string]string) []string {
	oldStatic, _ := databasetemplate.SplitSettings(tpl.Knobs(), oldSettings)
	newStatic, _ := databasetemplate.SplitSettings(tpl.Knobs(), newSettings)
	var changed []string
	seen := map[string]bool{}
	for k, v := range newStatic {
		seen[k] = true
		if oldStatic[k] != v {
			changed = append(changed, k)
		}
	}
	for k := range oldStatic {
		if !seen[k] {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return changed
}
