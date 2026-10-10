// Package systemgrpc 实现 fleetly.system.v1.SystemService：控制面自身
// 状态面（`fleetly status` / `fleetly doctor` 消费）与能力自描述面
// （`fleetly schema` / `fleetly explain`，F1.4）。
package systemgrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/schema"
)

// Service 满足 systemv1.SystemServiceServer。状态聚合 = 进程内在册
// Provider 实例的 Health() 逐项探活（IA v3 二期③接线——架构 §8 降级
// 矩阵驱动；任一不健康/超时即 DEGRADED）。checkers 为空 = 无在册实例
// （apitest 手工装配面），聚合态恒 HEALTHY 且 components 缺席。
type Service struct {
	systemv1.UnimplementedSystemServiceServer

	info     buildinfo.BuildInfo
	checkers []capability.Provider
	// engine 是组件重启的路由面（可空 = apitest 手工装配；缺席时重启
	// 精确失败——E_INTERNAL）。
	engine *engine.Engine
}

// healthCheckTimeout 是单项探活预算（TCP 拨号类检查自带更短超时；此处
// 兜底防慢检查拖垮 status 面）。
const healthCheckTimeout = 2 * time.Second

// New 构造 SystemService（checkers/engine 均可空）。
func New(info buildinfo.BuildInfo, checkers []capability.Provider, eng *engine.Engine) *Service {
	return &Service{info: info, checkers: checkers, engine: eng}
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
func (s *Service) GetStatus(ctx context.Context, _ *systemv1.GetStatusRequest) (*systemv1.GetStatusResponse, error) {
	components := s.checkComponents(ctx)
	state := systemv1.StatusState_STATUS_STATE_HEALTHY
	for _, component := range components {
		if !component.GetHealthy() {
			state = systemv1.StatusState_STATUS_STATE_DEGRADED
			break
		}
	}
	return &systemv1.GetStatusResponse{
		State:      state,
		Version:    s.info.Version,
		Components: components,
	}, nil
}

// checkComponents 并行探活在册 Provider（单项 2s 预算；超时/panic 按
// 不健康计——降级矩阵的诚实口径）。
func (s *Service) checkComponents(ctx context.Context) []*systemv1.ComponentHealth {
	out := make([]*systemv1.ComponentHealth, len(s.checkers))
	var wg sync.WaitGroup
	for i, provider := range s.checkers {
		if provider == nil {
			continue
		}
		wg.Add(1)
		go func(i int, provider capability.Provider) {
			defer wg.Done()
			describe := provider.Describe()
			component := &systemv1.ComponentHealth{
				Name:       describe.Name,
				Capability: string(describe.Capability),
			}
			out[i] = component
			defer func() {
				if r := recover(); r != nil {
					component.Healthy = false
					component.Details = fmt.Sprintf("health check panicked: %v", r)
				}
			}()
			checkCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
			defer cancel()
			done := make(chan capability.HealthReport, 1)
			go func() {
				done <- provider.Health(checkCtx)
			}()
			select {
			case report := <-done:
				component.Healthy = report.Healthy
				component.Details = report.Details
			case <-checkCtx.Done():
				component.Healthy = false
				component.Details = "health check timed out"
			}
		}(i, provider)
	}
	wg.Wait()
	return out
}

// RestartComponent 重启受管组件载体（IA v3 二期④）：引擎按 Provider 名
// 路由（capability.Managed 声明者），经 Runtime 重启子面逐 Workload 强制
// 重排；哨兵错误 map 为精确错误码。
func (s *Service) RestartComponent(ctx context.Context, req *systemv1.RestartComponentRequest) (*systemv1.RestartComponentResponse, error) {
	if s.engine == nil {
		return nil, apperr.New("E_INTERNAL", "component restart requires the engine face")
	}
	restarted, err := s.engine.RestartComponent(ctx, req.GetName())
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrComponentUnknown):
			return nil, apperr.New("E_NOT_FOUND", "component %q: unknown managed provider name", req.GetName())
		case errors.Is(err, engine.ErrRestartUnsupported):
			return nil, apperr.New("E_CONFLICT", "component %q: no managed workloads to restart", req.GetName())
		case errors.Is(err, engine.ErrRuntimeNoRestart):
			return nil, apperr.New("E_INTERNAL", "the runtime provider does not implement carrier restart")
		}
		return nil, err
	}
	return &systemv1.RestartComponentResponse{Restarted: int32(restarted)}, nil
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
	resource := strings.TrimSpace(req.GetResource())
	if resource == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "resource: must not be empty (a spec kind like \"app\" or an event name like \"deployment.succeeded\")")
	}
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
