package assembly

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/wire"
	"github.com/lynx-go/grpcapi/policy"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	"github.com/lynx-go/lynx/buildinfo"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/idem"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	alertrulerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/alertrule"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	appvariablerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/appvariable"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	backuprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/backup"
	browserepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/browse"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	catalogrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/catalog"
	channelrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/channel"
	cfgrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/config"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	freezerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/freeze"
	githubapprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	hookrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/hook"
	idemrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/idempotency"
	invitationrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/invitation"
	logcursorrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/logcursor"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/membership"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	networkpeerrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/networkpeer"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	outbox "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	revisionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	routerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/route"
	runrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/run"
	schedulerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/schedule"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	sharedvariablerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sharedvariable"
	sourcerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sourceupload"
	taskrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/task"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/token"
	userrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/user"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/upload"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/systemgrpc"
)

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// ProviderSet 是 fleetlyd 的完整依赖图。生命周期 provider（hooks）随批次
// 扩展：store 迁移（PreStart）、Provider reconcile（Drain 前）、引擎排空
// （PreStop）等按 ADR-0005 语义逐批挂入。
var ProviderSet = wire.NewSet(
	boot.New,
	NewAppConfig,
	NewPolicySet,
	NewStateDB,
	NewBinder,
	NewStores,
	NewTx,
	NewAuthnRepos,
	NewSeedRepos,
	NewAuthenticator,
	NewRuntimeProvider,
	NewBuilderProviders,
	NewProxyProvider,
	NewRegistryProvider,
	NewLoggingProvider,
	NewMetricsProvider,
	NewObjectStore,
	NewMaterialCipher,
	NewEngine,
	NewIdemEnforcer,
	NewRateLimiter,
	NewFreezeGuard,
	NewAPIServices,
	NewEngineService,
	NewRetentionJanitorService,
	NewProxyConfigServer,
	NewHealthCheckers,
	systemgrpc.New,
	NewGRPCServer,
	NewGatewayServer,
	NewServices,
	NewServiceFactories,
	NewPreStartHooks,
	NewDrainHooks,
	NewPreStopHooks,
	NewPostStopHooks,
)

// NewBinder 造事务作用域绑定闭包（ADR-0080）：组合根是唯一知道聚合 repo
// 构造形态的地方——每次事务以 tx 执行面重造全套绑定实例（Stores 全字段
// 齐备是有意的：调用点按需取用，不按需装配）。
func NewBinder(db *persistence.DB) transact.Binder {
	clock := db.Clock()
	return func(run state.Runner) transact.Stores {
		return transact.Stores{
			Anchor: anchor.New(anchorRepos(clock, run)),
			NextSeq: func(ctx context.Context, table string) (int64, error) {
				return persistence.NextSeq(ctx, run, table)
			},
			Projects:        projectrepo.New(clock, run),
			Apps:            apprepo.New(clock, run),
			Revisions:       revisionrepo.New(clock, run),
			Deployments:     deploymentrepo.New(clock, run),
			Builds:          buildrepo.New(clock, run),
			Outbox:          outbox.New(clock, run),
			Nodes:           noderepo.New(clock, run),
			Audits:          auditrepo.New(clock, run),
			Tasks:           taskrepo.New(clock, run),
			Runs:            runrepo.New(clock, run),
			Tokens:          tokenrepo.New(clock, run),
			Schedules:       schedulerepo.New(clock, run),
			NetworkPeers:    networkpeerrepo.New(clock, run),
			SourceUploads:   sourcerepo.New(clock, run),
			Browse:          browserepo.New(clock, run),
			Databases:       dbrepo.New(clock, run),
			Backups:         backuprepo.New(clock, run),
			LogCursors:      logcursorrepo.New(clock, run),
			AlertRules:      alertrulerepo.New(clock, run),
			Channels:        channelrepo.New(clock, run),
			Routes:          routerepo.New(clock, run),
			Secrets:         secretrepo.New(clock, run),
			Configs:         cfgrepo.New(clock, run),
			Volumes:         volumerepo.New(clock, run),
			Networks:        networkrepo.New(clock, run),
			GitHubApps:      githubapprepo.New(clock, run),
			SharedVariables: sharedvariablerepo.New(clock, run),
			AppVariables:    appvariablerepo.New(clock, run),
			Teams:           teamrepo.New(clock, run),
			Users:           userrepo.New(clock, run),
			Roles:           rolerepo.New(clock, run),
			Memberships:     membershiprepo.New(clock, run),
			Invitations:     invitationrepo.New(clock, run),
			Freezes:         freezerepo.New(clock, run),
			Hooks:           hookrepo.New(clock, run),
			Catalog:         catalogrepo.New(clock, run),
			Idempotency:     idemrepo.New(clock, run),
		}
	}
}

// NewStores 造聚合目录的 db 绑定形态（读径直调；CQRS-lite，ADR-0075）。
func NewStores(db *persistence.DB, bind transact.Binder) transact.Stores {
	return bind(db.Runner())
}

// NewTx 装配事务入口（db + Binder 双注入，唯一事务门面）。
func NewTx(db *persistence.DB, bind transact.Binder) transact.Tx {
	return transact.NewTx(db, bind)
}

