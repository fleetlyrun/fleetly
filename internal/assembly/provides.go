package assembly

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
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
	NewBuilderProvider,
	NewEdgeProvider,
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
	return rt, func() {
		if c, ok := rt.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}, nil
}

// NewBuilderProvider 构造 Builder Provider（dockerfile 经 /session 连本机
// daemon 内嵌 buildkit；可选能力——无在册者时返回 nil，构建链停用）。
func NewBuilderProvider() (capability.Builder, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindBuilder]) == 0 {
		return nil, func() {}, nil
	}
	p, err := capability.Build(context.Background(), capability.KindBuilder, "")
	if err != nil {
		return nil, nil, err
	}
	b, ok := p.(capability.Builder)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Builder port", p.Describe().Name)
	}
	return b, func() {
		if c, ok := b.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}, nil
}

// NewMaterialCipher 打开数据根 KEK（首启生成；ADR-0014 信封加密根）。
func NewMaterialCipher(cfg *config.AppConfig) (*material.Cipher, func(), error) {
	c, err := material.LoadCipher(cfg.DataRoot())
	if err != nil {
		return nil, nil, err
	}
	return c, func() {}, nil
}

// NewEngine 构造部署收敛引擎（重叠策略旋钮从 AppConfig 透传并 fail-fast
// 校验——ADR-0017 附录 A.4；其余参数当前取默认，配置面接入后从 AppConfig
// 继续透传 queue 容量/观察窗/构建并发）。
func NewEngine(
	db *state.DB,
	rt capability.Runtime,
	b capability.Builder,
	edge capability.Edge,
	cipher *material.Cipher,
	app lynx.App,
	cfg *config.AppConfig,
) (*engine.Engine, error) {
	overlap, err := engine.ParseScheduleOverlap(cfg.ScheduleOverlapPolicy())
	if err != nil {
		return nil, err
	}
	return engine.New(engine.Deps{
		DB: db, Runtime: rt, Builder: b, Edge: edge, Cipher: cipher, Logger: app.Logger(),
	}, engine.Options{DataRoot: cfg.DataRoot(), ScheduleOverlap: overlap}), nil
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

// RetentionJanitorService 把保留窗清扫（幂等记录 + 事件 outbox + 上传产物）
// 适配为 lynx 托管服务：复用 engine.NewLoop 唯一循环骨架（架构 §0"每段只许有一
// 份"），不自建 ticker。具名类型：wire 对 lynx.Service 同型多 provider
// 需可区分。
type RetentionJanitorService struct {
	enforcer *idem.Enforcer
	db       *state.DB
	uploads  *sourceupload.Repo
	blobs    *upload.Store
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

// NewRetentionJanitorService 构造 janitor 托管服务。
func NewRetentionJanitorService(enforcer *idem.Enforcer, db *state.DB, cfg *config.AppConfig, app lynx.App) *RetentionJanitorService {
	return &RetentionJanitorService{
		enforcer: enforcer, db: db,
		uploads: sourceupload.New(db.Clock()),
		blobs:   upload.NewStore(cfg.DataRoot(), 0, 0),
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
