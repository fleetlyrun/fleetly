package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
)

// builds 是构建面状态（输入登记 + 最近日志缓冲）。P3 批 6 收编：
// buildDomain → builds（复数普通名词零后缀，ADR-0075 命名法则）；P3 收官
// 批 8 收编：类型体自 engine.go 归位 builder.go（壳收口）。P4 wave-2b：
// 作为 intake 卫星的嵌套状态 struct 逐字保留（名与字段零改）——Engine 侧
// e.build 选择子经卫星裸嵌入提升继续可达（transition.go 冻结边，见 intake
// 注），核/测试消费点零改。
type builds struct {
	inputsMu sync.Mutex
	inputs   map[string]capability.BuildRequest // buildID → 登记输入（重启丢失即回 queued 重放）
	logs     *logBuffer
}

// 构建循环参数（Options 扩展；BuildConcurrency 上限可配，F0.9）。P3 收官
// 批 8 更名：buildOptions → buildLimits（剥与导出切片类型 BuildOptions 的
// 近撞名）。P4 wave-2b：字段随 intake 卫星（名保持 buildOpts）。
type buildLimits struct {
	Concurrency int           // 并发构建上限（默认 2）
	Timeout     time.Duration // 单次构建硬超时（默认 15m；超时=expired）
}

// intake 是构建摄取域卫星（git 材料/上传解包/构建循环/Registry 内容代理
// ——设计稿 §3 六文件：builder/buildsource/buildlog/gitmaterial/materials/
// registrycontent）。P4 wave-2b 嵌入改形（ADR-0088 决策 2/3）：接收者已
// 下域 *intake（域内短名 in），方法经嵌入提升（buildStep 仍在环表、
// gRPC Registry/Clone/git-head 面、drive 的 driveBuilding 与 materialize
// 的材料解析/卷钉住面、lifecycle 的 resetOrphanBuilds 与 transition 的
// 终态释放零改）；依赖显式逐字段 + 机制端口注入（construct.go 装配）。
// 禁挂 *Engine 回引（enginebackref 守卫执法）。
//
// 嵌套形态裁决（撞名先例的对偶面）：状态 struct 名 builds 与 Engine 字段
// 名 build 不同名（其余卫星两者同名），直接裸嵌入 builds 会把字段名改写
// 成 builds——transition.go:443 的 e.build.logs（冻结面持有的卫星边，P5
// 裁决表在册）与测试 seeding 面全部要动。故卫星名取域名 intake、状态以
// 命名字段 build builds 内嵌：e.build 经二级提升零改可达（冻结面零改是
// 本波验收锚），方法提升走卫星层不受影响。
type intake struct {
	build builds // 既有构建面状态（逐字保留，e.build 经提升可达——见上注）

	// 域自有事实（自 Engine 迁入，单一真源随域）：builders 是 Builder 家族
	//（名→Provider，spec 路由；lifecycle 的 resetOrphanBuilds 与本域消费
	// 经提升读）；buildOpts 是并发/超时上限（Options 派生快照）；ghClient
	// 是 GitHub App 最小客户端（installation token 现铸，gitmaterial 消费
	// ——infrastructure/github 分层豁免随卫星文件留 engine 包，ADR-0088
	// 决策 8）。
	builders  map[string]capability.Builder
	buildOpts buildLimits
	ghClient  *github.Client

	// 显式依赖（实测消费面，逐字段注入）：registry 是受管仓库端点/内容
	// 面与 per-Project 凭证面（核副本双持——drive 的前置门与 component-
	// restart 的 providers 消费留核，P5 过渡形态通则二）；logging 是 build
	// 日志持久化面（核副本双持，同通则二）；cipher 是 Secret/Git 材料解封
	//（核副本双持——resolve.go 凭证解析族留核）；runtime 是卷钉住的首节
	// 点探测（DescribeCluster 读动词，非载体写——maintenanceguard 不执法
	// 读半边）；repos 是行读写聚合目录；tx 是 Build 行铸造事务入口；log
	// 是域内日志器。
	registry capability.Registry
	logging  capability.Logging
	cipher   *material.Cipher
	runtime  capability.Runtime
	repos    transact.Stores
	tx       transact.Tx
	log      *slog.Logger

	// dataRoot 是数据根配置快照端口（通则五标准子形态：闭包逐拍读核
	// opts——测试经 opts 写驱动上下文目录行为不冻结）。
	dataRoot func() string

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// rootCtx 是构建 goroutine 派生根（六条例外边之一——lifecycle 派生
	// 根机制，logpipes/metrics 同款端口）；spawn 是 wg Add/Done 配对排水
	//（单源留核，卫星结构性无法破坏排水纪律）；transitBuild/transit-
	// AndReload/commitWrite 是 writer 端口（Build 四件一与部署状态机受控
	// 暴露，前缀判据零漂移）；failDeployment/loadSpec 是受理线例外边的
	// 端口归位（设计稿 §4 六条之二，真源留核）；eventTeamOfProject 是
	// emitter 端口（事实 Team 盖戳）；projectTeam 与 registry 端点四函数
	//（registryEndpoint/registryProjectEndpoints/registryEndpointFor/
	// managedEndpoint）是 facts 端口（解析面六族住 resolve.go，
	// resolutionface 锚扩条）；kickBuild 是拾取唤醒（Loop 挂载留核）。
	rootCtx                  func() context.Context
	spawn                    func(fn func())
	transitBuild             func(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error)
	transitAndReload         func(ctx context.Context, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) (*deployment.Deployment, error)
	failDeployment           func(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error)
	loadSpec                 func(ctx context.Context, revID string) (*specv1.AppSpec, error)
	commitWrite              func(ctx context.Context, s transact.Stores, f WriteFact) error
	eventTeamOfProject       func(ctx context.Context, s transact.Stores, projectID string) string
	projectTeam              func(ctx context.Context, projectID string) (string, error)
	registryEndpoint         func(ctx context.Context) (capability.RegistryEndpoint, error)
	registryProjectEndpoints func() capability.ProjectEndpoints
	registryEndpointFor      func(ctx context.Context, projectID string) (capability.RegistryEndpoint, error)
	managedEndpoint          func(ctx context.Context) (capability.RegistryEndpoint, error)
	kickBuild                func()
}