// NewAuthnRepos 构造身份执法面的 repo 集（P1b C 批构造上提）。
func NewAuthnRepos(db *persistence.DB) authn.Repos {
	clock := db.Clock()
	return authn.Repos{
		Roles:       rolerepo.New(clock, db.Runner()),
		Users:       userrepo.New(clock, db.Runner()),
		Tokens:      tokenrepo.New(clock, db.Runner()),
		Memberships: membershiprepo.New(clock, db.Runner()),
		Audits:      auditrepo.New(clock, db.Runner()),
	}
}

// NewSeedRepos 构造种子与 Bootstrap Token 首启面的 repo 集（P1b C 批
// 构造上提）。
func NewSeedRepos(db *persistence.DB) authn.SeedRepos {
	clock := db.Clock()
	return authn.SeedRepos{
		Teams:  teamrepo.New(clock, db.Runner()),
		Roles:  rolerepo.New(clock, db.Runner()),
		Tokens: tokenrepo.New(clock, db.Runner()),
		Users:  userrepo.New(clock, db.Runner()),
	}
}

// anchorRepos 装配「聚合行 → Project → Team」归属解析图的 repo 面（db
// 绑定与事务绑定两形态共用同一清单）。
func anchorRepos(clock state.Clock, run state.Runner) anchor.Repos {
	return anchor.Repos{
		Projects:    projectrepo.New(clock, run),
		Apps:        apprepo.New(clock, run),
		Tasks:       taskrepo.New(clock, run),
		Schedules:   schedulerepo.New(clock, run),
		Networks:    networkrepo.New(clock, run),
		Peers:       networkpeerrepo.New(clock, run),
		Hooks:       hookrepo.New(clock, run),
		Routes:      routerepo.New(clock, run),
		Databases:   dbrepo.New(clock, run),
		Volumes:     volumerepo.New(clock, run),
		Runs:        runrepo.New(clock, run),
		Deployments: deploymentrepo.New(clock, run),
		Builds:      buildrepo.New(clock, run),
		GitHubApps:  githubapprepo.New(clock, run),
	}
}

// NewAnchor 构造 db 绑定形态的归属解析图（freeze 与行级授权共用单源）。
func NewAnchor(db *persistence.DB) *anchor.Anchor {
	return anchor.New(anchorRepos(db.Clock(), db.Runner()))
}

// NewAuthenticator 构造身份执法器（authn 拦截器依赖集；词表单一源注入；
// 策略集构造期 ViewRules 物化，非规范树形即装配失败——fail-closed）。
func NewAuthenticator(db *persistence.DB, repos authn.Repos, policySet *policy.Set, app lynx.App) (*authn.Authenticator, error) {
	return authn.NewAuthenticator(db, repos, policySet, ScopeResources(), app.Logger())
}

// NewStateDB 打开数据根下的控制面库（WAL + goose 前滚迁移在 Open 内完成；
// 关闭经 wire cleanup 聚合到 OnPostStop）。engine/API 批次按聚合 repo 消费。
func NewStateDB(app lynx.App, cfg *config.AppConfig) (*persistence.DB, func(), error) {
	db, err := persistence.Open(context.Background(), filepath.Join(cfg.DataRoot(), "fleetly.db"), persistence.WallClock())
	if err != nil {
		return nil, nil, err
	}
	app.Logger().Info("state store opened", "path", cfg.DataRoot(), "driver", "sqlite", "mode", "wal")
	return db, func() { _ = db.Close() }, nil
}

// NewRuntimeProvider 经工厂注册表构造 Runtime Provider（cmd/fleetlyd 的
// blank import 触发在册 Provider 自注册；选择 = config runtime.provider，
// 缺省 swarm——ADR-0052 决策 1：整集群声明迁移，未注册名 Build 报错
// fail-fast 并列在册候选）。
func NewRuntimeProvider(app lynx.App, cfg *config.AppConfig) (capability.Runtime, func(), error) {
	p, err := capability.Build(context.Background(), capability.KindRuntime, cfg.RuntimeProvider())
	if err != nil {
		return nil, nil, err
	}
	rt, ok := p.(capability.Runtime)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Runtime port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "runtime", rt)
	return rt, func() {
		if c, ok := rt.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}, nil
}

// NewHealthCheckers 聚合进程内在册 Provider 实例为健康检查面（IA v3
// 二期③：GetStatus components 的数据源——架构 §8 降级矩阵驱动）。face
// 类型经 Provider 断言回收 Health 契约（三件套必实现）；builder 家族
// 不入列（无状态构建执行器，健康无观测意义）。
func NewHealthCheckers(
	rt capability.Runtime,
	proxy capability.Proxy,
	reg capability.Registry,
	logs capability.Logging,
	mtr capability.Metrics,
	store capability.ObjectStore,
) []capability.Provider {
	faces := []any{rt, proxy, reg, logs, mtr, store}
	checkers := make([]capability.Provider, 0, len(faces))
	for _, face := range faces {
		if provider, ok := face.(capability.Provider); ok && provider != nil {
			checkers = append(checkers, provider)
		}
	}
	return checkers
}

// logCapabilityFaces 打一行能力提供面矩阵（FacesOf 协商点的可观测枚举，
// 2026-10-03 架构评审候选 4：可选子面的降级文化此前只活在 8 处断言点
// 注释里——启动期一行日志让"谁提供什么面"可查；GetStatus 扩字段随
// F0.19 能力降级矩阵批次设计）。
func logCapabilityFaces(l *slog.Logger, kind string, p capability.Provider) {
	faces := capability.FacesOf(p)
	offered := faces.Offered()
	if len(offered) == 0 {
		l.Info("capability faces", "kind", kind, "provider", p.Describe().Name, "faces", "none")
		return
	}
	l.Info("capability faces", "kind", kind, "provider", p.Describe().Name, "faces", strings.Join(offered, ","))
}

