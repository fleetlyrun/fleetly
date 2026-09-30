package assembly

import (
	"time"

	"google.golang.org/grpc"

	"github.com/lynx-go/grpcapi/authz"
	grpcapiinterceptor "github.com/lynx-go/grpcapi/interceptor"
	"github.com/lynx-go/lynx"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/api/systemgrpc"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// grpcTimeout 是 gRPC 请求级超时兜底；SSE/流式面（events follow，N1）
// 落地时按方法豁免。
const grpcTimeout = 60 * time.Second

// NewGRPCServer 装配控制面 gRPC 服务：grpcapi 槽位链（ClientInfo 打头、
// Auth 居中、Validate 收尾）+ 流式同款执法 + 服务注册 +
// AssertAllRegisteredHavePolicy（已注册方法缺 authz 策略即拒绝启动，
// fail-closed，见 ADR-0021）。
func NewGRPCServer(
	app lynx.App,
	cfg *config.AppConfig,
	policySet *authz.PolicySet,
	authenticator *authn.Authenticator,
	system *systemgrpc.Service,
	services *fleetlygrpc.Services,
) (*lynxgrpc.Server, error) {
	unary, stream, err := NewInterceptors(policySet, authenticator)
	if err != nil {
		return nil, err
	}

	srv := lynxgrpc.NewServer(
		lynxgrpc.WithAddr(cfg.GRPCAddr()),
		lynxgrpc.WithTimeout(grpcTimeout),
		lynxgrpc.WithLogger(app.Logger()),
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

// NewInterceptors 构造完整拦截器链（unary + 流式）——assembly 服务器与
// apitest 夹具共用（夹具不经 lynx，但必须走同一条执法链，否则测试面与
// 生产面漂移）。
func NewInterceptors(policySet *authz.PolicySet, authenticator *authn.Authenticator) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor, error) {
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
	return chain, []grpc.StreamServerInterceptor{authenticator.Stream()}, nil
}
