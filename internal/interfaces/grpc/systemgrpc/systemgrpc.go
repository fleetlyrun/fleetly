// Package systemgrpc 实现 fleetly.system.v1.SystemService：控制面自身
// 状态面（`fleetly status` / `fleetly doctor` 消费）与能力自描述面
// （`fleetly schema` / `fleetly explain`，F1.4）。
package systemgrpc

import (
	"context"
	"errors"
	"strings"

	"github.com/lynxland/lynx/buildinfo"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/application/schema"
)

// Engine 是系统面消费的引擎窄接口：健康组件行 + 受管组件重启路由。
// 组合根注入 *engine.Engine（结构实现）；本包不 import 引擎实现，方法集
// 漂移在 assembly 的编译期断言处红。
type Engine interface {
	HealthComponents() []api.HealthComponent
	RestartComponent(ctx context.Context, name string) (int, error)
	// ComponentsHealthSnapshot 是组件健康快照导出（B2b 共享导出——TTL 缓存
	// 单源在 engine，status 面与事故装配双消费；快照面缺席 = nil）。
	ComponentsHealthSnapshot(ctx context.Context) []capability.ComponentHealth
}

// Service 满足 systemv1.SystemServiceServer。状态聚合 = 进程内在册
// Provider 实例的 Health() 逐项探活（IA v3 二期③接线——架构 §8 降级
// 矩阵驱动；任一不健康/超时即 DEGRADED）。checkers 为空 = 无在册实例
// （apitest 手工装配面），聚合态恒 HEALTHY 且 components 缺席。
type Service struct {
	systemv1.UnimplementedSystemServiceServer

	info buildinfo.BuildInfo
	// engine 是组件重启与健康行路由面（可空 = apitest 手工装配；缺席时
	// 重启精确失败——E_RUNTIME_FACE_UNSUPPORTED）。
	engine Engine
	// engineHealth 是引擎健康组件查询窄缝（R02-M7：环死亡 / 收敛能力行；
	// 缺省委托 Engine.HealthComponents，测试可注入）。
	engineHealth func() []api.HealthComponent
}

// New 构造 SystemService（engine 可空）。
func New(info buildinfo.BuildInfo, eng Engine) *Service {
	return &Service{info: info, engine: eng}
}

// GetVersion 返回控制面构建信息。
func (s *Service) GetVersion(context.Context, *systemv1.GetVersionRequest) (*systemv1.GetVersionResponse, error) {
	return &systemv1.GetVersionResponse{
		Version: s.info.Version,
		Commit:  s.info.Commit,
		Date:    s.info.Date,
	}, nil
}

// GetStatus 返回控制面服务状态（逐项 Health() 并行探活 + 聚合）。
//
// 两面形态（R08-M9）：公开/匿名面只回最小态（state + version；components
// 空 repeated 按 protojson 约定省略）——components 携带内部地址与上游
// 错误原文，仅已认证调用方（拦截器注入 Identity，PUBLIC 面携有效凭证同样
// 有身份）可见。聚合态由缓存快照计算（匿名方也看不到逐项详情，但 degraded
// 事实不受影响）。
func (s *Service) GetStatus(ctx context.Context, _ *systemv1.GetStatusRequest) (*systemv1.GetStatusResponse, error) {
	components := s.componentsCached(ctx)
	state := systemv1.StatusState_STATUS_STATE_HEALTHY
	for _, component := range components {
		if !component.GetHealthy() {
			state = systemv1.StatusState_STATUS_STATE_DEGRADED
			break
		}
	}
	resp := &systemv1.GetStatusResponse{
		State:   state,
		Version: s.info.Version,
	}
	if _, ok := authn.FromContext(ctx); ok {
		resp.Components = components
	}
	return resp, nil
}

// componentsCached 返回 TTL 内的探活快照（B2b 共享导出：聚合单源在
// engine——TTL 缓存/单飞/WithoutCancel 全随快照器；引擎健康行已在快照内
// 并入，status 面直映不重复）。engine 缺席（apitest 手工装配）= 引擎行
// 经 engineHealthRows 兜底在册。
func (s *Service) componentsCached(ctx context.Context) []*systemv1.ComponentHealth {
	var snap []capability.ComponentHealth
	if s.engine != nil {
		snap = s.engine.ComponentsHealthSnapshot(context.WithoutCancel(ctx))
	}
	out := make([]*systemv1.ComponentHealth, 0, len(snap))
	for _, c := range snap {
		out = append(out, &systemv1.ComponentHealth{
			Name:       c.Name,
			Capability: c.Capability,
			Healthy:    c.Healthy,
			Details:    c.Details,
		})
	}
	if len(out) == 0 {
		// engine 缺席（apitest 手工装配）：引擎健康行兜底在册（R02-M7 口径）。
		for _, row := range s.engineHealthRows() {
			out = append(out, &systemv1.ComponentHealth{
				Name: row.Name, Capability: "engine", Healthy: row.Healthy, Details: row.Details,
			})
		}
	}
	return out
}

