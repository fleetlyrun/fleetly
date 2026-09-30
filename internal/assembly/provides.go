package assembly

import (
	"github.com/google/wire"
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
	lynxgrpc "github.com/lynx-go/lynx/server/grpc"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/api/systemgrpc"
	"github.com/fleetlyrun/fleetly/internal/config"
)

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// ProviderSet 是 fleetlyd 的完整依赖图。生命周期 provider（hooks）随批次
// 扩展：store 迁移（PreStart）、Provider reconcile（Drain 前）、引擎排空
// （PreStop）等按 ADR-0005 语义逐批挂入。
var ProviderSet = wire.NewSet(
	boot.New,
	NewAppConfig,
	NewPolicySet,
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

// NewPreStartHooks 启动前钩子（OnPreStart，先于监听）：当前为空；goose
// 迁移与密封密钥初始化随 state 批次挂入。
func NewPreStartHooks() boot.PreStartHooks { return nil }

// NewDrainHooks 排水钩子（OnDrain，摘流窗口内执行）：当前为空；Managed
// Provider 摘流随 Edge 批次挂入。
func NewDrainHooks() boot.DrainHooks { return nil }

// NewPreStopHooks 停止前钩子（OnPreStop，服务仍在处理在途请求）：当前为
// 空；引擎排空（in-flight Ensure 可安全中断，ADR-0005）随部署链挂入。
func NewPreStopHooks() boot.PreStopHooks { return nil }

// NewPostStopHooks 收尾钩子（OnPostStop 预算内执行）：当前为空；wire
// cleanup 已单独经 Bootstrap 返回值挂载。
func NewPostStopHooks() boot.PostStopHooks { return nil }

// NewServices 返回服务注册顺序：grpc → gateway。lynx 按注册顺序有界停止；
// gateway 与 grpc 间为惰性共享连接，排水窗口（WithDrainTimeout）覆盖停止
// 期间的残余转发请求。
func NewServices(
	grpcServer *lynxgrpc.Server,
	gateway *lynxhttp.Server,
) []lynx.Service {
	return []lynx.Service{grpcServer, gateway}
}