// NewBuilderProviders 构造 Builder 家族（ADR-0032：Builder 是 spec 路由
// 家族——全部在册 Provider 一并构造，BuildSpec.builder 是路由键；可选
// 能力——无在册者时返回 nil，构建链停用）。
func NewBuilderProviders() (map[string]capability.Builder, func(), error) {
	names := capability.RegisteredFactories()[capability.KindBuilder]
	builders := make(map[string]capability.Builder, len(names))
	for _, name := range names {
		p, err := capability.Build(context.Background(), capability.KindBuilder, name)
		if err != nil {
			return nil, nil, fmt.Errorf("assembly: builder %s: %w", name, err)
		}
		b, ok := p.(capability.Builder)
		if !ok {
			return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Builder port", name)
		}
		builders[name] = b
	}
	if len(builders) == 0 {
		return nil, func() {}, nil
	}
	return builders, func() {
		for _, b := range builders {
			if c, ok := b.(interface{ Close() error }); ok {
				_ = c.Close()
			}
		}
	}, nil
}

// NewRegistryProvider 构造 Registry Provider（zot 受管自宿；可选能力——
// config.registry.addr 与 env FLEETLY_REGISTRY_ADDR 皆未配置时返回 nil，
// Registry 面停用：镜像直投部署不受影响，build 源部署在 prepare 精确失败，
// ADR-0019 附录 B.2/B.5①）。config 值优先（ADR-0036）：经装配 ctx 注入
// 工厂，env 是同键旧通道兜底。
func NewRegistryProvider(app lynx.App, cfg *config.AppConfig) (capability.Registry, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindRegistry]) == 0 {
		return nil, func() {}, nil
	}
	ctx := capability.WithRegistryAddr(context.Background(), cfg.RegistryAddr())
	p, err := capability.Build(ctx, capability.KindRegistry, "")
	if err != nil {
		app.Logger().Warn("registry provider unavailable; build-source deployments disabled", "err", err)
		return nil, func() {}, nil
	}
	reg, ok := p.(capability.Registry)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Registry port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "registry", reg)
	return reg, func() {}, nil
}

// NewLoggingProvider 构造 Logging Provider（VictoriaLogs 受管自宿；可选
// 能力——config.logging.addr 未配置时返回 nil，Logging 面停用：logs 回退
// Runtime 实时路径、build 日志回退环形缓冲，ADR-0040。addr/保留窗经装配
// ctx 注入工厂（config 是唯一契约源，无 env 旧通道——本批首生即带键）。
func NewLoggingProvider(app lynx.App, cfg *config.AppConfig) (capability.Logging, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindLogging]) == 0 {
		return nil, func() {}, nil
	}
	if cfg.LoggingAddr() == "" {
		app.Logger().Info("logging address not configured; the managed log store stays disabled (logs fall back to the realtime path)")
		return nil, func() {}, nil
	}
	ctx := capability.WithLoggingAddr(context.Background(), cfg.LoggingAddr())
	ctx = capability.WithLoggingRetentionDays(ctx, cfg.LoggingRetentionDays())
	p, err := capability.Build(ctx, capability.KindLogging, "")
	if err != nil {
		// 工厂 fail loud 形态（地址在册但构造失败=数据根/凭证面故障）不吞：
		// 装配失败优于带病运行。
		return nil, nil, fmt.Errorf("assembly: logging provider: %w", err)
	}
	lg, ok := p.(capability.Logging)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Logging port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "logging", lg)
	return lg, func() {}, nil
}

// NewMetricsProvider 构造 Metrics Provider（VictoriaMetrics 受管自宿；可选
// 能力——config.metrics.addr 未配置时返回 nil，Metrics 面停用：零采集/零
// 告警、查询精确失败，ADR-0041）。
func NewMetricsProvider(app lynx.App, cfg *config.AppConfig) (capability.Metrics, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindMetrics]) == 0 {
		return nil, func() {}, nil
	}
	if cfg.MetricsAddr() == "" {
		app.Logger().Info("metrics address not configured; the managed metrics store stays disabled (no collection, no alerting)")
		return nil, func() {}, nil
	}
	ctx := capability.WithMetricsAddr(context.Background(), cfg.MetricsAddr())
	ctx = capability.WithMetricsRetentionDays(ctx, cfg.MetricsRetentionDays())
	p, err := capability.Build(ctx, capability.KindMetrics, "")
	if err != nil {
		return nil, nil, fmt.Errorf("assembly: metrics provider: %w", err)
	}
	m, ok := p.(capability.Metrics)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Metrics port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "metrics", m)
	return m, func() {}, nil
}

// NewMaterialCipher 打开数据根 KEK（首启生成；ADR-0014 信封加密根）。
func NewMaterialCipher(cfg *config.AppConfig) (*material.Cipher, func(), error) {
	c, err := keyring.LoadCipher(cfg.DataRoot())
	if err != nil {
		return nil, nil, err
	}
	return c, func() {}, nil
}

