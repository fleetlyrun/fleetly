// Package systemgrpc 实现 fleetly.system.v1.SystemService：控制面自身
// 状态面（`fleetly status` / `fleetly doctor` 消费）与能力自描述面
// （`fleetly schema` / `fleetly explain`，F1.4）。
package systemgrpc

import (
	"context"
	"strings"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/schema"
)

// Service 满足 systemv1.SystemServiceServer。状态判定目前只看进程存活
// （控制面起来了即 healthy）；Capability Health() 降级矩阵接入后，
// DEGRADED 由 checkers 聚合派生（F0.19 随 swarm Provider 落地）。
type Service struct {
	systemv1.UnimplementedSystemServiceServer

	info buildinfo.BuildInfo
}

// New 构造 SystemService。
func New(info buildinfo.BuildInfo) *Service {
	return &Service{info: info}
}

// GetVersion 返回控制面构建信息。
func (s *Service) GetVersion(context.Context, *systemv1.GetVersionRequest) (*systemv1.GetVersionResponse, error) {
	return &systemv1.GetVersionResponse{
		Version: s.info.Version,
		Commit:  s.info.Commit,
		Date:    s.info.Date,
	}, nil
}

// GetStatus 返回控制面服务状态。
func (s *Service) GetStatus(context.Context, *systemv1.GetStatusRequest) (*systemv1.GetStatusResponse, error) {
	return &systemv1.GetStatusResponse{
		State:   systemv1.StatusState_STATUS_STATE_HEALTHY,
		Version: s.info.Version,
	}, nil
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
