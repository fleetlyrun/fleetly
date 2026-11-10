package intake

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Step 是构建循环的收敛步：拾取 queued（并发余量内）→ 起 goroutine
// 执行（per-Build 单写者：行级 CAS，goroutine 生命周期 = 该行写者）。
// P4 wave-2b 接收者下域 *intake → P5 wave-5 拆包导出 Step（循环表 step 经
// 契约 step 取值 contract.StepOf(e.intake, "build")，五个应用模块同款）；
// 构建 goroutine 经 spawn 端口计入 wg（Add/Done 配对单源留核，logpipes/
// metrics 同款）。
func (in *Intake) Step(ctx context.Context) {
	active, err := in.repos.Builds.ListActive(ctx)
	if err != nil {
		in.log.Error("build step: list active", "err", err)
		return
	}
	running := 0
	for _, b := range active {
		if b.State == build.StateBuilding {
			running++
		}
	}
	for _, b := range active {
		if b.State != build.StateQueued || running >= in.buildOpts.Concurrency {
			continue
		}
		// 拾取前置检（D-4）：无进程内登记的 queued 行不允许空拾取——无输入
		// 的执行只会弹跳或误终态。归它或等登记，见 buildPickable。
		if !in.buildPickable(ctx, &b) {
			continue
		}
		// CAS queued→building（四件一拍）后立即返回；执行在 goroutine（构建
		// 生命周期绑定进程而非收敛步——step ctx 取消不等构建，优雅退出由
		// executeBuild 内的 shutdown 路径回 queued 重放）。goroutine 计入
		// wg（Q-7）：Stop 取消 runCtx 即有界排水，不默等构建超时。
		fresh, err := in.transitBuildRelease(ctx, &b,
			[]build.State{build.StateQueued}, build.StateBuilding, nil)
		if err != nil {
			in.log.Error("build step: transit", "build", b.ID, "err", err)
			continue
		}
		running++
		in.spawn(func() { in.executeBuild(fresh) }) // 构建 goroutine 生命周期即 Build 行写者（架构 §6 per-线单写者）
	}
}

// buildPickable 是 queued 行的拾取前置检（D-4）：无进程内输入登记的行
// 只有两种来路——①归属部署仍活跃（重启后待 DriveBuilding 幂等重建登记，
// 本拍跳过等登记落地）；②归属部署已终态（取消/抢占后遗留的孤儿行）→
// 一跳到终态 cancelled（同步、无 goroutine），禁止拾取执行形成
// queued↔building 振荡。已登记的行正常放行。
func (in *Intake) buildPickable(ctx context.Context, b *build.Build) bool {
	in.build.inputsMu.Lock()
	_, registered := in.build.inputs[b.ID]
	in.build.inputsMu.Unlock()
	if registered {
		return true
	}
	owner, err := in.buildOwnerActive(ctx, b)
	if err != nil {
		// 归属判定失败（存储故障）：保守跳过本拍——不基于不确定的探测销毁行。
		in.log.Error("build step: probe owner", "build", b.ID, "err", err)
		return false
	}
	if owner {
		return false // 部署驱动将在 DriveBuilding 内重建输入（下拍拾取）
	}
	if _, err := in.transitBuildRelease(ctx, b,
		[]build.State{build.StateQueued}, build.StateCancelled,
		func(m *build.Build) { m.Error = "orphaned build: owning deployment is no longer active" }); err != nil {
		in.log.Error("build step: cancel orphan", "build", b.ID, "err", err)
	}
	return false
}

// buildOwnerActive 报告 Build 的归属部署是否仍活跃（同 App 且目标
// Revision 一致：该部署进入 building 态时 DriveBuilding 会重建输入登记）。
func (in *Intake) buildOwnerActive(ctx context.Context, b *build.Build) (bool, error) {
	active, err := in.repos.Deployments.ActiveByApp(ctx, b.AppID)
	if err != nil {
		return false, err
	}
	for i := range active {
		if active[i].ToRevision == b.RevisionID {
			return true, nil
		}
	}
	return false, nil
}