// NewObjectStore 经工厂注册表构造 ObjectStore 端口（ADR-0039 本地目标
// 开箱即用；ADR-0042 装配选择 = 在场驱动：platform_backup.s3 五元组在场
// → s3 Provider，缺席 → local Provider（升级零扰动）。选择谓词与「同机
// 备份非灾备」告警解除谓词是同一谓词——消警路径零改动）。
func NewObjectStore(app lynx.App, cfg *config.AppConfig) (capability.ObjectStore, func(), error) {
	name, ctx := objectStoreSelection(cfg)
	p, err := capability.Build(ctx, capability.KindObjectStore, name)
	if err != nil {
		return nil, nil, err
	}
	store, ok := p.(capability.ObjectStore)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the ObjectStore port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "objectstore", store)
	return store, func() {}, nil
}

// objectStoreSelection 是装配选择的决策面（纯函数可测——assembly 测试不
// import providers，选择语义在此钉死）：s3 五元组在场 → 名 "s3" + 配置
// 经装配 ctx 注入（config 是唯一契约源，ADR-0042 决策 2）；缺席 → "local"
// 且不注入（s3 工厂被显式选择而无配置时在工厂面精确失败）。
func objectStoreSelection(cfg *config.AppConfig) (string, context.Context) {
	ctx := context.Background()
	s3 := cfg.PlatformBackupS3()
	if s3 == nil {
		return "local", ctx
	}
	return "s3", capability.WithObjectStoreS3(ctx, &capability.ObjectStoreS3Config{
		Endpoint: s3.GetEndpoint(), Bucket: s3.GetBucket(),
		AccessKeyID: s3.GetAccessKeyId(), SecretAccessKey: s3.GetSecretAccessKey(),
	})
}

// checkConvergenceInspector 是收敛能力的装配门（R16-L3）：posture=all 是
// 缺省承诺（检出漂移即收敛），而 Runtime 未实现 Inspector 子面时 driftScan
// 的 spec/gen/缺席检测整拍失明——收敛静默停摆，装配期 fail-fast。posture=
// detect 是显式零写姿态，Inspector 缺席不违背承诺（此时由调用方记 Warn +
// doctor 的 convergence capability 行承载可发现性）。
func checkConvergenceInspector(rt capability.Provider, posture string) error {
	if rt == nil || posture != engine.ConvergePostureAll {
		return nil
	}
	if capability.FacesOf(rt).Inspector == nil {
		return fmt.Errorf("assembly: engine.converge_posture is %q but runtime provider %s does not implement the RuntimeInspector subface — drift detection and default convergence would silently stall (R16-L3); implement the inspector or set engine.converge_posture=%q",
			posture, rt.Describe().Name, engine.ConvergePostureDetect)
	}
	return nil
}

