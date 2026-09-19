package main

import (
	"context"
	"net"
	"net/http"

	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/gitserver"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/logs"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/statebackup"
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
	NewBackupManager,
	NewBuilder,
	NewBuildQueue,
	NewPlacementResolver,
	NewIngressManager,
	NewEngine,
	NewDaemonManager,
	NewLogsManager,
	NewAuthenticator,
	NewSystemService,
	NewAppsService,
	NewDeploymentsService,
	NewRevisionsService,
	NewBuildsService,
	NewDriftService,
	NewDomainsService,
	NewEnvService,
	NewLogsService,
	NewEventsService,
	NewPlacementService,
	NewTokensService,
	NewGitTriggers,
	NewGitKeysService,
	NewGRPCServer,
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
// A7/A10（S18）扩展：部署 compose 目录 30 天窗、builds 终态行 90 天、
// artifacts 目录 30 天保留窗与非终态超龄扫描（预算从 engine/build 配置
// 派生：2×（发布看门狗+观察窗）/ 2×构建超时）。
func NewJanitor(app lynx.App, st *state.Store, cfg *AppConfig) *state.Janitor {
	engineCfg := cfg.EngineSettings().Normalize()
	buildCfg := cfg.BuildSettings()
	return state.NewJanitor(st, state.JanitorConfig{
		EventRetentionDays:     cfg.State.EventRetentionDays,
		AuditRetentionDays:     cfg.State.AuditRetentionDays,
		BuildRetentionDays:     cfg.State.BuildRetentionDays,
		ArtifactsDir:           buildCfg.ArtifactsDir,
		ArtifactsRetentionDays: cfg.Build.ArtifactsRetentionDays,
		DeploymentsRoot:        cfg.DeploymentsRoot(),
		// 部署目录 30 天窗取注册默认（DeploymentDirRetentionDays 零值回落）。
		StaleDeploymentBudget: 2 * (engineCfg.ReleaseTimeout + engineCfg.ObserveWindow),
		StaleBuildBudget:      2 * buildCfg.Timeout,
	}, app.Logger())
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

// NewBackupManager 构建状态备份管理器（T2.22：热备快照 + 回读校验 +
// manifest/台账 + 每日循环；backup.* 配置节，dir 缺省回落数据根下 backups/
// ——主密钥分离性由构造期 fail-fast 守卫）。
func NewBackupManager(app lynx.App, cfg *AppConfig, st *state.Store, sb *secrets.Box) (*statebackup.Manager, error) {
	return statebackup.NewManager(cfg.BackupSettings(), cfg.BackupRoot(), sb.Path(),
		version, st, app.Logger())
}

// NewBuildQueue 构建构建队列调度器（信号量并发上限 + builds 行扫描认领 +
// per-build 超时预算 + 启动复位中断构建）。
func NewBuildQueue(app lynx.App, cfg *AppConfig, st *state.Store, b *build.Builder) *build.Queue {
	settings := cfg.BuildSettings()
	return build.NewQueue(st, b, settings.Concurrency, settings.PollInterval, settings.Timeout, app.Logger())
}

// NewPlacementResolver 构造放置解析器（放置意图解析/绑定落库/卷登记/
// 部署前哨；引擎 preparing 与 releasing 全程消费）。
func NewPlacementResolver(st *state.Store, dc state.DockerClient) *placement.Resolver {
	return placement.NewResolver(st, dc)
}

// NewIngressManager 构建入口/证书管理器（T2.15/T2.16：Traefik 部署 +
// 配置端点 + 集中 ACME；ingress.* 配置节，缺省回落 internal/ingress）。
// docker 连接经 manager cleanup 释放（Wire cleanup，OnPostStop 时点）。
func NewIngressManager(app lynx.App, cfg *AppConfig, st *state.Store) (*ingress.Manager, func(), error) {
	return ingress.NewManager(cfg.IngressSettings(), st, app.Logger())
}

// ingressPublisher 是 engine.RoutePublisher 的载荷转换适配器：引擎侧
// RoutePublishInput（核心类型）→ ingress.PublishInput（适配器类型）。
// 方向纪律：核心不感知适配器类型，转换只在此处。
type ingressPublisher struct {
	m *ingress.Manager
}

func (p ingressPublisher) PublishRoutes(ctx context.Context, in engine.RoutePublishInput) error {
	out := ingress.PublishInput{AppID: in.AppID, AppName: in.AppName}
	for _, svc := range in.Services {
		out.Services = append(out.Services, ingress.ServiceRoutes{
			Service: svc.Service, Port: svc.Port, Domains: svc.Domains,
		})
	}
	return p.m.PublishRoutes(ctx, out)
}

// NewEngine 构建发布引擎（T2-5a：状态机/对账/窗口语义；治理参数取 engine.*
// 配置节，缺省回落文档默认）。底座服务/任务面由 substrate.Client 隐式实现
// engine.Substrate + engine.ImageChecker（适配器方向：substrate → engine
// 核心接口）；路由发布端口由 ingress.Manager 经载荷适配实现（T2.15——
// 健康门后挂点）。
func NewEngine(app lynx.App, cfg *AppConfig, st *state.Store, sc *substrate.Client, pl *placement.Resolver, box *secrets.Box, m *ingress.Manager, bm *statebackup.Manager) *engine.Engine {
	return engine.NewEngine(cfg.EngineSettings(), st, sc, sc, pl, box, app.Logger()).
		WithRoutePublisher(ingressPublisher{m: m}).
		// 备份挂钩（T2.22）：每次部署成功后异步触发一次热备快照
		// （kind=post_deploy；失败只落台账/审计/组件三面红，不影响部署）。
		WithPostDeployHook(bm.RunPostDeploy)
}

// NewLogsManager 构建日志管线管理器（T2.20：采集/Follow/History/清理；
// logs.* 配置节，缺省回落 internal/logs）。底座端口由 substrate.Client
// 隐式实现 logs.Port（适配器方向：substrate → logs 核心接口）。B3：注入
// git 触发面为补充脱敏值集供给（钩子 token 明文只在钩子文件）。
func NewLogsManager(app lynx.App, cfg *AppConfig, st *state.Store, sc *substrate.Client, sb *secrets.Box, src *gitserver.GitTriggers) *logs.Manager {
	return logs.NewManager(cfg.LogsSettings(), st, sc, sb, app.Logger()).
		WithSecretSource(src)
}

// NewAppsService 构造应用资源面服务（T2.17；T2.19 增补 webhook/git 触发
// 配置面——box 加密 webhook secret 与拉源认证材料，gitEndpoint 拼 remote
// 提示；H9 增补路由撤销端口——app 删除管线经 ingress.Manager 撤销路由）。
func NewAppsService(st *state.Store, sb *secrets.Box, cfg *AppConfig, m *ingress.Manager) *api.AppsService {
	return api.NewAppsService(st, sb, gitEndpointForHint(cfg.GitSettings().Addr), m)
}

// gitEndpointForHint 把 SSH 监听地址归一为 remote 提示的 host:port
// （host 位通配/空回落 127.0.0.1——提示面永不输出空 host 形态）。
func gitEndpointForHint(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "127.0.0.1:8424"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// NewDeploymentsService 构造部署资源面服务（T2.17；T2.19 增补 DeployFromGit
// ——git 源端口由 internal/gitserver 实现，方向纪律：api 定义端口）。
func NewDeploymentsService(st *state.Store, src *gitserver.GitTriggers) *api.DeploymentsService {
	return api.NewDeploymentsService(st, src)
}

// NewGitTriggers 构建 git 触发入口核心（T2.19：bare 仓库管理 + post-receive
// 钩子 + DeployFromCommit + webhook 验签/防重放/去重/拉源 + SSH 服务器）。
// git.*/webhook.* 配置节经 AppConfig.GitSettings 翻译（缺省回落
// internal/gitserver 单一事实源）。
func NewGitTriggers(cfg *AppConfig, st *state.Store, sb *secrets.Box, app lynx.App) *gitserver.GitTriggers {
	return gitserver.NewGitTriggers(cfg.GitSettings(), st, sb, app.Logger())
}

// NewGitKeysService 构造 git 公钥管理面服务（T2.19；admin scope）。
func NewGitKeysService(st *state.Store) *api.GitKeysService {
	return api.NewGitKeysService(st)
}

// NewRevisionsService 构造版本快照只读面服务。
func NewRevisionsService(st *state.Store) *api.RevisionsService {
	return api.NewRevisionsService(st)
}

// NewBuildsService 构造构建资源面服务（T2.18；A11/S18 增补队列接线——
// TriggerBuild 经 Queue.Enqueue 入队，同进程触发立即唤醒扫描）。
func NewBuildsService(st *state.Store, q *build.Queue) *api.BuildsService {
	return api.NewBuildsService(st, q)
}

// NewDriftService 构造漂移面服务（T2.18；复用引擎对账原语）。
func NewDriftService(st *state.Store, eng *engine.Engine) *api.DriftService {
	return api.NewDriftService(st, eng)
}

// NewSystemService 构造系统/集群观察面服务（T2.18 起 SystemService 实现在
// internal/api；健康组件集在本装配点命名——lynx Checker 接口无名。Traefik
// 是降级设计（收敛/续期失败只日志告警，ingress 服务壳 CheckHealth 恒健康）
// ——组件集与装配壳同语义如实上报恒健康）。备份组件（T2.22）的检查器 =
// statebackup.Manager.CheckHealth：无 verified 备份 / 最近一次 verify 失败
// → 不健康（红色告警面：台账 failed 行 + backup.failed 审计 + 此组件）。
func NewSystemService(st *state.Store, id *state.NodeIdentity, ob *state.Observer, sb *secrets.Box, ing *ingress.Manager, bm *statebackup.Manager) *api.SystemService {
	components := func() []api.SystemComponent {
		return []api.SystemComponent{
			{Name: "state.store", Check: st.CheckHealth},
			{Name: "state.identity", Check: id.CheckHealth},
			{Name: "state.observer", Check: ob.CheckHealth},
			{Name: "state.secrets", Check: sb.CheckHealth},
			{Name: "state.backup", Check: bm.CheckHealth},
			{Name: "ingress.traefik", Check: func() error { return nil }},
		}
	}
	return api.NewSystemService(version, st, components, ing).WithBackupManager(bm)
}

// NewDomainsService 构造域名台账/验证面服务。
func NewDomainsService(st *state.Store, m *ingress.Manager) *api.DomainsService {
	return api.NewDomainsService(st, m)
}

// NewEnvService 构造平台 env 面服务（值加密边界在服务实现内）。
func NewEnvService(st *state.Store, sb *secrets.Box) *api.EnvService {
	return api.NewEnvService(st, sb)
}

// NewLogsService 构造日志面服务（T2.20：Follow/History 接管线管理器）。
func NewLogsService(st *state.Store, mg *logs.Manager) *api.LogsService {
	return api.NewLogsService(st, mg)
}

// NewEventsService 构造事件流面服务（seq 游标）。
func NewEventsService(st *state.Store) *api.EventsService {
	return api.NewEventsService(st)
}

// NewPlacementService 构造放置绑定只读面服务。
func NewPlacementService(st *state.Store) *api.PlacementService {
	return api.NewPlacementService(st)
}

// NewTokensService 构造 token 管理面服务。
func NewTokensService(st *state.Store) *api.TokensService {
	return api.NewTokensService(st)
}

// NewHTTPServer 创建控制面 HTTP 服务：根 handler 是 grpc-gateway mux
// （REST /v1/** 经 gateway 反代到本进程 gRPC，见 newGatewayMux）外包原生
// 端点分派（newRootHandler——webhook 与 Console /ui/ 静态托管，例外清单
// 见 gateway.go）；/healthz/liveness 与 /healthz/readiness 由 lynxhttp.
// Server 自行挂载，与 gateway 路由共存（torchwood 同款双面单端口形态）。
// Console 静态托管仅在 console.static_dir 非空时挂载（缺省关闭），目录缺
// index.html 时 fail-fast 拒绝启动。
func NewHTTPServer(app lynx.App, cfg *AppConfig, src *gitserver.GitTriggers) (*lynxhttp.Server, error) {
	mux, err := newGatewayMux(grpcEndpointFromAddr(cfg.GRPCAddr()))
	if err != nil {
		return nil, err
	}
	var consoleUI http.Handler
	if dir := cfg.Console.StaticDir; dir != "" {
		consoleUI, err = newConsoleUIHandler(dir)
		if err != nil {
			return nil, err
		}
	}
	root := newRootHandler(gitserver.NewWebhookHandler(src), consoleUI, mux)
	return lynxhttp.NewServer(root,
		lynxhttp.WithAddr(cfg.Addr),
		lynxhttp.WithHealthCheckers(app.HealthCheckers),
		lynxhttp.WithLogger(app.Logger("logger", "http-requestlog")),
	), nil
}

// NewServices 聚合全部受托管服务：状态层五服务（store/identity/observer/
// janitor/backup）、构建队列、发布引擎、入口与日志采集服务先于 HTTP/gRPC
// 注册——lynx 按注册顺序启动，store 的 Init 在装配期（Register 阶段）完成
// 迁移，Start 阶段顺序无实质依赖，注册顺序表达「状态与队列、引擎、入口、
// 日志采集先于 API 面」（ingress 在 engine 之后：引擎 tick 触发发布时
// 配置端点已监听）。
func NewServices(
	app lynx.App,
	st *state.Store,
	id *state.NodeIdentity,
	ob *state.Observer,
	jr *state.Janitor,
	bm *statebackup.Manager,
	sb *secrets.Box,
	q *build.Queue,
	b *build.Builder,
	eng *engine.Engine,
	ing *ingress.Manager,
	lm *logs.Manager,
	src *gitserver.GitTriggers,
	cfg *AppConfig,
	hs *lynxhttp.Server,
	gs *lynxgrpc.Server,
) []lynx.Service {
	return []lynx.Service{
		newStoreService(st),
		newIdentityService(id),
		newObserverService(ob),
		newJanitorService(jr),
		newBackupService(bm),
		newSecretsService(sb),
		newBuilderService(q, b, app.Logger()),
		newEngineService(eng),
		newIngressService(ing, app, cfg.IngressSettings().ConfigAddr),
		newLogsService(lm),
		newGitService(src, app, cfg.GitSettings().Enabled, cfg.GitSettings().Addr),
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
