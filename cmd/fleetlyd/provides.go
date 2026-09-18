package main

import (
	"context"

	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// ProviderSet 是 fleetlyd 的 Wire 依赖集（装配形态对齐 lynx
// _examples/boot：Bootstrap 聚合钩子与服务，cleanup 由 main 挂 OnPostStop）。
var ProviderSet = wire.NewSet(
	boot.New,
	NewConfig,
	NewStore,
	NewSubstrateClient,
	NewDockerClient,
	NewNodeIdentity,
	NewObserver,
	NewJanitor,
	NewSecretsBox,
	NewBuilder,
	NewBuildQueue,
	NewDaemonManager,
	NewGRPCServer,
	NewSystemService,
	NewHTTPServer,
	NewServices,
	NewServiceFactories,
	NewPreStarts,
	NewDrains,
	NewPreStops,
	NewPostStops,
)

// NewConfig 从应用配置（flags + 配置文件）解出 AppConfig，缺省值在此回落。
func NewConfig(app lynx.App) (*AppConfig, error) {
	c := new(AppConfig)
	if err := app.Config().Unmarshal(c); err != nil {
		return nil, err
	}
	if c.Addr == "" {
		c.Addr = defaultHTTPAddr
	}
	if c.GRPC.Addr == "" {
		c.GRPC.Addr = defaultGRPCAddr
	}
	return c, nil
}

// NewStore 打开状态库（启动即建库迁移，失败 fail-fast 拒绝启动）；资源
// 释放交给 Wire cleanup（main 挂 OnPostStop，晚于全部服务 Stop）。
func NewStore(cfg *AppConfig) (*state.Store, func(), error) {
	st, err := state.Open(context.Background(), cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return st, func() { _ = st.Close() }, nil
}

// NewSubstrateClient 构造底座适配器客户端（moby/client）。它是多个端口
// 的实现载体：state.DockerClient（节点观测/身份）与 build 包端口（镜像
// inspect/load、buildkitd 容器编排）；连接资源由 Wire cleanup 释放。
func NewSubstrateClient(cfg *AppConfig) (*substrate.Client, func(), error) {
	c, err := substrate.NewClient(cfg.State.DockerHost)
	if err != nil {
		return nil, nil, err
	}
	return c, func() { _ = c.Close() }, nil
}

// NewDockerClient 以 substrate 客户端实现 state.DockerClient 端口（连接
// 生命周期归 NewSubstrateClient 的 cleanup，此处不重复释放）。
func NewDockerClient(sc *substrate.Client) (state.DockerClient, func(), error) {
	return sc, func() {}, nil
}

// NewDaemonManager 以 substrate 客户端实现 build.DaemonManager 端口
// （buildkitd 容器编排：镜像拉取/卷/容器幂等收敛）。
func NewDaemonManager(sc *substrate.Client) build.DaemonManager {
	return sc
}

// NewNodeIdentity 构造节点身份管理器（平台节点 ID n_<ULID> 生成持久化
// 与 Swarm label 锚定，state-model §2.3）。
func NewNodeIdentity(app lynx.App, st *state.Store, dc state.DockerClient) *state.NodeIdentity {
	return state.NewNodeIdentity(st, dc, app.Logger())
}

// NewObserver 构造节点观测缓存刷新器（30s 全量 resync + 事件驱动失效，
// 底座不可达置 stale + 指数退避）。
func NewObserver(app lynx.App, st *state.Store, dc state.DockerClient) *state.Observer {
	return state.NewObserver(st, dc, app.Logger())
}

// NewJanitor 构造保留期清理守护（事件/审计过期清理，周期可配）。
func NewJanitor(app lynx.App, st *state.Store, cfg *AppConfig) *state.Janitor {
	return state.NewJanitor(st, cfg.State.EventRetentionDays, cfg.State.AuditRetentionDays, app.Logger())
}

// NewSecretsBox 初始化 envelope 主密钥（fail-fast：加载/权限/生成任一失败
// 拒绝启动——密钥是全部平台 env 密文的生命线，architecture §2.3）。首启
// 生成时日志明示妥善保存（与备份分离；丢失 = env 密文不可解）。装配期
// 完成即 fleetlyd 主密钥初始化语义；健康面由 secrets 服务壳 CheckHealth
// 持续上报。
func NewSecretsBox(app lynx.App, cfg *AppConfig) (*secrets.Box, error) {
	box, created, err := secrets.EnsureKey(cfg.KeyPath())
	if err != nil {
		return nil, err
	}
	if created {
		app.Logger().Warn("master key generated at " + box.Path() +
			" —妥善保存并与备份分离（丢失后平台 env 密文不可解）")
	}
	return box, nil
}

// NewBuilder 构建构建执行器（build.Builder：railpack/dockerfile 双驱动 +
// buildkit solve + 产物归档 + 执行前 buildkitd 就绪收敛）。镜像端口与容器
// 编排由 substrate.Client 隐式实现 build 包端口（适配器方向：
// substrate → build 核心接口）。
func NewBuilder(cfg *AppConfig, st *state.Store, dc *substrate.Client, dm build.DaemonManager, app lynx.App) *build.Builder {
	return build.NewBuilder(cfg.BuildSettings(), st, dc, dm, app.Logger())
}

// NewBuildQueue 构建构建队列调度器（信号量并发上限 + builds 行扫描认领）。
func NewBuildQueue(app lynx.App, cfg *AppConfig, st *state.Store, b *build.Builder) *build.Queue {
	settings := cfg.BuildSettings()
	return build.NewQueue(st, b, settings.Concurrency, settings.PollInterval, app.Logger())
}

// NewHTTPServer 创建控制面 HTTP 服务：根 handler 是 grpc-gateway mux
// （REST /v1/** 经 gateway 反代到本进程 gRPC，见 newGatewayMux）；
// /healthz/liveness 与 /healthz/readiness 由 lynxhttp.Server 自行挂载，
// 与 gateway 路由共存（torchwood 同款双面单端口形态）。
func NewHTTPServer(app lynx.App, cfg *AppConfig) (*lynxhttp.Server, error) {
	mux, err := newGatewayMux(grpcEndpointFromAddr(cfg.GRPCAddr()))
	if err != nil {
		return nil, err
	}
	return lynxhttp.NewServer(mux,
		lynxhttp.WithAddr(cfg.Addr),
		lynxhttp.WithHealthCheckers(app.HealthCheckers),
		lynxhttp.WithLogger(app.Logger("logger", "http-requestlog")),
	), nil
}

// NewServices 聚合全部受托管服务：状态层四服务（store/identity/observer/
// janitor）与构建队列先于 HTTP/gRPC 注册——lynx 按注册顺序启动，store 的
// Init 在装配期（Register 阶段）完成迁移，Start 阶段顺序无实质依赖，注册
// 顺序表达「状态与队列先于 API 面」。
func NewServices(
	app lynx.App,
	st *state.Store,
	id *state.NodeIdentity,
	ob *state.Observer,
	jr *state.Janitor,
	sb *secrets.Box,
	q *build.Queue,
	b *build.Builder,
	hs *lynxhttp.Server,
	gs *lynxgrpc.Server,
) []lynx.Service {
	return []lynx.Service{
		newStoreService(st),
		newIdentityService(id),
		newObserverService(ob),
		newJanitorService(jr),
		newSecretsService(sb),
		newBuilderService(q, b, app.Logger()),
		hs,
		gs,
	}
}

func NewServiceFactories() []lynx.ServiceFactory {
	return []lynx.ServiceFactory{}
}

func NewPreStarts(app lynx.App) boot.PreStartHooks {
	return boot.PreStartHooks{
		func(ctx context.Context) error {
			app.Logger().Info("fleetlyd starting")
			return nil
		},
	}
}

func NewDrains() boot.DrainHooks {
	return boot.DrainHooks{}
}

func NewPreStops(app lynx.App) boot.PreStopHooks {
	return boot.PreStopHooks{
		func(ctx context.Context) error {
			app.Logger().Info("fleetlyd stopping")
			return nil
		},
	}
}

func NewPostStops() boot.PostStopHooks {
	return boot.PostStopHooks{}
}
