package assembly

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/wire"
	"github.com/lynx-go/grpcapi/authz"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/api/systemgrpc"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/governance"
	"github.com/fleetlyrun/fleetly/internal/idem"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/upload"
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
	NewAuthenticator,
	NewRuntimeProvider,
	NewBuilderProviders,
	NewEdgeProvider,
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
	NewEdgeConfigServer,
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

// NewAuthenticator 构造身份执法器（authn 拦截器依赖集；词表单一源注入）。
func NewAuthenticator(db *state.DB, policySet *authz.PolicySet, app lynx.App) *authn.Authenticator {
	return authn.NewAuthenticator(db, policySet, ScopeResources(), app.Logger())
}

// NewStateDB 打开数据根下的控制面库（WAL + goose 前滚迁移在 Open 内完成；
// 关闭经 wire cleanup 聚合到 OnPostStop）。engine/API 批次按聚合 repo 消费。
func NewStateDB(app lynx.App, cfg *config.AppConfig) (*state.DB, func(), error) {
	db, err := state.Open(context.Background(), filepath.Join(cfg.DataRoot(), "fleetly.db"), state.WallClock())
	if err != nil {
		return nil, nil, err
	}
	app.Logger().Info("state store opened", "path", cfg.DataRoot(), "driver", "sqlite", "mode", "wal")
	return db, func() { _ = db.Close() }, nil
}

