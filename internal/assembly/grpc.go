package assembly

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lynx-go/grpcapi/authz"
	grpcapiinterceptor "github.com/lynx-go/grpcapi/interceptor"
	"github.com/lynx-go/lynx"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/idem"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/systemgrpc"
)

// grpcShutdownTimeout 是 gRPC 优雅关停上限（在途请求的排水窗口）。注意
// lynx WithTimeout 的语义就是优雅关停超时（SC-05 历史命名警示：与 HTTP 侧
// WithTimeout 的"读写超时"同名不同义）——本常量不约束任何在途请求，
// 请求级超时由 grpcUnaryTimeout 拦截器承担。
const grpcShutdownTimeout = 60 * time.Second

// grpcUnaryTimeout 是 unary RPC 服务端硬上限。选值依据：与引擎
// ManagedStepTimeout（默认 30s）同量级——API 面背后的 docker/proxy 调用
// 均以该值为界，超过即属挂死而非慢；客户端自带更短 deadline 时以短者
// 为准（ctx 语义）。流式 RPC（StreamLogs / StreamBuildLogs / events
// follow 等）不走本拦截器，天然豁免——长流面的生命周期由取消信号管理。
const grpcUnaryTimeout = 30 * time.Second

// grpcMaxRecvMsgSize 是 gRPC 服务端单条请求消息上限（32MiB）。必须覆盖
// webhook 原生入口的 hookPayloadLimit（25MiB，对齐 GitHub）：否则大
// payload 死于 gRPC 默认 4MiB 的不透明 RESOURCE_EXHAUSTED，设计内的
// 413 永不可达（Q-11 限额错位）。单一真源经 GRPCServerOptions 下发到
// 装配服务器与 apitest bufconn 夹具，防两面漂移。
const grpcMaxRecvMsgSize = 32 << 20

// init 钉死包含关系（Go 无编译期数值比较，启动期断言代偿）：webhook
// 上限一旦越过 gRPC 收包上限，hook 面对大 payload 只会收获不透明的
// RESOURCE_EXHAUSTED——配置错位启动即红，不留到首个 25MiB 请求暴露。
func init() {
	if hookPayloadLimit > grpcMaxRecvMsgSize {
		panic("assembly: hookPayloadLimit exceeds grpcMaxRecvMsgSize; webhook payloads must fail with the designed 413, not gRPC RESOURCE_EXHAUSTED")
	}
}

// GRPCServerOptions 返回传输层服务端选项的单一真源（收包限额等）：
// lynx 服务器（WithServerOptions 透传）与 apitest bufconn 夹具共用——
// 夹具缺同一限额会让大请求面的测试结果失真。
func GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(grpcMaxRecvMsgSize)}
}

// longRunningUnary 是通用 30s unary 预算的豁免面（维护型长执行动词——
// 服务端自带的执行硬上限就是它们的界，超时拦截器不重复设界）：
// - RebuildNetwork（ADR-0046）：detach 排水（stop-first + StopGrace 量级）
//   - 复建 + re-attach 全序以引擎 NetworkRebuildTimeout（缺省 5m）为界；
//     30s 通用预算会腰斩真实排水窗。CLI 侧同款豁免（noDeadline 拨号）。
var longRunningUnary = map[string]bool{
	"/fleetly.structure.v1.NetworksService/RebuildNetwork": true,
}

// newUnaryTimeoutInterceptor 构造 unary 请求超时拦截器：handler 的 ctx
// 包 WithTimeout，挂死的 handler 经 ctx 取消链回收（仓储/docker 调用均
// 透传 ctx），不再无限占用连接与 goroutine。longRunningUnary 豁免面
// 原样放行（各自的引擎级硬上限承担防挂死职责）。
func newUnaryTimeoutInterceptor(timeout time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if longRunningUnary[info.FullMethod] {
			return handler(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handler(ctx, req)
	}
}

// NewGRPCServer 装配控制面 gRPC 服务：grpcapi 槽位链（ClientInfo 打头、
// Auth 居中、Validate 收尾）+ 流式同款执法 + 服务注册 +
// AssertAllRegisteredHavePolicy（已注册方法缺 authz 策略即拒绝启动，
// fail-closed，见 ADR-0021）。
func NewGRPCServer(
	app lynx.App,
	cfg *config.AppConfig,
	policySet *authz.PolicySet,
	authenticator *authn.Authenticator,
	enforcer *idem.Enforcer,
	limiter *governance.RateLimiter,
	freeze *governance.FreezeGuard,
	system *systemgrpc.Service,
	services *fleetlygrpc.Services,
) (*lynxgrpc.Server, error) {
	unary, stream, err := NewInterceptors(app.Logger(), policySet, authenticator, enforcer, limiter, freeze)
	if err != nil {
		return nil, err
	}

	srv := lynxgrpc.NewServer(
		lynxgrpc.WithAddr(cfg.GRPCAddr()),
		// SC-05 别名：与 HTTP 侧同名选项对齐，明确这是关停超时而非请求超时。
		lynxgrpc.WithShutdownTimeout(grpcShutdownTimeout),
		lynxgrpc.WithLogger(app.Logger()),
		lynxgrpc.WithServerOptions(GRPCServerOptions()...),
		lynxgrpc.WithInterceptors(unary...),
		lynxgrpc.WithStreamInterceptors(stream...),
	)
	grpcSrv := srv.GetServer()

	systemv1.RegisterSystemServiceServer(grpcSrv, system)
	fleetlygrpc.RegisterAll(grpcSrv, services)

	// fail-closed：全部已注册方法（含框架服务豁免外的业务方法）必须带
	// authz 策略，缺失即启动失败——注解缺失不能等到首个请求才暴露。
	if err := grpcapiinterceptor.AssertAllRegisteredHavePolicy(grpcSrv, policySet); err != nil {
		return nil, err
	}
	return srv, nil
}

// newErrorExitInterceptor 构造出口纪律拦截器（P0-3，unary + 流式同款）：
// 链头挂载（一切离服错误的最后一站）。E_INTERNAL（及 Internal/Unknown
// 机械错误）经 apperrstatus.SanitizeExit 单点收口——message 脱敏、铸造 error_id
// （outgoing trailer metadata x-fleetly-error-id 承载，信封 context 同步
// 携带）、cause 全文落服务端日志（error_id 关联）。非 E_INTERNAL 原样
// 放行（REST gateway 的错误路由共用同一单点，转发链幂等复用 id）。
func newErrorExitInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		return resp, sanitizeExitError(ctx, logger, err, info.FullMethod)
	}
}

