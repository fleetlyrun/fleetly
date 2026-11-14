// Package fleetlygrpc 是 gRPC 输入面的共享组合根/门面：Services 装配全部
// handler 依赖，RegisterAll 注册各限界上下文 handler 包（structuregrpc/
// datagrpc/deliverygrpc/identitygrpc/…）；跨包共享原语走 grpcapi。读路径
// 直读聚合 repo；写路径四件一拍（engine 或 repo 事务内组合 outbox/audit）；
// 错误一律 apperr 信封。scope 注解已在 proto 写好，拦截器执法。
package fleetlygrpc

// Services 是 gRPC 输入面的组合根：按限界上下文分组持有 repo/模块消费面/
// 客户端/配置，经各 *wiring.go 投影为 handler 包的窄 Deps。handler 不依赖
// 本类型；字段提升（如 s.Projects）只服务组合根与测试夹具。
type Services struct {
	Core
	ModuleAPIs
	StructureDeps
	DataDeps
	DeliveryDeps
	TemplatesDeps
	IdentityDeps
	AutomationDeps
	PlatformDeps
	Clients
	Config
}

// Deps 是 Services 的装配依赖（组合根构造注入——P1b C 批构造上提：本包
// 零持久化构造，repo 实现选择住 assembly）。分组与 Services 同构；eventTickets
// 由 NewServices 以注入 DB 的时钟铸造，不由组合根提供。
type Deps struct {
	Core
	ModuleAPIs
	StructureDeps
	DataDeps
	DeliveryDeps
	TemplatesDeps
	IdentityDeps
	AutomationDeps
	PlatformDeps
	Clients
	Config
}

// WithWrites 返回附带受理位写原语的 Deps 副本。writes 是跨模块共用的
// 私有接口（不属于九个模块消费面），组合根用本方法注入同一 engine 实例；
// 参数赋值同时充当 *engine.Engine 对 writeAPI 的编译期断言。
func (d Deps) WithWrites(w writeAPI) Deps {
	d.writes = w
	return d
}

// NewServices 构造（deps 全量由组合根注入；eventTickets 以注入 DB 的时钟
// 铸造）。
func NewServices(deps Deps) *Services {
	core := deps.Core
	core.eventTickets = newEventTicketStore(deps.DB.Clock())
	return &Services{
		Core:           core,
		ModuleAPIs:     deps.ModuleAPIs,
		StructureDeps:  deps.StructureDeps,
		DataDeps:       deps.DataDeps,
		DeliveryDeps:   deps.DeliveryDeps,
		TemplatesDeps:  deps.TemplatesDeps,
		IdentityDeps:   deps.IdentityDeps,
		AutomationDeps: deps.AutomationDeps,
		PlatformDeps:   deps.PlatformDeps,
		Clients:        deps.Clients,
		Config:         deps.Config,
	}
}