// NewEngine 构造部署收敛引擎（重叠策略旋钮从 AppConfig 透传并 fail-fast
// 校验——ADR-0017 附录 A.4；其余参数当前取默认，配置面接入后从 AppConfig
// 继续透传 queue 容量/观察窗/构建并发）。
func NewEngine(
	db *persistence.DB,
	stores transact.Stores,
	tx transact.Tx,
	rt capability.Runtime,
	b map[string]capability.Builder,
	proxy capability.Proxy,
	reg capability.Registry,
	logs capability.Logging,
	mtr capability.Metrics,
	store capability.ObjectStore,
	cipher *material.Cipher,
	app lynx.App,
	cfg *config.AppConfig,
	info buildinfo.BuildInfo,
) (*engine.Engine, error) {
	// 节点平台 ID 铸造权归平台（ADR-0070）：Provider 经可选接口拿铸造闭包
	// （providers 不 import state，装配期接线；未实现接口的 Provider 照旧）。
	if m, ok := rt.(capability.NodeIDMinter); ok {
		nodes := stores.Nodes
		m.SetNodeIDMinter(func(ctx context.Context) (string, error) {
			return nodes.MintPlatformID(ctx)
		})
	}
	overlap, err := engine.ParseScheduleOverlap(cfg.ScheduleOverlapPolicy())
	if err != nil {
		return nil, err
	}
	// 收敛姿态旋钮（ADR-0082 决策 5）：AppConfig 透传并 fail-fast 校验
	//（detect = 只报事件不收敛的观察期逃生门；缺省 all——检出漂移即收敛，
	// 上站裁决不设 detect 观察期）。冷却记账参数同批透传。
	posture, err := engine.ParseConvergePosture(cfg.ConvergePosture())
	if err != nil {
		return nil, err
	}
	// 收敛能力装配门（R16-L3）：缺省收敛依赖 Inspector 子面；缺席即启动
	// 期 fail-fast（detect 显式降级形态记 Warn，doctor 有 capability 行）。
	if err := checkConvergenceInspector(rt, posture); err != nil {
		return nil, err
	}
	if posture == engine.ConvergePostureDetect && capability.FacesOf(rt).Inspector == nil {
		app.Logger().Warn("convergence: runtime provider has no Inspector subface; posture=detect can detect nothing (spec drift, gen deviation and absence checks are disabled)",
			"provider", rt.Describe().Name)
	}
	// Platform Backup 配置快照（ADR-0039）：缺省节拍/保留经 config 访问器；
	// s3 未配置 = 仅本地仓。
	pb := &engine.PlatformBackupConfig{
		Interval:  time.Duration(cfg.PlatformBackupInterval()) * time.Second,
		Retention: time.Duration(cfg.PlatformBackupRetention()) * time.Second,
	}
	if s3 := cfg.PlatformBackupS3(); s3 != nil {
		pb.S3 = &engine.S3RepoConfig{
			Endpoint: s3.GetEndpoint(), Bucket: s3.GetBucket(), Prefix: s3.GetPrefix(),
			AccessKeyID: s3.GetAccessKeyId(), SecretAccessKey: s3.GetSecretAccessKey(),
		}
	}
	// TLS 缺省归一 none：traefik 渲染层把空串当 auto（既有口径），browse
	// 的缺省明文（ADR-0051 决策 5）必须在装配侧钉住。
	browseTLS := cfg.Browse.GetTls()
	if browseTLS == "" {
		browseTLS = "none"
	}
	return engine.New(engine.Deps{
		DB: db, Stores: stores, Tx: tx, Runtime: rt, Builders: b, Proxy: proxy, Registry: reg, Logging: logs,
		Metrics: mtr, ObjectStore: store, Cipher: cipher, Logger: app.Logger(),
	}, engine.Options{
		Task: engine.TaskOptions{
			ScheduleOverlap: overlap,
		},
		Converge: engine.ConvergeOptions{
			Posture:          posture,
			FailureThreshold: int(cfg.ConvergeFailureThreshold()),
			Cooldown:         time.Duration(cfg.ConvergeCooldownSecs()) * time.Second,
		},
		Backup: engine.BackupOptions{
			PlatformBackup: pb,
		},
		// 回环中继代理（ADR-0049）：gateway 基址按绑定地址派生（空 host =
		// 127.0.0.1；绑定具体 IP 时照用该 IP——staging 形态 10.124.0.3:9081
		// 只在该地址监听）。版本取 buildinfo（回环代理握手上报）。
		Relay: engine.RelayOptions{
			RelayLoopbackURL: relayLoopbackURL(cfg.HTTPAddr()),
			RelayVersion:     info.Version,
		},
		// 数据浏览器面（F3.6，ADR-0051）：config 三字段直通（host_suffix 空
		// = 面停用——BrowseDatabase 精确拒绝，升级零扰动）；TLS 缺省经
		// browseTLS 归一 none（traefik 渲染层把空串当 auto 的既有口径）。
		Data: engine.DataOptions{
			DataRoot: cfg.DataRoot(),
			Browse: engine.BrowseConfig{
				HostSuffix: cfg.Browse.GetHostSuffix(),
				GatewayURL: cfg.Browse.GetGatewayUrl(),
				TLSMode:    browseTLS,
			},
			// Console 面板公网发布面（ADR-0061）：config 三字段直通（host 空 =
			// 不发布，升级零扰动）；tls 空值随渲染层口径视为 auto，不归一。
			Console: engine.ConsoleConfig{
				Host:       cfg.Console.GetHost(),
				GatewayURL: cfg.Console.GetGatewayUrl(),
				TLSMode:    cfg.Console.GetTls(),
			},
			// API 公网发布面（ADR-0079）：config 三字段直通（host 空 =
			// 不发布，升级零扰动）；tls 空值同 console 口径视为 auto。
			Api: engine.ApiConfig{
				Host:    cfg.Api.GetHost(),
				GRPCURL: cfg.Api.GetGrpcUrl(),
				TLSMode: cfg.Api.GetTls(),
			},
		},
	}), nil
}

// relayLoopbackURL 从 gateway 绑定地址派生回环代理基址。
func relayLoopbackURL(httpAddr string) string {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port
}

// gatewayPort 从 gateway 绑定地址取端口段（EnrollNode 定向中继材料的
// NodeRelayCommand 拼装锚；空 = 无中继材料面）。
func gatewayPort(httpAddr string) string {
	_, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return ""
	}
	return port
}