// buildStep 是构建循环的收敛步：拾取 queued（并发余量内）→ 起 goroutine
// 执行（per-Build 单写者：行级 CAS，goroutine 生命周期 = 该行写者）。
// P4 wave-2b：接收者下域 *intake；构建 goroutine 经 spawn 端口计入 wg
// （Add/Done 配对单源留核，logpipes/metrics 同款）。
func (in *intake) buildStep(ctx context.Context) {
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
		fresh, err := in.transitBuild(ctx, &b,
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
// 只有两种来路——①归属部署仍活跃（重启后待 driveBuilding 幂等重建登记，
// 本拍跳过等登记落地）；②归属部署已终态（取消/抢占后遗留的孤儿行）→
// 一跳到终态 cancelled（同步、无 goroutine），禁止拾取执行形成
// queued↔building 振荡。已登记的行正常放行。
func (in *intake) buildPickable(ctx context.Context, b *build.Build) bool {
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
		return false // 部署驱动将在 driveBuilding 内重建输入（下拍拾取）
	}
	if _, err := in.transitBuild(ctx, b,
		[]build.State{build.StateQueued}, build.StateCancelled,
		func(m *build.Build) { m.Error = "orphaned build: owning deployment is no longer active" }); err != nil {
		in.log.Error("build step: cancel orphan", "build", b.ID, "err", err)
	}
	return false
}

// buildOwnerActive 报告 Build 的归属部署是否仍活跃（同 App 且目标
// Revision 一致：该部署进入 building 态时 driveBuilding 会重建输入登记）。
func (in *intake) buildOwnerActive(ctx context.Context, b *build.Build) (bool, error) {
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
// buildsource.go；transition 终态收口经提升调用）。输入含推送凭证明文，
// 终态后不得常驻内存（R03-M1）。
func (in *intake) forgetBuildInput(buildID string) {
	in.build.inputsMu.Lock()
	delete(in.build.inputs, buildID)
	in.build.inputsMu.Unlock()
}

// resetOrphanBuilds 把崩溃遗留的 building 行重置 queued（重放：buildkit
// 缓存幂等）。输入登记不在此重建——driveBuilding 见到在途行时幂等重建
// （D-4）；归属部署已终态的孤儿行由 buildStep 前置检一跳到终态。
// P4 wave-2b：自 lifecycle.go 迁入本域（Start 的调用点经嵌入提升零改，
// sweepInterruptedBackups 先例）。
func (in *intake) resetOrphanBuilds(ctx context.Context) {
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
		if _, err := in.transitBuild(ctx, &b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil {
			in.log.Error("build: reset orphan", "build", b.ID, "err", err)
		}
	}
}

// executeBuild 执行一次构建（goroutine；根 = 引擎进程生命期 runCtx，构建
// 超时独立包裹其上）。幂等重放：崩溃遗留的 building 行由 Start 重置
// queued 重跑（buildkit 缓存保证重放成本可控）。
func (in *intake) executeBuild(b *build.Build) {
	// Q-7：根取引擎 runCtx（Start 物化；Stop 取消 → 下方 runCtx.Err() 分支
	// 优雅回 queued，排水有界），超时独立包裹（Stop 不默等 15m 看门狗）。
	runCtx, cancel := context.WithTimeout(in.rootCtx(), in.buildOpts.Timeout)
	defer cancel()
	// 终态落库 ctx：runCtx 超时/被取消后仍需写库（脱离取消链、保留值链）。
	finishCtx := context.WithoutCancel(runCtx)

	// 构建输入在 building 前由部署驱动备好（buildInputs 是行外内存登记表；
	// 重启丢失由 driveBuilding 幂等重建，拾取前置检已拦无登记行——到达
	// 此分支即防御纵深）。
	in.build.inputsMu.Lock()
	input, ok := in.build.inputs[b.ID]
	in.build.inputsMu.Unlock()
	if !ok {
		// 无输入登记且无人会再登记（孤儿形态：部署已不在/输入丢失）→
		// 一跳到终态 cancelled。不得回 queued 弹跳——那正是 queued↔building
		// 无限振荡的根因（D-4）。
		if _, err := in.transitBuild(finishCtx, b,
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
		if _, terr := in.transitBuild(finishCtx, b,
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
		if _, terr := in.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateSucceeded,
			func(m *build.Build) { m.Digest = result.Digest }); terr != nil {
			in.log.Error("build: transit succeeded", "build", b.ID, "err", terr)
		}
	case isContextTimeout(runCtx, err):
		if _, terr := in.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateExpired,
			func(m *build.Build) { m.Error = "build timed out" }); terr != nil {
			in.log.Error("build: transit expired", "build", b.ID, "err", terr)
		}
	case runCtx.Err() != nil:
		// 进程退出路径（优雅退出）：回 queued，重启后重放。
		if _, terr := in.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); terr != nil {
			in.log.Error("build: reset on shutdown", "build", b.ID, "err", terr)
		}
	default:
		if _, terr := in.transitBuild(finishCtx, b,
			[]build.State{build.StateBuilding}, build.StateFailed,
			func(m *build.Build) { m.Error = err.Error() }); terr != nil {
			in.log.Error("build: transit failed", "build", b.ID, "err", terr)
		}
	}
	in.kickBuild() // 释放并发位，立即拾取下一个
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