// forgetBuildInput 释放单条构建输入登记（终态唯一释放口；登记写入点在
// buildsource.go；终态收口经 transitBuildRelease）。输入含推送凭证明文，
// 终态后不得常驻内存（R03-M1）。
func (in *Intake) forgetBuildInput(buildID string) {
	in.build.inputsMu.Lock()
	delete(in.build.inputs, buildID)
	in.build.inputsMu.Unlock()
}

// ResetOrphanBuilds 把崩溃遗留的 building 行重置 queued（重放：buildkit
// 缓存幂等）。输入登记不在此重建——DriveBuilding 见到在途行时幂等重建
// （D-4）；归属部署已终态的孤儿行由 Step 前置检一跳到终态。P4 wave-2b
// 自核 lifecycle.go 迁入本域；P5 wave-5 拆包导出（Start 调用点经应用模块接口
// e.intake.ResetOrphanBuilds，sweepInterruptedBackups 先例）。
func (in *Intake) ResetOrphanBuilds(ctx context.Context) {
	if len(in.builders) == 0 {
		return
	}
	active, err := in.repos.Builds.ListActive(ctx)
	if err != nil {
		in.log.Error("build: reset orphans", "err", err)
		return
	}
	for _, b := range active {
		if b.State != build.StateBuilding {
			continue
		}
		if _, err := in.transitBuildRelease(ctx, &b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil {
			in.log.Error("build: reset orphan", "build", b.ID, "err", err)
		}
	}
}

// transitBuildRelease 是 transitBuild 端口（Build 四件一，真源在核
// transition.go）的应用模块侧包装：迁移完成后终态触发本域副作用——日志缓冲
// 终态登记与超龄回收（N0.1 P2-1）+ 构建输入登记释放（R03-M1：
// BuildRequest 携带推送凭证明文，终态后不得常驻内存）。
//
// P5 wave-5 冻结边裁决（ADR-0088 P5 验收锚「22 边裁决表」transition.go
// 终态释放条目 = 原 transition.go:420/443 两条边）：终态副作用函数随应用模块
// 走（ADR-0088 已裁明的处置二选一之一），核 transitBuild 同批纯化为四件一
// 本体（与 transitRunFour 同形，transition 唯写序列冻结面零残留应用模块边）；
// 本域全部迁移调用点经本包装（副作用时机与迁移完成的同调零漂移）。
func (in *Intake) transitBuildRelease(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error) {
	fresh, err := in.transitBuild(ctx, b, from, to, mut)
	if err != nil {
		return nil, err
	}
	if fresh.State.Terminal() {
		// 终态登记触发超龄缓冲回收（frames map 只增不清会泄漏，N0.1 P2-1）。
		in.build.logs.markTerminal(b.ID)
		// 终态释放输入登记（R03-M1）：BuildRequest 携带推送凭证明文，且每次
		// 构建一行——只增不清即进程生命期凭据常驻 + 内存泄漏。全部终态
		//（succeeded/failed/expired/cancelled）都经本口。
		in.forgetBuildInput(b.ID)
	}
	return fresh, nil
}

// executeBuild 执行一次构建（goroutine；根 = 引擎进程生命期 rootCtx 端口，
// 构建超时独立包裹其上）。幂等重放：崩溃遗留的 building 行由 Start 侧重置
// queued 重跑（buildkit 缓存保证重放成本可控）。
func (in *Intake) executeBuild(b *build.Build) {
	// Q-7：根取引擎 runCtx（Start 物化；Stop 取消 → 下方 runCtx.Err() 分支
	// 优雅回 queued，排水有界），超时独立包裹（Stop 不默等 15m 看门狗）。
	runCtx, cancel := context.WithTimeout(in.rootCtx(), in.buildOpts.Timeout)
	defer cancel()
	// 终态落库 ctx：runCtx 超时/被取消后仍需写库（脱离取消链、保留值链）。
	finishCtx := context.WithoutCancel(runCtx)

	// 构建输入在 building 前由部署驱动备好（buildInputs 是行外内存登记表；
	// 重启丢失由 DriveBuilding 幂等重建，拾取前置检已拦无登记行——到达
	// 此分支即防御纵深）。
	in.build.inputsMu.Lock()
	input, ok := in.build.inputs[b.ID]
	in.build.inputsMu.Unlock()
	if !ok {
		// 无输入登记且无人会再登记（孤儿形态：部署已不在/输入丢失）→
		// 一跳到终态 cancelled。不得回 queued 弹跳——那正是 queued↔building
		// 无限振荡的根因（D-4）。
		if _, err := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateCancelled,
			func(m *build.Build) { m.Error = "build input lost: no registered input for this build" }); err != nil {
			in.log.Error("build: cancel without input", "build", b.ID, "err", err)
		}
		in.kickBuild()
		return
	}

	// 路由解析（ADR-0032）：input.Builder 是 Revision 冻结体的分派键；未知
	// 名（未装配的 Provider 或漂移名）→ 精确终态失败（不 panic、不弹回
	// queued 重试——重试不会让未装配的 Provider 出现）。
	builder, ok := in.builders[input.Builder]
	if !ok {
		if _, terr := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) {
				m.Error = fmt.Sprintf("builder %q is not wired on this platform (wired: %s)", input.Builder, strings.Join(sortedBuilderNames(in.builders), ", "))
			}); terr != nil {
			in.log.Error("build: transit unwired-builder failure", "build", b.ID, "err", terr)
		}
		in.kickBuild()
		return
	}

	// 出口单点（P11/ADR-0040 决策 3）：builder 的日志 writer 经
	// buildLogWriter 链——脱敏（值→指纹短形态）→ 环形缓冲（live 面）+
	// VL ingest（kind=build 持久化）。域盖戳（team/project）解析失败不
	// 阻断构建： stamps 缺席时按 build_id 仍可检索。
	var ingester *buildLogIngester
	if in.logging != nil {
		ingester = &buildLogIngester{logging: in.logging, log: in.log, buildID: b.ID}
		if a, aerr := in.repos.Apps.Get(finishCtx, b.AppID); aerr == nil {
			ingester.app = b.AppID
			ingester.project = a.ProjectID
			if team, terr := in.projectTeam(finishCtx, a.ProjectID); terr == nil {
				ingester.team = team
			}
		}
	}
	writer := &buildLogWriter{
		inner:  &bufferWriter{logs: in.build.logs, buildID: b.ID},
		redact: buildRedactionTable(&input),
		ingest: ingester,
	}
	result, err := builder.Build(runCtx, input, writer)
	if ingester != nil {
		ingester.flush(context.WithoutCancel(runCtx)) // 终批（best-effort；丢批告警不阻断终态）
	}
	switch {
	case err == nil:
		if _, terr := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateSucceeded,
			func(m *build.Build) { m.Digest = result.Digest }); terr != nil {
			in.log.Error("build: transit succeeded", "build", b.ID, "err", terr)
		}
	case isContextTimeout(runCtx, err):
		if _, terr := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateExpired,
			func(m *build.Build) { m.Error = "build timed out" }); terr != nil {
			in.log.Error("build: transit expired", "build", b.ID, "err", terr)
		}
	case runCtx.Err() != nil:
		// 进程退出路径（优雅退出）：回 queued，重启后重放。
		if _, terr := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); terr != nil {
			in.log.Error("build: reset on shutdown", "build", b.ID, "err", terr)
		}
	default:
		if _, terr := in.transitBuildRelease(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) { m.Error = err.Error() }); terr != nil {
			in.log.Error("build: transit failed", "build", b.ID, "err", terr)
		}
	}
	in.kickBuild() // 释放并发位，立即拾取下一个
}

func isContextTimeout(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded"))
}

// sortedBuilderNames 返回在册 builder 名（排序稳定，供失败文本与日志）。
func sortedBuilderNames(builders map[string]capability.Builder) []string {
	names := make([]string, 0, len(builders))
	for name := range builders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