// NewAPIDeps 构造六上下文 API 服务依赖集（P1b C 批构造上提：repo 构造
// 与 anchor 组装集中组合根，fleetlygrpc 零持久化构造；生产 assembly 与
// apitest 夹具共用本构造——测试面与生产面同源）。scope 词表单一源注入；
// dataRoot 透传给上传产物 blob 面。
func NewAPIDeps(db *persistence.DB, bind transact.Binder, e *engine.Engine, cipher *material.Cipher, rt capability.Runtime, dataRoot string, vocab []string, log *slog.Logger) fleetlygrpc.Deps {
	clock := db.Clock()
	projects := projectrepo.New(clock, db.Runner())
	apps := apprepo.New(clock, db.Runner())
	tasks := taskrepo.New(clock, db.Runner())
	schedules := schedulerepo.New(clock, db.Runner())
	networks := networkrepo.New(clock, db.Runner())
	peers := networkpeerrepo.New(clock, db.Runner())
	routes := routerepo.New(clock, db.Runner())
	volumes := volumerepo.New(clock, db.Runner())
	runs := runrepo.New(clock, db.Runner())
	deployments := deploymentrepo.New(clock, db.Runner())
	builds := buildrepo.New(clock, db.Runner())
	hooks := hookrepo.New(clock, db.Runner())
	ghApps := githubapprepo.New(clock, db.Runner())
	databases := dbrepo.New(clock, db.Runner())
	return fleetlygrpc.Deps{
		DB: db, Tx: transact.NewTx(db, bind), Engine: e, Cipher: cipher, Runtime: rt,
		Logging:     e.LoggingProvider(),
		Metrics:     e.MetricsProvider(),
		Vocab:       vocab,
		Log:         log,
		GitHub:      github.NewClient(nil, nil),
		UploadStore: upload.NewStore(dataRoot, 0, 0),

		Projects:        projects,
		Apps:            apps,
		Deployments:     deployments,
		Revisions:       revisionrepo.New(clock, db.Runner()),
		Builds:          builds,
		Tasks:           tasks,
		Runs:            runs,
		Schedules:       schedules,
		OutboxEvents:    outbox.New(clock, db.Runner()),
		Secrets:         secretrepo.New(clock, db.Runner()),
		Configs:         cfgrepo.New(clock, db.Runner()),
		SharedVariables: sharedvariablerepo.New(clock, db.Runner()),
		AppVariables:    appvariablerepo.New(clock, db.Runner()),
		Volumes:         volumes,
		Networks:        networks,
		NetworkPeers:    peers,
		Freezes:         freezerepo.New(clock, db.Runner()),
		Routes:          routes,
		Nodes:           noderepo.New(clock, db.Runner()),
		Audits:          auditrepo.New(clock, db.Runner()),
		Users:           userrepo.New(clock, db.Runner()),
		Teams:           teamrepo.New(clock, db.Runner()),
		Roles:           rolerepo.New(clock, db.Runner()),
		Memberships:     membershiprepo.New(clock, db.Runner()),
		Tokens:          tokenrepo.New(clock, db.Runner()),
		Invitations:     invitationrepo.New(clock, db.Runner()),
		Hooks:           hooks,
		GitHubApps:      ghApps,
		Uploads:         sourcerepo.New(clock, db.Runner()),
		Databases:       databases,
		Browse:          browserepo.New(clock, db.Runner()),
		Backups:         backuprepo.New(clock, db.Runner()),
		Catalog:         catalogrepo.New(clock, db.Runner()),
		// anchor 与 Services 字段共用同一批 repo 实例（构造期共享，与
		// 构造上提前的 NewServices 形态一致）。
		Anchor: anchor.New(anchor.Repos{
			Projects: projects, Apps: apps, Tasks: tasks, Schedules: schedules,
			Networks: networks, Peers: peers, Hooks: hooks, Routes: routes,
			Databases: databases, Volumes: volumes, Runs: runs,
			Deployments: deployments, Builds: builds, GitHubApps: ghApps,
		}),
	}
}

// NewAPIServices 构造六上下文 API 服务依赖集（scope 词表单一源注入；
// gateway 端口 = 定向中继材料的 NodeRelayCommand 拼装锚，H10）。
// auth.login_token_ttl 在此解析并注入 Login 铸造面：非法值启动 fail-fast
// （S18-B / R14-M1——配置错误不得静默回退到永不过期）。
func NewAPIServices(db *persistence.DB, bind transact.Binder, e *engine.Engine, cipher *material.Cipher, rt capability.Runtime, cfg *config.AppConfig, app lynx.App) (*fleetlygrpc.Services, error) {
	loginTTL, err := cfg.LoginTokenTTL()
	if err != nil {
		return nil, err
	}
	s := fleetlygrpc.NewServices(NewAPIDeps(db, bind, e, cipher, rt, cfg.DataRoot(), ScopeResources(), app.Logger()))
	s.GatewayPort = gatewayPort(cfg.HTTPAddr())
	s.LoginTokenTTL = loginTTL
	// App 模板目录刷新源（F3.3，ADR-0050 决策 4：空 = 刷新停用，内嵌目录
	// 即全部）。
	if cfg.Server != nil {
		s.TemplatesCatalogURL = cfg.Server.TemplatesCatalogUrl
	}
	// manifest 自动建流的公网基址（ADR-0073 决策 8）：console.host 非空才
	// 启用（redirect_url 必须 https——console 面按部署形态恒经 ACME/反代）。
	if cfg.Console != nil && cfg.Console.GetHost() != "" {
		s.GitHubManifestBase = "https://" + cfg.Console.GetHost()
	}
	// B1 确认票据面回注（engine 先于 Services 构造的装配序闭合点）：
	// engine 受理临界区的验票/烧票与 confirm 拦截器共享 Services 的同一
	// 进程内票据店（NewGRPCServer 侧经 Services.ConfirmTickets 取同一实例）。
	e.BindConfirmTickets(s.ConfirmTickets())
	return s, nil
}

// NewIdemEnforcer 构造通用幂等执法器（ADR-0024：拦截器 + janitor sweep 面）。
func NewIdemEnforcer(db *persistence.DB, app lynx.App) *idem.Enforcer {
	return idem.NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), app.Logger())
}

// NewRateLimiter 构造创建速率限制器（ADR-0017 附录 A.2 缺省：60s 固定窗 /
// 每 Token 120 次，内存计数——控制面单进程）。配置面接入后从 AppConfig
// 透传窗与预算。
func NewRateLimiter(db *persistence.DB) *governance.RateLimiter {
	return governance.NewRateLimiter(db.Clock(), governance.DefaultRateWindow, governance.DefaultRateBudget)
}

// NewFreezeGuard 构造变更冻结执法器（ADR-0017 附录 A.3：拦截器 + Team
// 逐级回行解析；repo 与归属解析图在组合根构造——P1b C 批构造上提）。
func NewFreezeGuard(db *persistence.DB, app lynx.App) *governance.FreezeGuard {
	return governance.NewFreezeGuard(db, freezerepo.New(db.Clock(), db.Runner()), NewAnchor(db), app.Logger())
}

// retentionSweepInterval 是保留窗清扫节拍（幂等记录 24h/认领 90s、事件
// outbox 7d——十分钟粒度足够收敛积压，Sweep 均为单条 DELETE，成本可忽略）。
const retentionSweepInterval = 10 * time.Minute