// newStreamErrorExitInterceptor 是流式面的出口纪律拦截器（语义同 unary 款；
// trailer 经 ServerStream 直设）。
func newStreamErrorExitInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, ss)
		if err == nil {
			return nil
		}
		errorID, sanitized := apperrstatus.SanitizeExit(ss.Context(), logger, err, info.FullMethod)
		if errorID != "" {
			ss.SetTrailer(metadata.Pairs(apperrstatus.ErrorIDHeader, errorID))
		}
		return sanitized
	}
}

// sanitizeExitError 是 unary 出口的收口辅助：单点脱敏 + trailer 承载。
func sanitizeExitError(ctx context.Context, logger *slog.Logger, err error, method string) error {
	if err == nil {
		return nil
	}
	errorID, sanitized := apperrstatus.SanitizeExit(ctx, logger, err, method)
	if errorID != "" {
		// trailer 与 status 同批上线（guaranteed delivery 面），非 status 元
		// 素消费方（grpcurl 等）可直读；proto 消费方走信封 detail。
		_ = grpc.SetTrailer(ctx, metadata.Pairs(apperrstatus.ErrorIDHeader, errorID)) //nolint:errcheck // trailer 失败不掩盖业务错误
	}
	return sanitized
}

// NewInterceptors 构造完整拦截器链（unary + 流式）——assembly 服务器与
// apitest 夹具共用（夹具不经 lynx，但必须走同一条执法链，否则测试面与
// 生产面漂移）。链序（ADR-0017/0024）：出口纪律（链头，P0-3——E_INTERNAL
// 三面统一收口）→ 执法链（clientinfo/auth/validate）→ 冻结拦截器（拒绝
// 不占幂等记录）→ 幂等执法器（重放不消耗业务超时预算、鉴权失败不占幂等
// 记录）→ 创建速率限制器（仅实际执行计数——重放不消耗预算）→ 请求超时。
// 流式链：出口纪律 → authn → 冻结（首帧 RecvMsg 执法，F1.10 起
// client-streaming 写面在封禁面——ADR-0019 附录 A.2）；幂等/速率/超时保持
// unary 面（上传的自然幂等=内容寻址，限额刹车=大小上限与项目存量配额，
// 流生命周期由取消信号管理）。
func NewInterceptors(logger *slog.Logger, policySet *authz.PolicySet, authenticator *authn.Authenticator, enforcer *idem.Enforcer, limiter *governance.RateLimiter, freeze *governance.FreezeGuard) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor, error) {
	chain, err := grpcapiinterceptor.Assemble(
		grpcapiinterceptor.ChainItem{
			Slot:        grpcapiinterceptor.SlotClientInfo,
			Interceptor: grpcapiinterceptor.NewClientInfo(grpcapiinterceptor.ClientInfoConfig{}).Unary(),
		},
		grpcapiinterceptor.ChainItem{
			Slot:        grpcapiinterceptor.SlotAuth,
			Interceptor: authenticator.Unary(),
		},
		grpcapiinterceptor.ChainItem{
			Slot:        grpcapiinterceptor.SlotValidate,
			Interceptor: grpcapiinterceptor.NewValidate().Unary(),
		},
	)
	if err != nil {
		return nil, nil, err
	}
	// 出口纪律挂链头（最外层）：执法链自身的失败（幂等查询失败等）与
	// handler 失败同走收口面，不漏内部细节。
	chain = append([]grpc.UnaryServerInterceptor{newErrorExitInterceptor(logger)}, chain...)
	if freeze != nil {
		chain = append(chain, freeze.Unary())
	}
	chain = append(chain, enforcer.Unary())
	chain = append(chain, limiter.Unary())
	// 请求级超时挂链尾（最贴近 handler）：执法链（clientinfo/auth/validate）
	// 在 deadline 外运行——超时的请求同样要过完整的执法与未来的审计/用量
	// 面，且鉴权查询不被请求 deadline 误杀；流式链不加超时（follow 面与
	// 上传流面豁免——生命周期由取消信号管理，上传另有 15m 流硬上限）。
	chain = append(chain, newUnaryTimeoutInterceptor(grpcUnaryTimeout))
	stream := []grpc.StreamServerInterceptor{newStreamErrorExitInterceptor(logger), authenticator.Stream()}
	if freeze != nil {
		stream = append(stream, freeze.Stream())
	}
	return chain, stream, nil
}
