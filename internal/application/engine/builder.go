package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// builds 是构建面状态（输入登记 + 最近日志缓冲；并发/超时是 Options
// 派生面，留在 Engine.opts/buildOpts）。P3 批 6 收编：buildDomain →
// builds（复数普通名词零后缀，ADR-0075 命名法则）；Engine 字段名 build
// 保持不变。P3 收官批 8 收编：类型体自 engine.go 归位 builder.go（壳收口）。
type builds struct {
	inputsMu sync.Mutex
	inputs   map[string]capability.BuildRequest // buildID → 登记输入（重启丢失即回 queued 重放）
	logs     *logBuffer
}

// 构建循环参数（Options 扩展；BuildConcurrency 上限可配，F0.9）。P3 收官
// 批 8 更名：buildOptions → buildLimits（剥与导出切片类型 BuildOptions 的
// 近撞名）；Engine 字段名 buildOpts 保持不变。
type buildLimits struct {
	Concurrency int           // 并发构建上限（默认 2）
	Timeout     time.Duration // 单次构建硬超时（默认 15m；超时=expired）
}

// buildStep 是构建循环的收敛步：拾取 queued（并发余量内）→ 起 goroutine
// 执行（per-Build 单写者：行级 CAS，goroutine 生命周期 = 该行写者）。
func (e *Engine) buildStep(ctx context.Context) {
	active, err := e.repos.Builds.ListActive(ctx)
	if err != nil {
		e.log.Error("build step: list active", "err", err)
		return
	}
	running := 0
	for _, b := range active {
		if b.State == build.StateBuilding {
			running++
		}
	}
	for _, b := range active {
		if b.State != build.StateQueued || running >= e.buildOpts.Concurrency {
			continue
		}
		// 拾取前置检（D-4）：无进程内登记的 queued 行不允许空拾取——无输入
		// 的执行只会弹跳或误终态。归它或等登记，见 buildPickable。
		if !e.buildPickable(ctx, &b) {
			continue
		}
		// CAS queued→building（四件一拍）后立即返回；执行在 goroutine（构建
		// 生命周期绑定进程而非收敛步——step ctx 取消不等构建，优雅退出由
		// executeBuild 内的 shutdown 路径回 queued 重放）。goroutine 计入
		// wg（Q-7）：Stop 取消 runCtx 即有界排水，不默等构建超时。
		fresh, err := e.transitBuild(ctx, &b,
			[]build.State{build.StateQueued}, build.StateBuilding, nil)
		if err != nil {
			e.log.Error("build step: transit", "build", b.ID, "err", err)
			continue
		}
		running++
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.executeBuild(fresh)
		}() //nolint:gosec // 构建 goroutine 生命周期即 Build 行写者（架构 §6 per-线单写者）
	}
}

// buildPickable 是 queued 行的拾取前置检（D-4）：无进程内输入登记的行
// 只有两种来路——①归属部署仍活跃（重启后待 driveBuilding 幂等重建登记，
// 本拍跳过等登记落地）；②归属部署已终态（取消/抢占后遗留的孤儿行）→
// 一跳到终态 cancelled（同步、无 goroutine），禁止拾取执行形成
// queued↔building 振荡。已登记的行正常放行。
func (e *Engine) buildPickable(ctx context.Context, b *build.Build) bool {
	e.build.inputsMu.Lock()
	_, registered := e.build.inputs[b.ID]
	e.build.inputsMu.Unlock()
	if registered {
		return true
	}
	owner, err := e.buildOwnerActive(ctx, b)
	if err != nil {
		// 归属判定失败（存储故障）：保守跳过本拍——不基于不确定的探测销毁行。
		e.log.Error("build step: probe owner", "build", b.ID, "err", err)
		return false
	}
	if owner {
		return false // 部署驱动将在 driveBuilding 内重建输入（下拍拾取）
	}
	if _, err := e.transitBuild(ctx, b,
		[]build.State{build.StateQueued}, build.StateCancelled,
		func(m *build.Build) { m.Error = "orphaned build: owning deployment is no longer active" }); err != nil {
		e.log.Error("build step: cancel orphan", "build", b.ID, "err", err)
	}
	return false
}

