package main

// 状态层 lynx.Service 装配壳：把 internal/state 的零框架组件接入
// boot.Bootstrap。领域代码零框架类型依赖（D20）；CheckHealth 为结构性
// 实现 lynx.Checker——lynx 对实现该接口的服务自动收集进 readiness
// （healthz/readiness 汇总 store 与 observer 两个状态层检查器）。

import (
	"context"
	"log/slog"
	"time"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/statebackup"
)

// storeService 是状态库服务壳：迁移已在装配期（state.Open）完成，Init
// 无动作；CheckHealth 探测数据库可达。lynx run.Group actor 契约要求
// Start 阻塞到关停（立即返回会被视为首个完成者触发整组关停），故
// Start 阻塞在服务 ctx 上，Stop 语义为空。
type storeService struct {
	st *state.Store
}

func newStoreService(st *state.Store) lynx.Service { return storeService{st: st} }

func (s storeService) Name() string                 { return "state.store" }
func (s storeService) Init(_ lynx.AppContext) error { return nil }
func (s storeService) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Stop 无资源动作：连接池由 Wire cleanup 释放（OnPostStop，晚于全部
// 服务 Stop——排水期在途请求仍需读库）。
func (s storeService) Stop(_ context.Context) error { return nil }

func (s storeService) CheckHealth() error { return s.st.CheckHealth() }

// identityService 是节点身份服务壳：Init 阶段确保平台节点 ID（fail-fast，
// 身份生成失败拒绝启动），Start 阶段循环锚定 Swarm label；Start 阻塞
// 到关停（actor 契约同上），Stop 停锚定循环。
type identityService struct {
	id *state.NodeIdentity
}

func newIdentityService(id *state.NodeIdentity) lynx.Service { return identityService{id: id} }

func (s identityService) Name() string { return "state.identity" }
func (s identityService) Init(ac lynx.AppContext) error {
	_, err := s.id.EnsurePlatformID(ac.Context())
	return err
}
func (s identityService) Start(ctx context.Context) error {
	if err := s.id.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s identityService) Stop(ctx context.Context) error { return s.id.Stop(ctx) }

// CheckHealth 报告平台身份就绪（底座锚定的健康语义由 state.observer 承载）。
func (s identityService) CheckHealth() error { return s.id.CheckHealth() }

// observerService 是节点观测缓存刷新服务壳：30s 全量 resync + 事件驱动
// 失效；CheckHealth 反映底座最近一次全量同步是否成功。Start 阻塞到
// 关停（actor 契约同上）。
type observerService struct {
	ob *state.Observer
}

func newObserverService(ob *state.Observer) lynx.Service { return observerService{ob: ob} }

func (s observerService) Name() string                 { return "state.observer" }
func (s observerService) Init(_ lynx.AppContext) error { return nil }
func (s observerService) Start(ctx context.Context) error {
	if err := s.ob.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s observerService) Stop(ctx context.Context) error { return s.ob.Stop(ctx) }

func (s observerService) CheckHealth() error { return s.ob.CheckHealth() }

// janitorService 是保留期清理服务壳（事件/审计过期清理）；Start 阻塞
// 到关停（actor 契约同上）。
type janitorService struct {
	jr *state.Janitor
}

func newJanitorService(jr *state.Janitor) lynx.Service { return janitorService{jr: jr} }

func (s janitorService) Name() string                 { return "state.janitor" }
func (s janitorService) Init(_ lynx.AppContext) error { return nil }
func (s janitorService) Start(ctx context.Context) error {
	if err := s.jr.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s janitorService) Stop(ctx context.Context) error { return s.jr.Stop(ctx) }

// backupService 是状态备份服务壳（T2.22）：Start 阶段进入每日备份循环
// （启动即一拍——新装平台首启即有 verified 备份；此后每 Interval 一拍）。
// Start 阻塞到关停（actor 契约同上），Stop 等待循环退出（在途单次备份由
// Trigger 自身预算收敛）。手动与升级编排触发（RPC / upgrade.sh）不经过
// 本循环——循环只承载 daily 拍子。
type backupService struct {
	bm *statebackup.Manager
}