// outboxRetention 是事件保留窗（ADR-0026）：窗内只增（订阅契约以
// earliest_seq 划界），窗外由 retention janitor 回收——Task 事件量下
// outbox 无界增长的自愈上限。7d = Console 隔周末重连不断档的宽裕缺省。
const outboxRetention = 7 * 24 * time.Hour

// uploadRetention 是上传产物保留窗（ADR-0019 附录 A.3）：未被任何 Revision
// 引用的上传行过窗清扫；引用=spec 体内含 upload id（Revision 冻结先于
// engine.Submit，部署路径天然受保护）。孤儿 tmp（崩溃遗留）>1h 清理。
const (
	uploadRetention = 7 * 24 * time.Hour
	uploadTmpAge    = 1 * time.Hour
)

// 载体卫生清扫面（收尾批 E29 + N2 评审 P2-4）：孤儿 Secret 载体删除预算
// （每拍上限——staging zot 风暴存量 1300+，100/拍×10 分钟节拍 ≈ 2 小时清
// 空，稳态近零）、匿名孤儿卷删除预算（staging manager 积 1680 枚镜像
// VOLUME 遗产，100/拍 ≈ 17 拍 ≈ 3 小时清空，稳态每次 db 替换泄漏一枚、
// 7d 窗后回收）与终态 Task 残留载体清扫的候选窗/每拍行数。窗取 7d 与事件/
// 上传保留窗同文化；行序新→旧（近期收口才是残留实际所在），已收敛行是
// 幂等 no-op。
const (
	orphanSecretDeleteBudget = 100
	orphanVolumeDeleteBudget = 100
	orphanGrantDeleteBudget  = 100
	terminalCarrierWindow    = 7 * 24 * time.Hour
	terminalCarrierLimit     = 20
)

// RetentionJanitorService 把保留窗清扫（幂等记录 + 事件 outbox + 上传产物
// + 载体卫生）适配为 lynx 托管服务：复用 engine.NewLoop 唯一循环骨架（架构 §0"每段只许有一
// 份"），不自建 ticker。具名类型：wire 对 lynx.Service 同型多 provider
// 需可区分。
type RetentionJanitorService struct {
	enforcer *idem.Enforcer
	db       *persistence.DB
	uploads  sourceupload.Repo
	blobs    *upload.Store
	engine   *engine.Engine
	log      *slog.Logger
}