// engineHealthRows 返回引擎健康组件行（窄缝优先，其次 Engine 接口）。
func (s *Service) engineHealthRows() []api.HealthComponent {
	if s.engineHealth != nil {
		return s.engineHealth()
	}
	if s.engine == nil {
		return nil
	}
	return s.engine.HealthComponents()
}

// RestartComponent 重启受管组件载体（IA v3 二期④）：引擎按 Provider 名
// 路由（capability.Managed 声明者），经 Runtime 重启子面逐 Workload 强制
// 重排；哨兵错误 map 为精确错误码。
func (s *Service) RestartComponent(ctx context.Context, req *systemv1.RestartComponentRequest) (*systemv1.RestartComponentResponse, error) {
	// 平台管理员门（R08-H1）：重启受管组件影响全平台载体，门在引擎路由
	// 与重启副作用之前（403 先于 501/404——不向非平台管理员暴露组件面
	// 状态）。判定复用 fleetlygrpc 的同一真源，不复制 role/team 条件。
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if s.engine == nil {
		// 引擎面缺席是 honest failure（P4-3 批 3）：精确 501，不再落
		// E_INTERNAL 脱敏 500。
		return nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "component restart requires the engine face")
	}
	restarted, err := s.engine.RestartComponent(ctx, req.GetName())
	if err != nil {
		switch {
		case errors.Is(err, api.ErrComponentUnknown):
			return nil, apperr.New("E_NOT_FOUND", "component %q: unknown managed provider name", req.GetName())
		case errors.Is(err, api.ErrRestartUnsupported):
			return nil, apperr.New("E_CONFLICT", "component %q: no managed workloads to restart", req.GetName())
		case errors.Is(err, api.ErrRuntimeNoRestart):
			return nil, apperr.New("E_INTERNAL", "the runtime provider does not implement carrier restart")
		}
		return nil, err
	}
	// G115：restarted 是单组件载体内活计数（Provider 声明面，个位数域），
	// int32 溢出面不存在。
	return &systemv1.RestartComponentResponse{Restarted: int32(restarted)}, nil //nolint:gosec
}

// GetSchema 返回能力自描述全量文档（Spec 契约 + 事件 payload schema；
// schema.Build 合并 builtin 与各扩展面注入——进程内链接到谁就含谁的贡献，
// 服务器二进制经 assembly 链接全量）。
func (s *Service) GetSchema(context.Context, *systemv1.GetSchemaRequest) (*systemv1.GetSchemaResponse, error) {
	doc := schema.Build()
	entries := make([]*systemv1.SchemaEntry, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		entries = append(entries, schemaEntryMsg(e))
	}
	return &systemv1.GetSchemaResponse{Entries: entries}, nil
}

// Explain 返回单个资源的自描述（寻址名裁决在 schema 注册表单源）。
func (s *Service) Explain(_ context.Context, req *systemv1.ExplainRequest) (*systemv1.ExplainResponse, error) {
	// resource 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截（文案随字段原文逐字节上注解）；
	// TrimSpace 归一保留——未知名裁决在注册表单源。
	resource := strings.TrimSpace(req.GetResource())
	e, ok := schema.Lookup(resource)
	if !ok {
		return nil, apperr.New("E_NOT_FOUND", "resource %q is not explainable; run 'fleetly schema' to list every name", resource)
	}
	return &systemv1.ExplainResponse{Entry: schemaEntryMsg(e)}, nil
}

// schemaEntryMsg 把 schema 条目转 proto 消息（schema_json = canonical
// 紧凑 JSON，与 golden 漂移门同一字节）。
func schemaEntryMsg(e schema.Entry) *systemv1.SchemaEntry {
	return &systemv1.SchemaEntry{
		Name: e.Name, Kind: string(e.Kind), Summary: e.Summary, SchemaJson: e.SchemaJSON(),
	}
}