// NewRuntimeProvider 经工厂注册表构造 Runtime Provider（cmd/fleetlyd 的
// blank import 触发 swarm 自注册；配置层选择 Provider 名的能力随配置面
// 扩展接入，当前缺省在册者）。
func NewRuntimeProvider(app lynx.App) (capability.Runtime, func(), error) {
	p, err := capability.Build(context.Background(), capability.KindRuntime, "")
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
	c, err := material.LoadCipher(cfg.DataRoot())
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

// NewEngine 构造部署收敛引擎（重叠策略旋钮从 AppConfig 透传并 fail-fast
// 校验——ADR-0017 附录 A.4；其余参数当前取默认，配置面接入后从 AppConfig
// 继续透传 queue 容量/观察窗/构建并发）。
func NewEngine(
	db *state.DB,
	rt capability.Runtime,
	b map[string]capability.Builder,
	edge capability.Edge,
	reg capability.Registry,
	logs capability.Logging,
	mtr capability.Metrics,
	store capability.ObjectStore,
	cipher *material.Cipher,
	app lynx.App,
	cfg *config.AppConfig,
) (*engine.Engine, error) {
	overlap, err := engine.ParseScheduleOverlap(cfg.ScheduleOverlapPolicy())
	if err != nil {
		return nil, err
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
	return engine.New(engine.Deps{
		DB: db, Runtime: rt, Builders: b, Edge: edge, Registry: reg, Logging: logs,
		Metrics: mtr, ObjectStore: store, Cipher: cipher, Logger: app.Logger(),
	}, engine.Options{
		DataRoot:        cfg.DataRoot(),
		ScheduleOverlap: overlap,
		PlatformBackup:  pb,
	}), nil
}

// NewAPIServices 构造六上下文 API 服务依赖集（scope 词表单一源注入；
// dataRoot 透传给上传产物 blob 面）。
func NewAPIServices(db *state.DB, e *engine.Engine, cipher *material.Cipher, rt capability.Runtime, cfg *config.AppConfig, app lynx.App) *fleetlygrpc.Services {
	return fleetlygrpc.NewServices(db, e, cipher, rt, cfg.DataRoot(), ScopeResources(), app.Logger())
}

// NewIdemEnforcer 构造通用幂等执法器（ADR-0024：拦截器 + janitor sweep 面）。
func NewIdemEnforcer(db *state.DB, app lynx.App) *idem.Enforcer {
	return idem.NewEnforcer(db, app.Logger())
}

// NewRateLimiter 构造创建速率限制器（ADR-0017 附录 A.2 缺省：60s 固定窗 /
// 每 Token 120 次，内存计数——控制面单进程）。配置面接入后从 AppConfig
// 透传窗与预算。
func NewRateLimiter(db *state.DB) *governance.RateLimiter {
	return governance.NewRateLimiter(db.Clock(), governance.DefaultRateWindow, governance.DefaultRateBudget)
}

// NewFreezeGuard 构造变更冻结执法器（ADR-0017 附录 A.3：拦截器 + Team
// 逐级回行解析）。
func NewFreezeGuard(db *state.DB, app lynx.App) *governance.FreezeGuard {
	return governance.NewFreezeGuard(db, app.Logger())
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

// 载体卫生清扫面（收尾批 E29）：孤儿 Secret 载体删除预算（每拍上限——
// staging zot 风暴存量 1300+，100/拍×10 分钟节拍 ≈ 2 小时清空，稳态近零）
// 与终态 Task 残留载体清扫的候选窗/每拍行数。窗取 7d 与事件/上传保留窗同
// 文化；行序新→旧（近期收口才是残留实际所在），已收敛行是幂等 no-op。
const (
	orphanSecretDeleteBudget = 100
	terminalCarrierWindow    = 7 * 24 * time.Hour
	terminalCarrierLimit     = 20
)

// RetentionJanitorService 把保留窗清扫（幂等记录 + 事件 outbox + 上传产物
// + 载体卫生）适配为 lynx 托管服务：复用 engine.NewLoop 唯一循环骨架（架构 §0"每段只许有一
// 份"），不自建 ticker。具名类型：wire 对 lynx.Service 同型多 provider
// 需可区分。
type RetentionJanitorService struct {
	enforcer *idem.Enforcer
	db       *state.DB
	uploads  *sourceupload.Repo
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
		if n, err := outbox.New(s.db.Clock()).TrimBefore(ctx, s.db.Runner(), s.db.Clock().Now().Add(-outboxRetention)); err != nil {
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
		// 载体卫生（E29）：删除面在 engine/Runtime Provider，本服务只供节
		// 拍与预算（幂等可重放：预算内下一拍续清）。
		if n, err := s.engine.SweepOrphanSecretCarriers(ctx, orphanSecretDeleteBudget); err != nil {
			s.log.Error("retention janitor: orphan secret sweep", "err", err)
		} else if n > 0 {
			s.log.Info("retention janitor: removed orphaned secret carriers", "count", n)
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
	stale, err := s.uploads.SweepUnreferenced(ctx, s.db.Runner(), cutoff)
	if err != nil {
		s.log.Error("retention janitor: upload sweep scan", "err", err)
		return
	}
	swept := 0
	for _, u := range stale {
		if err := s.db.Tx(ctx, func(tx *sql.Tx) error {
			return s.uploads.Delete(ctx, tx, u.ID)
		}); err != nil {
			s.log.Error("retention janitor: upload row delete", "upload", u.ID, "err", err)
			continue
		}
		swept++
		if n, cerr := s.uploads.CountByDigest(ctx, s.db.Runner(), u.Digest); cerr == nil && n == 0 {
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
func NewRetentionJanitorService(enforcer *idem.Enforcer, db *state.DB, cfg *config.AppConfig, e *engine.Engine, app lynx.App) *RetentionJanitorService {
	return &RetentionJanitorService{
		enforcer: enforcer, db: db,
		uploads: sourceupload.New(db.Clock()),
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

// NewPreStartHooks 启动前钩子（OnPreStart，先于监听）：identity 种子 +
// Bootstrap Token 首启流（无用户时生成一次、journal 去重；F0.2/F0.6）。
func NewPreStartHooks(db *state.DB, cfg *config.AppConfig, app lynx.App) boot.PreStartHooks {
	return boot.PreStartHooks{func(ctx context.Context) error {
		_, err := authn.EnsureBootstrapToken(ctx, db, cfg.DataRoot(), ScopeResources(), app.Logger())
		return err
	}}
}

// NewDrainHooks 排水钩子（OnDrain，摘流窗口内执行）：当前为空；Managed
// Provider 摘流随 Edge 批次挂入。
func NewDrainHooks() boot.DrainHooks { return nil }

// NewPreStopHooks 停止前钩子（OnPreStop，服务仍在处理在途请求）：当前为
// 空；引擎排空（in-flight Ensure 可安全中断，ADR-0005）随部署链挂入。
func NewPreStopHooks() boot.PreStopHooks { return nil }

// NewPostStopHooks 收尾钩子（OnPostStop 预算内执行）：当前为空；wire
// cleanup 已单独经 Bootstrap 返回值挂载。
func NewPostStopHooks() boot.PostStopHooks { return nil }

// NewServices 返回服务注册顺序：engine → idem-janitor → grpc → gateway →
// edgeconfig。lynx 按注册顺序启动、逆序停止——引擎最后停：服务面已摘流
// （gateway → grpc 先停）后引擎才排空（ADR-0005 优雅退出：在途 Ensure 可
// 安全中断重放）。edgeconfig（受管 Edge 的配置拉取端点）最先停——受管
// 实例轮询失败保留存量配置，无中断面。idem-janitor 在 grpc 之前停：新请
// 求面关闭后不再产生新记录，收尾 Sweep 由下次启动补上。
func NewServices(
	engineSvc lynx.Service,
	idemJanitor *RetentionJanitorService,
	grpcServer *lynxgrpc.Server,
	gateway *lynxhttp.Server,
	edgeConfig *EdgeConfigServer,
) []lynx.Service {
	services := []lynx.Service{engineSvc, idemJanitor, grpcServer, gateway}
	if edgeConfig != nil {
		services = append(services, edgeConfig)
	}
	return services
}