// buildOwnerActive 报告 Build 的归属部署是否仍活跃（同 App 且目标
// Revision 一致：该部署进入 building 态时 driveBuilding 会重建输入登记）。
func (e *Engine) buildOwnerActive(ctx context.Context, b *build.Build) (bool, error) {
	active, err := e.repos.Deployments.ActiveByApp(ctx, b.AppID)
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

// executeBuild 执行一次构建（goroutine；根 = 引擎进程生命期 runCtx，构建
// 超时独立包裹其上）。幂等重放：崩溃遗留的 building 行由 Start 重置
// queued 重跑（buildkit 缓存保证重放成本可控）。
func (e *Engine) executeBuild(b *build.Build) {
	// Q-7：根取引擎 runCtx（Start 物化；Stop 取消 → 下方 runCtx.Err() 分支
	// 优雅回 queued，排水有界），超时独立包裹（Stop 不默等 15m 看门狗）。
	runCtx, cancel := context.WithTimeout(e.buildRootCtx(), e.buildOpts.Timeout)
	defer cancel()
	// 终态落库 ctx：runCtx 超时/被取消后仍需写库（脱离取消链、保留值链）。
	finishCtx := context.WithoutCancel(runCtx)

	// 构建输入在 building 前由部署驱动备好（buildInputs 是行外内存登记表；
	// 重启丢失由 driveBuilding 幂等重建，拾取前置检已拦无登记行——到达
	// 此分支即防御纵深）。
	e.build.inputsMu.Lock()
	input, ok := e.build.inputs[b.ID]
	e.build.inputsMu.Unlock()
	if !ok {
		// 无输入登记且无人会再登记（孤儿形态：部署已不在/输入丢失）→
		// 一跳到终态 cancelled。不得回 queued 弹跳——那正是 queued↔building
		// 无限振荡的根因（D-4）。
		if _, err := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateCancelled,
			func(m *build.Build) { m.Error = "build input lost: no registered input for this build" }); err != nil {
			e.log.Error("build: cancel without input", "build", b.ID, "err", err)
		}
		e.buildLoop.Kick()
		return
	}

	// 路由解析（ADR-0032）：input.Builder 是 Revision 冻结体的分派键；未知
	// 名（未装配的 Provider 或漂移名）→ 精确终态失败（不 panic、不弹回
	// queued 重试——重试不会让未装配的 Provider 出现）。
	builder, ok := e.builders[input.Builder]
	if !ok {
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) {
				m.Error = fmt.Sprintf("builder %q is not wired on this platform (wired: %s)", input.Builder, strings.Join(sortedBuilderNames(e.builders), ", "))
			}); terr != nil {
			e.log.Error("build: transit unwired-builder failure", "build", b.ID, "err", terr)
		}
		e.buildLoop.Kick()
		return
	}

	// 出口单点（P11/ADR-0040 决策 3）：builder 的日志 writer 经
	// buildLogWriter 链——脱敏（值→指纹短形态）→ 环形缓冲（live 面）+
	// VL ingest（kind=build 持久化）。域盖戳（team/project）解析失败不
	// 阻断构建： stamps 缺席时按 build_id 仍可检索。
	var ingester *buildLogIngester
	if e.logging != nil {
		ingester = &buildLogIngester{logging: e.logging, log: e.log, buildID: b.ID}
		if a, aerr := e.repos.Apps.Get(finishCtx, b.AppID); aerr == nil {
			ingester.app = b.AppID
			ingester.project = a.ProjectID
			if team, terr := e.projectTeam(finishCtx, a.ProjectID); terr == nil {
				ingester.team = team
			}
		}
	}
	writer := &buildLogWriter{
		inner:  &bufferWriter{logs: e.build.logs, buildID: b.ID},
		redact: buildRedactionTable(&input),
		ingest: ingester,
	}
	result, err := builder.Build(runCtx, input, writer)
	if ingester != nil {
		ingester.flush(context.WithoutCancel(runCtx)) // 终批（best-effort；丢批告警不阻断终态）
	}
	switch {
	case err == nil:
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateSucceeded,
			func(m *build.Build) { m.Digest = result.Digest }); terr != nil {
			e.log.Error("build: transit succeeded", "build", b.ID, "err", terr)
		}
	case isContextTimeout(runCtx, err):
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateExpired,
			func(m *build.Build) { m.Error = "build timed out" }); terr != nil {
			e.log.Error("build: transit expired", "build", b.ID, "err", terr)
		}
	case runCtx.Err() != nil:
		// 进程退出路径（优雅退出）：回 queued，重启后重放。
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); terr != nil {
			e.log.Error("build: reset on shutdown", "build", b.ID, "err", terr)
		}
	default:
		if _, terr := e.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) { m.Error = err.Error() }); terr != nil {
			e.log.Error("build: transit failed", "build", b.ID, "err", terr)
		}
	}
	e.buildLoop.Kick() // 释放并发位，立即拾取下一个
}

// transitBuild（Build 四件一）已收口至 transition.go。

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