func newBackupService(bm *statebackup.Manager) lynx.Service { return backupService{bm: bm} }

func (s backupService) Name() string                 { return "state.backup" }
func (s backupService) Init(_ lynx.AppContext) error { return nil }
func (s backupService) Start(ctx context.Context) error {
	if err := s.bm.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s backupService) Stop(ctx context.Context) error { return s.bm.Stop(ctx) }

// secretsService 是平台密钥服务壳：主密钥已在装配期（NewSecretsBox →
// EnsureKey）fail-fast 加载/生成，Init 无动作；CheckHealth 持续上报密钥
// 就绪（readiness 汇总可见——密钥丢失/损坏是平台 env 不可解的先行指标）。
// Start 阻塞到关停（actor 契约同上），Stop 无资源动作（密钥文件句柄不
// 常驻，文件生命周期归 OS）。
type secretsService struct {
	box *secrets.Box
}

func newSecretsService(b *secrets.Box) lynx.Service { return secretsService{box: b} }

func (s secretsService) Name() string                 { return "state.secrets" }
func (s secretsService) Init(_ lynx.AppContext) error { return nil }
func (s secretsService) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
func (s secretsService) Stop(_ context.Context) error { return nil }

func (s secretsService) CheckHealth() error { return s.box.CheckHealth() }

// builderService 是构建队列服务壳（T2.8）：Start 阶段先后台预热自管
// buildkitd（预拉钉版镜像 + 收敛容器运行，失败只降级日志——构建执行前的
// ensureDaemonReady 同步收敛兜底），然后进入队列调度主循环（扫描 builds
// queued 行 + 信号量并发执行）。Start 阻塞到关停（actor 契约同上），Stop
// 无资源动作（worker 生命周期 = 服务 ctx）。
type builderService struct {
	queue   *build.Queue
	builder *build.Builder
	log     *slog.Logger
}

func newBuilderService(q *build.Queue, b *build.Builder, log *slog.Logger) lynx.Service {
	return builderService{queue: q, builder: b, log: log}
}

func (s builderService) Name() string                 { return "build.queue" }
func (s builderService) Init(_ lynx.AppContext) error { return nil }

func (s builderService) Start(ctx context.Context) error {
	s.warmDaemon()
	return s.queue.Run(ctx)
}

// Stop 无资源动作：Run 随服务 ctx 取消返回，在途构建经 ctx 排水。
func (s builderService) Stop(_ context.Context) error { return nil }

// engineService 是发布引擎服务壳（T2-5a）：Start 阶段进入引擎主循环（启动
// 扫描非终态 deployment 分类恢复 → 周期 tick 推进状态机/对账/窗口语义）。
// Start 阻塞到关停（actor 契约同上），Stop 无资源动作（循环生命周期 = 服务
// ctx；在途发布状态全部落库，控制面重启由引擎自身分类恢复）。
type engineService struct {
	eng *engine.Engine
}

func newEngineService(e *engine.Engine) lynx.Service { return engineService{eng: e} }

func (s engineService) Name() string                 { return "engine.release" }
func (s engineService) Init(_ lynx.AppContext) error { return nil }

func (s engineService) Start(ctx context.Context) error {
	return s.eng.Run(ctx)
}

// Stop 无资源动作：Run 随服务 ctx 取消返回（部署状态机持久化于 SQLite，
// 续跑语义由控制面重启恢复承载）。
func (s engineService) Stop(_ context.Context) error { return nil }

// warmDaemon 后台预热平台自管 buildkitd（预算 6 分钟——首启拉镜像受网络
// 主导）。ManageDaemon=false（外部端点形态）为 no-op。预热失败不阻塞启动、
// 不影响 readiness（构建执行前 ensureDaemonReady 同步收敛兜底）。
func (s builderService) warmDaemon() {
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if err := s.builder.EnsureDaemonReady(warmCtx); err != nil {
			s.log.Warn("buildkitd warm-up failed (first build will retry ensure)", "error", err)
		}
	}()
}