func (s *RetentionJanitorService) Name() string               { return "retention-janitor" }
func (s *RetentionJanitorService) Init(lynx.AppContext) error { return nil }
func (s *RetentionJanitorService) Start(ctx context.Context) error {
	engine.NewLoop("retention-janitor", s.log).Run(ctx, retentionSweepInterval, func(ctx context.Context) {
		if n, err := s.enforcer.Sweep(ctx); err != nil {
			s.log.Error("retention janitor: idempotency sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: swept expired idempotency records", "count", n)
		}
		if n, err := outbox.New(s.db.Clock(), s.db.Runner()).TrimBefore(ctx, s.db.Clock().Now().Add(-outboxRetention)); err != nil {
			s.log.Error("retention janitor: outbox trim", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: trimmed outbox rows past the retention window", "count", n)
		}
		s.sweepUploads(ctx)
		if n, err := s.blobs.SweepOrphanTmp(uploadTmpAge); err != nil {
			s.log.Error("retention janitor: upload tmp sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: removed orphaned upload staging files", "count", n)
		}
		// 载体卫生（E29 + P2-4）：删除面在 engine/Runtime Provider，本服务只供节
		// 拍与预算（幂等可重放：预算内下一拍续清）。
		if n, err := s.engine.SweepOrphanSecretCarriers(ctx, orphanSecretDeleteBudget); err != nil {
			s.log.Error("retention janitor: orphan secret sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: removed orphaned secret carriers", "count", n)
		}
		if n, err := s.engine.SweepOrphanVolumeCarriers(ctx, orphanVolumeDeleteBudget); err != nil {
			s.log.Error("retention janitor: orphan volume sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: removed orphaned anonymous volumes", "count", n)
		}
		if n, err := s.engine.SweepOrphanPeerGrantCarriers(ctx, orphanGrantDeleteBudget); err != nil {
			s.log.Error("retention janitor: orphan peer grant sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: removed orphaned peer grant policies", "count", n)
		}
		if n, err := s.engine.SweepTerminalTaskCarriers(ctx, terminalCarrierWindow, terminalCarrierLimit); err != nil {
			s.log.Error("retention janitor: terminal task carrier sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: swept residual carriers of terminal tasks", "count", n)
		}
	})
	return nil
}
func (s *RetentionJanitorService) Stop(context.Context) error { return nil }

// sweepUploads 清扫过保留窗且未被任何 Revision 引用的上传行；末行删除时
// blob 一并删除（refcount=行数，ADR-0019 附录 A.3）。
func (s *RetentionJanitorService) sweepUploads(ctx context.Context) {
	cutoff := state.FormatTime(s.db.Clock().Now().Add(-uploadRetention))
	stale, err := s.uploads.SweepUnreferenced(ctx, cutoff)
	if err != nil {
		s.log.Error("retention janitor: upload sweep scan", "err", err)
		return
	}
	swept := 0
	for _, u := range stale {
		if err := s.db.Tx(ctx, func(run state.Runner) error {
			return sourcerepo.New(s.db.Clock(), run).Delete(ctx, u.ID)
		}); err != nil {
			s.log.Error("retention janitor: upload row delete", "upload", u.ID, "err", err)
			continue
		}
		swept++
		if n, cerr := s.uploads.CountByDigest(ctx, u.Digest); cerr == nil && n == 0 {
			if derr := s.blobs.DeleteBlob(u.Digest); derr != nil {
				s.log.Error("retention janitor: upload blob delete", "digest", u.Digest, "err", derr)
			}
		}
	}
	if swept > 0 {
		s.log.Info("retention janitor: swept unreferenced uploads past the retention window", "count", swept)
	}
}

// NewRetentionJanitorService 构造 janitor 托管服务（engine 注入 = 载体
// 卫生面的消费口：engine 拥有 Task 域 ns 解析真源与 RuntimeHygiene 子面
// 判定，本服务只供节拍与预算）。
func NewRetentionJanitorService(enforcer *idem.Enforcer, db *persistence.DB, cfg *config.AppConfig, e *engine.Engine, app lynx.App) *RetentionJanitorService {
	return &RetentionJanitorService{
		enforcer: enforcer, db: db,
		uploads: sourcerepo.New(db.Clock(), db.Runner()),
		blobs:   upload.NewStore(cfg.DataRoot(), 0, 0),
		engine:  e,
		log:     app.Logger(),
	}
}

// engineService 把引擎适配为 lynx 托管服务（组合根职责：engine 包不依赖
// 框架，hermetic 测试保持纯净）。
type engineService struct {
	e *engine.Engine
}

func (s *engineService) Name() string               { return "engine" }
func (s *engineService) Init(lynx.AppContext) error { return nil }
func (s *engineService) Start(ctx context.Context) error {
	s.e.Start(ctx)
	// lynx 服务契约：Start 阻塞直至关停（快速返回会被当作服务退出并
	// 触发全局 shutdown）。
	<-ctx.Done()
	return nil
}
func (s *engineService) Stop(ctx context.Context) error { return s.e.Stop(ctx) }

// NewEngineService 构造托管服务。
func NewEngineService(e *engine.Engine) lynx.Service { return &engineService{e: e} }

// NewAppConfig 从 lynx 配置源解码 AppConfig 并应用缺省。
func NewAppConfig(app lynx.App) (*config.AppConfig, error) {
	var c config.AppConfig
	if err := config.UnmarshalConfig(app.Config(), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// NewServiceFactories 返回惰性服务工厂：当前为空（单进程全量注册）；
// Console SPA embed 等可选编译面随对应批次挂入。
func NewServiceFactories() []lynx.ServiceFactory { return nil }

// NewPreStartHooks 启动前钩子（OnPreStart，先于监听）：先跑暴露面门
// （受管 Proxy 配置端点在多租户形态下不得无认证——R09-M8/R14-M6 →
// S14-c9；失败即 fail-fast），再执行 identity 种子 + Bootstrap Token 首启
// 流（无用户时生成一次、journal 去重；F0.2/F0.6）。
func NewPreStartHooks(db *persistence.DB, seedRepos authn.SeedRepos, cfg *config.AppConfig, app lynx.App) boot.PreStartHooks {
	return boot.PreStartHooks{func(ctx context.Context) error {
		if err := ensureProxyConfigExposure(ctx, db, cfg); err != nil {
			return err
		}
		_, err := authn.EnsureBootstrapToken(ctx, db, transact.NewTx(db, NewBinder(db)), seedRepos, cfg.DataRoot(), ScopeResources(), app.Logger())
		return err
	}}
}

// NewDrainHooks 排水钩子（OnDrain，摘流窗口内执行）：当前为空；Managed
// Provider 摘流随 Proxy 批次挂入。
func NewDrainHooks() boot.DrainHooks { return nil }

// NewPreStopHooks 停止前钩子（OnPreStop，服务仍在处理在途请求）：当前为
// 空；引擎排空（in-flight Ensure 可安全中断，ADR-0005）随部署链挂入。
func NewPreStopHooks() boot.PreStopHooks { return nil }

// NewPostStopHooks 收尾钩子（OnPostStop 预算内执行）：当前为空；wire
// cleanup 已单独经 Bootstrap 返回值挂载。
func NewPostStopHooks() boot.PostStopHooks { return nil }

// NewServices 返回服务注册顺序：engine → idem-janitor → grpc → gateway →
// proxyconfig。lynx 按注册顺序启动、逆序停止——引擎最后停：服务面已摘流
// （gateway → grpc 先停）后引擎才排空（ADR-0005 优雅退出：在途 Ensure 可
// 安全中断重放）。proxyconfig（受管 Proxy 的配置拉取端点）最先停——受管
// 实例轮询失败保留存量配置，无中断面。idem-janitor 在 grpc 之前停：新请
// 求面关闭后不再产生新记录，收尾 Sweep 由下次启动补上。
func NewServices(
	engineSvc lynx.Service,
	idemJanitor *RetentionJanitorService,
	grpcServer *lynxgrpc.Server,
	gateway *lynxhttp.Server,
	proxyConfig *ProxyConfigServer,
) []lynx.Service {
	services := []lynx.Service{engineSvc, idemJanitor, grpcServer, gateway}
	if proxyConfig != nil {
		services = append(services, proxyConfig)
	}
	return services
}
